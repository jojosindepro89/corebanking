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
	"core-banking-ledger/internal/domain"

	gwapi "core-banking-ledger/internal/gateway/api"
	gwdomain "core-banking-ledger/internal/gateway/domain"
	gwrepo "core-banking-ledger/internal/gateway/repository"
	gwservice "core-banking-ledger/internal/gateway/service"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/pquerna/otp/totp"
)

var gwMigrateOnce sync.Once

func setupGatewayTest(t *testing.T) (*sql.DB, http.Handler, *gwservice.AuthService, *gwservice.GatewayService) {
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
		t.Skipf("Skipping integration test because PostgreSQL is unreachable: %v", err)
	}

	gwMigrateOnce.Do(func() {
		migr1, err := os.ReadFile("../../migrations/001_init_schema.sql")
		if err != nil {
			migr1, _ = os.ReadFile("migrations/001_init_schema.sql")
		}
		migr2, err := os.ReadFile("../../migrations/002_api_gateway.sql")
		if err != nil {
			migr2, _ = os.ReadFile("migrations/002_api_gateway.sql")
		}
		if len(migr1) > 0 {
			_, _ = db.Exec(string(migr1))
		}
		if len(migr2) > 0 {
			_, _ = db.Exec(string(migr2))
		}
	})

	cRepo := coreRepo.NewRepository(db)
	cService := coreService.NewService(cRepo)

	gRepo := gwrepo.NewGatewayRepository(db)
	jwtSecret := "test-secret-key-32bytes-long-2026!"
	authSvc := gwservice.NewAuthService(jwtSecret, 15*time.Minute)
	gService := gwservice.NewGatewayService(gRepo, cService, authSvc, 7*24*time.Hour)

	reconciler := gwservice.NewReconciliationRunner(gRepo, cService)
	dashSvc := gwservice.NewAdminDashboardService(gRepo, cService, reconciler)
	dashHandler := gwapi.NewAdminDashboardHandler(dashSvc)

	handler := gwapi.NewGatewayHandler(gService)
	router := gwapi.NewGatewayRouter(handler, dashHandler, nil, nil, nil, authSvc, gRepo, []string{"*"})

	return db, router, authSvc, gService
}

// 1. AUTH FLOW TESTS

func TestAuth_RegistrationAndLogin(t *testing.T) {
	_, router, _, _ := setupGatewayTest(t)

	email := fmt.Sprintf("testuser_%s@example.com", uuid.New().String()[:8])
	password := "SecurePassword123!"

	// 1. Register
	regBody, _ := json.Marshal(gwdomain.RegisterRequest{Email: email, Password: password})
	req := httptest.NewRequest("POST", "/auth/register", bytes.NewBuffer(regBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("Expected status 201 Created, got %d: %s", w.Code, w.Body.String())
	}

	var userView gwdomain.UserView
	_ = json.Unmarshal(w.Body.Bytes(), &userView)
	if userView.Email != email {
		t.Errorf("Expected email %s, got %s", email, userView.Email)
	}

	// 2. Duplicate Registration Rejection
	wDup := httptest.NewRecorder()
	reqDup := httptest.NewRequest("POST", "/auth/register", bytes.NewBuffer(regBody))
	reqDup.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wDup, reqDup)
	if wDup.Code != http.StatusConflict {
		t.Errorf("Expected 409 Conflict for duplicate registration, got %d", wDup.Code)
	}

	// 3. Login
	loginBody, _ := json.Marshal(gwdomain.LoginRequest{Email: email, Password: password})
	reqLogin := httptest.NewRequest("POST", "/auth/login", bytes.NewBuffer(loginBody))
	reqLogin.Header.Set("Content-Type", "application/json")
	wLogin := httptest.NewRecorder()

	router.ServeHTTP(wLogin, reqLogin)
	if wLogin.Code != http.StatusOK {
		t.Fatalf("Expected status 200 OK for login, got %d: %s", wLogin.Code, wLogin.Body.String())
	}

	var tokenResp gwdomain.TokenResponse
	_ = json.Unmarshal(wLogin.Body.Bytes(), &tokenResp)
	if tokenResp.AccessToken == "" || tokenResp.RefreshToken == "" {
		t.Errorf("Expected access & refresh tokens in login response")
	}
}

func TestAuth_RefreshTokenRotationAndReuseRevocation(t *testing.T) {
	_, router, _, _ := setupGatewayTest(t)

	email := fmt.Sprintf("refresh_%s@example.com", uuid.New().String()[:8])
	password := "SecurePassword123!"

	// Register & Login
	regBody, _ := json.Marshal(gwdomain.RegisterRequest{Email: email, Password: password})
	reqReg := httptest.NewRequest("POST", "/auth/register", bytes.NewBuffer(regBody))
	reqReg.Header.Set("Content-Type", "application/json")
	wReg := httptest.NewRecorder()
	router.ServeHTTP(wReg, reqReg)

	loginBody, _ := json.Marshal(gwdomain.LoginRequest{Email: email, Password: password})
	reqLogin := httptest.NewRequest("POST", "/auth/login", bytes.NewBuffer(loginBody))
	reqLogin.Header.Set("Content-Type", "application/json")
	wLogin := httptest.NewRecorder()
	router.ServeHTTP(wLogin, reqLogin)

	var tokenResp gwdomain.TokenResponse
	_ = json.Unmarshal(wLogin.Body.Bytes(), &tokenResp)
	originalRefresh := tokenResp.RefreshToken

	// Refresh 1: Rotate Token
	refBody, _ := json.Marshal(gwdomain.RefreshTokenRequest{RefreshToken: originalRefresh})
	reqRef1 := httptest.NewRequest("POST", "/auth/refresh", bytes.NewBuffer(refBody))
	reqRef1.Header.Set("Content-Type", "application/json")
	wRef1 := httptest.NewRecorder()
	router.ServeHTTP(wRef1, reqRef1)

	if wRef1.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK on token refresh, got %d: %s", wRef1.Code, wRef1.Body.String())
	}

	var tokenResp2 gwdomain.TokenResponse
	_ = json.Unmarshal(wRef1.Body.Bytes(), &tokenResp2)
	newRefresh := tokenResp2.RefreshToken

	if originalRefresh == newRefresh {
		t.Errorf("Expected rotated refresh token, got identical token")
	}

	// Attempt Reuse of Original Refresh Token -> Should be rejected and trigger family revocation!
	wReuse := httptest.NewRecorder()
	reqReuse := httptest.NewRequest("POST", "/auth/refresh", bytes.NewBuffer(refBody))
	reqReuse.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wReuse, reqReuse)

	if wReuse.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401 Unauthorized for reused refresh token, got %d", wReuse.Code)
	}

	// Attempt Refresh with newRefresh -> Should also fail because token family was revoked due to detected reuse attack!
	refBody2, _ := json.Marshal(gwdomain.RefreshTokenRequest{RefreshToken: newRefresh})
	wReuse2 := httptest.NewRecorder()
	reqReuse2 := httptest.NewRequest("POST", "/auth/refresh", bytes.NewBuffer(refBody2))
	reqReuse2.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(wReuse2, reqReuse2)

	if wReuse2.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401 Unauthorized for family-revoked refresh token, got %d", wReuse2.Code)
	}
}

// 2. AUTHORIZATION TESTS

func TestAuthorization_UserACannotAccessUserBAccount(t *testing.T) {
	_, router, _, gService := setupGatewayTest(t)
	ctx := context.Background()

	// Create User A and User B
	userA, err := gService.RegisterUser(ctx, gwdomain.RegisterRequest{
		Email:    fmt.Sprintf("usera_%s@example.com", uuid.New().String()[:8]),
		Password: "Password123!",
	})
	if err != nil {
		t.Fatalf("RegisterUser A failed: %v", err)
	}

	userB, err := gService.RegisterUser(ctx, gwdomain.RegisterRequest{
		Email:    fmt.Sprintf("userb_%s@example.com", uuid.New().String()[:8]),
		Password: "Password123!",
	})
	if err != nil {
		t.Fatalf("RegisterUser B failed: %v", err)
	}

	// Create Account for User B
	accB, err := gService.CreateAccount(ctx, userB.UserID, gwdomain.CreateGatewayAccountRequest{
		Name:        "User B Savings",
		AccountType: "asset",
		Currency:    "USD",
	})
	if err != nil {
		t.Fatalf("CreateAccount B failed: %v", err)
	}

	// Login User A to get User A's Access Token
	loginA, _ := gService.LoginUser(ctx, gwdomain.LoginRequest{
		Email:    userA.Email,
		Password: "Password123!",
	})

	// User A attempts to read User B's balance
	reqBal := httptest.NewRequest("GET", fmt.Sprintf("/accounts/%s/balance", accB.AccountID), nil)
	reqBal.Header.Set("Authorization", fmt.Sprintf("Bearer %s", loginA.AccessToken))
	wBal := httptest.NewRecorder()
	router.ServeHTTP(wBal, reqBal)

	if wBal.Code != http.StatusForbidden {
		t.Errorf("SECURITY FAILURE: User A was able to access User B's balance! Expected 403 Forbidden, got %d", wBal.Code)
	}

	// User A attempts to transfer FROM User B's account
	transferPayload, _ := json.Marshal(gwdomain.TransferRequest{
		IdempotencyKey:       uuid.New().String(),
		SourceAccountID:      accB.AccountID.String(),
		DestinationAccountID: uuid.New().String(),
		AmountCents:          1000,
	})
	reqTx := httptest.NewRequest("POST", "/transfers", bytes.NewBuffer(transferPayload))
	reqTx.Header.Set("Authorization", fmt.Sprintf("Bearer %s", loginA.AccessToken))
	reqTx.Header.Set("Content-Type", "application/json")
	wTx := httptest.NewRecorder()
	router.ServeHTTP(wTx, reqTx)

	if wTx.Code != http.StatusForbidden {
		t.Errorf("SECURITY FAILURE: User A was able to transfer money from User B's account! Expected 403 Forbidden, got %d", wTx.Code)
	}
}

func TestAuthorization_NonAdminCannotAccessAdminEndpoints(t *testing.T) {
	_, router, _, gService := setupGatewayTest(t)
	ctx := context.Background()

	// Register normal user
	normalUser, _ := gService.RegisterUser(ctx, gwdomain.RegisterRequest{
		Email:    fmt.Sprintf("normal_%s@example.com", uuid.New().String()[:8]),
		Password: "Password123!",
	})

	loginResp, _ := gService.LoginUser(ctx, gwdomain.LoginRequest{
		Email:    normalUser.Email,
		Password: "Password123!",
	})

	// Attempt to access admin endpoint
	reqAdmin := httptest.NewRequest("GET", fmt.Sprintf("/admin/accounts/%s", uuid.New().String()), nil)
	reqAdmin.Header.Set("Authorization", fmt.Sprintf("Bearer %s", loginResp.AccessToken))
	wAdmin := httptest.NewRecorder()
	router.ServeHTTP(wAdmin, reqAdmin)

	if wAdmin.Code != http.StatusForbidden {
		t.Errorf("SECURITY FAILURE: Normal user accessed admin endpoint! Expected 403 Forbidden, got %d", wAdmin.Code)
	}
}

// 3. INTEGRATION TEST: Full Registration -> MFA Enrollment -> Account Setup -> Transfer -> Audit Trail

func TestIntegration_FullEndToEndFlowWithMFAAndAuditLog(t *testing.T) {
	db, router, _, gService := setupGatewayTest(t)
	ctx := context.Background()

	// 1. Register User 1 & User 2
	email1 := fmt.Sprintf("user1_e2e_%s@example.com", uuid.New().String()[:8])
	email2 := fmt.Sprintf("user2_e2e_%s@example.com", uuid.New().String()[:8])

	user1, err := gService.RegisterUser(ctx, gwdomain.RegisterRequest{Email: email1, Password: "Password123!"})
	if err != nil {
		t.Fatalf("RegisterUser 1 failed: %v", err)
	}

	user2, err := gService.RegisterUser(ctx, gwdomain.RegisterRequest{Email: email2, Password: "Password123!"})
	if err != nil {
		t.Fatalf("RegisterUser 2 failed: %v", err)
	}

	// 2. Setup MFA for User 1
	mfaSetup, err := gService.SetupMFA(ctx, user1.UserID)
	if err != nil {
		t.Fatalf("SetupMFA failed: %v", err)
	}

	// Generate valid TOTP code
	totpCode, err := totp.GenerateCode(mfaSetup.Secret, time.Now().UTC())
	if err != nil {
		t.Fatalf("GenerateCode failed: %v", err)
	}

	err = gService.VerifyMFASetup(ctx, user1.UserID, totpCode)
	if err != nil {
		t.Fatalf("VerifyMFASetup failed: %v", err)
	}

	// Login User 1 (MFA required)
	totpCodeLogin, _ := totp.GenerateCode(mfaSetup.Secret, time.Now().UTC())
	loginResp1, err := gService.LoginUser(ctx, gwdomain.LoginRequest{
		Email:    email1,
		Password: "Password123!",
		MFACode:  totpCodeLogin,
	})
	if err != nil {
		t.Fatalf("LoginUser 1 failed: %v", err)
	}

	// 3. Create Accounts
	acc1, err := gService.CreateAccount(ctx, user1.UserID, gwdomain.CreateGatewayAccountRequest{Name: "User 1 Checking", AccountType: "asset", Currency: "USD"})
	if err != nil {
		t.Fatalf("CreateAccount 1 failed: %v", err)
	}

	acc2, err := gService.CreateAccount(ctx, user2.UserID, gwdomain.CreateGatewayAccountRequest{Name: "User 2 Checking", AccountType: "liability", Currency: "USD"})
	if err != nil {
		t.Fatalf("CreateAccount 2 failed: %v", err)
	}

	// Fund User 1 Account using core ledger service direct deposit ($500.00 = 50000 cents)
	// (Simulating external funding deposit)
	fundingAcc, _ := gService.CreateAccount(ctx, user1.UserID, gwdomain.CreateGatewayAccountRequest{Name: "Bank Vault", AccountType: "equity", Currency: "USD"})
	coreSvc := coreService.NewService(coreRepo.NewRepository(db))
	_, _ = coreSvc.PostTransaction(ctx, fmt.Sprintf("fund_%s", uuid.New().String()), "Initial funding", []domain.PostEntryInput{
		{AccountID: acc1.AccountID, Direction: domain.DirectionDebit, AmountCents: 50000},
		{AccountID: fundingAcc.AccountID, Direction: domain.DirectionCredit, AmountCents: 50000},
	}, "system")

	// 4. Perform Transfer from User 1 to User 2 ($150.00 = 15000 cents) with MFA Code
	totpCodeTransfer, _ := totp.GenerateCode(mfaSetup.Secret, time.Now().UTC())
	transferPayload, _ := json.Marshal(gwdomain.TransferRequest{
		IdempotencyKey:       fmt.Sprintf("e2e_tx_%s", uuid.New().String()),
		SourceAccountID:      acc1.AccountID.String(),
		DestinationAccountID: acc2.AccountID.String(),
		AmountCents:          15000,
		Description:          "E2E Transfer to User 2",
	})

	reqTx := httptest.NewRequest("POST", "/transfers", bytes.NewBuffer(transferPayload))
	reqTx.Header.Set("Authorization", fmt.Sprintf("Bearer %s", loginResp1.AccessToken))
	reqTx.Header.Set("X-MFA-Code", totpCodeTransfer)
	reqTx.Header.Set("Content-Type", "application/json")
	wTx := httptest.NewRecorder()

	router.ServeHTTP(wTx, reqTx)
	if wTx.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK on transfer, got %d: %s", wTx.Code, wTx.Body.String())
	}

	// 5. Verify Balances
	bal1, _ := gService.GetAccountBalance(ctx, user1.UserID, gwdomain.RoleUser, acc1.AccountID)
	if bal1.ComputedBalanceCents != 35000 { // 50000 - 15000 = 35000
		t.Errorf("Expected User 1 balance 35000 cents, got %d", bal1.ComputedBalanceCents)
	}

	// Wait 100ms for async audit log insertion
	time.Sleep(100 * time.Millisecond)

	// 6. Verify API Audit Log Record
	var auditCount int
	err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM api_audit_logs WHERE endpoint = '/transfers' AND outcome = 'SUCCESS'").Scan(&auditCount)
	if err != nil {
		t.Fatalf("Query api_audit_logs failed: %v", err)
	}
	if auditCount == 0 {
		t.Errorf("Expected API audit log entry for /transfers, got 0")
	}
}
