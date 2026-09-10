package gateway_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	coreRepo "core-banking-ledger/internal/repository"
	coreService "core-banking-ledger/internal/service"

	gwapi "core-banking-ledger/internal/gateway/api"
	gwdomain "core-banking-ledger/internal/gateway/domain"
	gwrepo "core-banking-ledger/internal/gateway/repository"
	gwservice "core-banking-ledger/internal/gateway/service"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

var dashMigrateOnce sync.Once

func setupDashboardTest(t *testing.T) (*sql.DB, http.Handler, *gwservice.AuthService, *gwservice.GatewayService, *gwservice.AdminDashboardService, *gwservice.ReconciliationRunner) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		dbURL = os.Getenv("DATABASE_URL")
	}
	if dbURL == "" {
		dbURL = "postgres://ledger_user:ledger_password@localhost:5432/ledger_db?sslmode=disable"
	}

	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}

	if err := db.Ping(); err != nil {
		t.Skipf("Skipping dashboard test because PostgreSQL is unreachable: %v", err)
	}

	dashMigrateOnce.Do(func() {
		for _, f := range []string{"migrations/001_init_schema.sql", "migrations/002_api_gateway.sql", "migrations/003_admin_dashboard.sql", "../../migrations/001_init_schema.sql", "../../migrations/002_api_gateway.sql", "../../migrations/003_admin_dashboard.sql"} {
			content, err := os.ReadFile(f)
			if err == nil && len(content) > 0 {
				_, _ = db.Exec(string(content))
			}
		}
	})

	cRepo := coreRepo.NewRepository(db)
	cService := coreService.NewService(cRepo)

	gRepo := gwrepo.NewGatewayRepository(db)
	jwtSecret := "dashboard-test-secret-key-32bytes"
	authSvc := gwservice.NewAuthService(jwtSecret, 15*time.Minute)
	gService := gwservice.NewGatewayService(gRepo, cService, authSvc, 7*24*time.Hour)

	reconciler := gwservice.NewReconciliationRunner(gRepo, cService)
	dashSvc := gwservice.NewAdminDashboardService(gRepo, cService, reconciler)

	handler := gwapi.NewGatewayHandler(gService)
	dashHandler := gwapi.NewAdminDashboardHandler(dashSvc)
	router := gwapi.NewGatewayRouter(handler, dashHandler, nil, nil, nil, authSvc, gRepo, []string{"*"})

	return db, router, authSvc, gService, dashSvc, reconciler
}

// 1. RECONCILIATION DISCREPANCY DETECTION TEST

func TestDashboard_ReconciliationDetectsArtificiallyIntroducedImbalance(t *testing.T) {
	db, _, _, gService, _, reconciler := setupDashboardTest(t)
	ctx := context.Background()

	// 1. Create a user & account
	user, err := gService.RegisterUser(ctx, gwdomain.RegisterRequest{
		Email:    fmt.Sprintf("imbalance_%s@example.com", uuid.New().String()[:8]),
		Password: "Password123!",
	})
	if err != nil {
		t.Fatalf("RegisterUser failed: %v", err)
	}

	acc, err := gService.CreateAccount(ctx, user.UserID, gwdomain.CreateGatewayAccountRequest{
		Name:        "Test Account Imbalance",
		AccountType: "asset",
		Currency:    "USD",
	})
	if err != nil {
		t.Fatalf("CreateAccount failed: %v", err)
	}

	// 2. Artificially tamper with cached_balance_cents in account_balances table to introduce an imbalance mismatch!
	_, err = db.ExecContext(ctx, "UPDATE account_balances SET cached_balance_cents = 999999 WHERE account_id = $1", acc.AccountID)
	if err != nil {
		t.Fatalf("Failed to artificially tamper account balance: %v", err)
	}
	defer func() {
		_, _ = db.ExecContext(context.Background(), "UPDATE account_balances SET cached_balance_cents = 0 WHERE account_id = $1", acc.AccountID)
	}()

	// 3. Run reconciliation check
	result, err := reconciler.RunReconciliation(ctx, "test_imbalance_check")
	if err != nil {
		t.Fatalf("RunReconciliation failed: %v", err)
	}

	if result.MismatchesCount == 0 {
		t.Fatalf("TEST FAILURE: Reconciliation runner failed to detect artificially introduced balance discrepancy!")
	}

	var found bool
	for _, m := range result.Mismatches {
		if m.AccountID == acc.AccountID {
			found = true
			if m.DifferenceCents == 0 {
				t.Errorf("Expected non-zero difference cents for account %s", acc.AccountID)
			}
		}
	}
	if !found {
		t.Errorf("Expected account %s in mismatches list", acc.AccountID)
	}
}

// 2. NON-ADMIN ACCESS DENIAL TEST

func TestDashboard_NonAdminCannotAccessDashboardEndpoints(t *testing.T) {
	_, router, _, gService, _, _ := setupDashboardTest(t)
	ctx := context.Background()

	// Register non-admin user
	normalUser, err := gService.RegisterUser(ctx, gwdomain.RegisterRequest{
		Email:    fmt.Sprintf("user_dashboard_%s@example.com", uuid.New().String()[:8]),
		Password: "Password123!",
	})
	if err != nil {
		t.Fatalf("RegisterUser failed: %v", err)
	}

	loginResp, err := gService.LoginUser(ctx, gwdomain.LoginRequest{
		Email:    normalUser.Email,
		Password: "Password123!",
	})
	if err != nil {
		t.Fatalf("LoginUser failed: %v", err)
	}

	endpoints := []struct {
		method string
		path   string
	}{
		{"GET", "/admin/dashboard/health"},
		{"POST", "/admin/dashboard/reconcile/run"},
		{"GET", "/admin/dashboard/reconcile/history"},
		{"GET", "/admin/dashboard/transactions/flagged"},
		{"GET", "/admin/dashboard/kyc/queue"},
		{"GET", "/admin/audit-logs"},
	}

	for _, ep := range endpoints {
		req := httptest.NewRequest(ep.method, ep.path, nil)
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", loginResp.AccessToken))
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("SECURITY FAILURE: Non-admin accessed %s %s! Expected 403 Forbidden, got %d", ep.method, ep.path, w.Code)
		}
	}
}

// 3. ADMIN FREEZE, REVERSAL & AUDIT TRAIL TEST

func TestDashboard_AdminFreezeReversalAndAuditTrail(t *testing.T) {
	db, router, _, gService, _, _ := setupDashboardTest(t)
	ctx := context.Background()

	// 1. Create Admin user & Login
	adminUser, err := gService.RegisterUser(ctx, gwdomain.RegisterRequest{
		Email:    fmt.Sprintf("admin_ops_%s@example.com", uuid.New().String()[:8]),
		Password: "Password123!",
		IsAdmin:  true,
	})
	if err != nil {
		t.Fatalf("RegisterUser Admin failed: %v", err)
	}

	loginAdmin, err := gService.LoginUser(ctx, gwdomain.LoginRequest{
		Email:    adminUser.Email,
		Password: "Password123!",
	})
	if err != nil {
		t.Fatalf("LoginUser Admin failed: %v", err)
	}

	// 2. Create Target Account
	targetAcc, err := gService.CreateAccount(ctx, adminUser.UserID, gwdomain.CreateGatewayAccountRequest{
		Name:        "Ops Freeze Target",
		AccountType: "asset",
		Currency:    "USD",
	})
	if err != nil {
		t.Fatalf("CreateAccount failed: %v", err)
	}

	// 3. Freeze Account via Admin API
	freezeBody, _ := json.Marshal(map[string]string{"reason": "Court Order #501"})
	reqFreeze := httptest.NewRequest("POST", fmt.Sprintf("/admin/accounts/%s/freeze", targetAcc.AccountID), bytes.NewBuffer(freezeBody))
	reqFreeze.Header.Set("Authorization", fmt.Sprintf("Bearer %s", loginAdmin.AccessToken))
	reqFreeze.Header.Set("Content-Type", "application/json")
	wFreeze := httptest.NewRecorder()
	router.ServeHTTP(wFreeze, reqFreeze)

	if wFreeze.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK on Freeze, got %d: %s", wFreeze.Code, wFreeze.Body.String())
	}

	// Verify Account status in DB is frozen
	var statusStr string
	err = db.QueryRowContext(ctx, "SELECT status FROM accounts WHERE account_id = $1", targetAcc.AccountID).Scan(&statusStr)
	if err != nil || statusStr != "frozen" {
		t.Errorf("Expected account status 'frozen', got '%s'", statusStr)
	}

	// 4. Unfreeze Account via Admin API
	unfreezeBody, _ := json.Marshal(map[string]string{"reason": "Court Order Lifted"})
	reqUnfreeze := httptest.NewRequest("POST", fmt.Sprintf("/admin/accounts/%s/unfreeze", targetAcc.AccountID), bytes.NewBuffer(unfreezeBody))
	reqUnfreeze.Header.Set("Authorization", fmt.Sprintf("Bearer %s", loginAdmin.AccessToken))
	reqUnfreeze.Header.Set("Content-Type", "application/json")
	wUnfreeze := httptest.NewRecorder()
	router.ServeHTTP(wUnfreeze, reqUnfreeze)

	if wUnfreeze.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK on Unfreeze, got %d: %s", wUnfreeze.Code, wUnfreeze.Body.String())
	}

	// 5. Verify API Audit Log Record created for Admin Actions
	var auditCount int
	for i := 0; i < 10; i++ {
		time.Sleep(100 * time.Millisecond)
		err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM api_audit_logs WHERE user_id = $1 AND endpoint LIKE '/admin/accounts/%'", adminUser.UserID).Scan(&auditCount)
		if err == nil && auditCount >= 2 {
			break
		}
	}
	if auditCount < 2 {
		t.Errorf("Expected at least 2 audit log records for Admin freeze/unfreeze, got %d (err: %v)", auditCount, err)
	}
}

// 4. KYC APPLICATION REVIEW QUEUE TEST

func TestDashboard_KYCQueueReviewFlow(t *testing.T) {
	_, router, _, gService, dashSvc, _ := setupDashboardTest(t)
	ctx := context.Background()

	// 1. Register User & Admin
	user, _ := gService.RegisterUser(ctx, gwdomain.RegisterRequest{Email: fmt.Sprintf("kyc_user_%s@example.com", uuid.New().String()[:8]), Password: "Password123!"})
	admin, _ := gService.RegisterUser(ctx, gwdomain.RegisterRequest{Email: fmt.Sprintf("kyc_admin_%s@example.com", uuid.New().String()[:8]), Password: "Password123!", IsAdmin: true})

	loginAdmin, _ := gService.LoginUser(ctx, gwdomain.LoginRequest{Email: admin.Email, Password: "Password123!"})

	// 2. Submit KYC Application
	kycApp, err := dashSvc.SubmitKYCApplication(ctx, user.UserID, "Alice Smith", "1990-05-15", "ID-98765432")
	if err != nil {
		t.Fatalf("SubmitKYCApplication failed: %v", err)
	}

	// 3. Admin Reviews & Approves KYC Application via API
	reviewBody, _ := json.Marshal(map[string]string{"status": "approved"})
	reqRev := httptest.NewRequest("POST", fmt.Sprintf("/admin/dashboard/kyc/%s/review", kycApp.KYCID), bytes.NewBuffer(reviewBody))
	reqRev.Header.Set("Authorization", fmt.Sprintf("Bearer %s", loginAdmin.AccessToken))
	reqRev.Header.Set("Content-Type", "application/json")
	wRev := httptest.NewRecorder()
	router.ServeHTTP(wRev, reqRev)

	if wRev.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK on KYC review, got %d: %s", wRev.Code, wRev.Body.String())
	}

	// Verify status updated to approved
	apps, err := dashSvc.GetKYCApplications(ctx, "approved")
	if err != nil {
		t.Fatalf("GetKYCApplications failed: %v", err)
	}

	var found bool
	for _, a := range apps {
		if a.KYCID == kycApp.KYCID {
			found = true
			if a.Status != "approved" {
				t.Errorf("Expected status 'approved', got '%s'", a.Status)
			}
		}
	}
	if !found {
		t.Errorf("Expected approved KYC application in queue")
	}
}
