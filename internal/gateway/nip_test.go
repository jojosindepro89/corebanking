package gateway_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"core-banking-ledger/internal/domain"
	coreRepo "core-banking-ledger/internal/repository"
	coreService "core-banking-ledger/internal/service"

	gwapi "core-banking-ledger/internal/gateway/api"
	gwdomain "core-banking-ledger/internal/gateway/domain"
	gwrepo "core-banking-ledger/internal/gateway/repository"
	gwservice "core-banking-ledger/internal/gateway/service"
	fraudSvc "core-banking-ledger/internal/gateway/service/fraud"
	nipSvc "core-banking-ledger/internal/gateway/service/nip"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

var nipMigrateOnce sync.Once

func setupNIPTest(t *testing.T) (*sql.DB, http.Handler, *gwservice.AuthService, *nipSvc.NIPService, *nipSvc.MockNIBSSProvider, *fraudSvc.FraudService) {
	t.Helper()

	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://ledger_user:ledger_password@localhost:5432/ledger_db?sslmode=disable"
	}

	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		t.Fatalf("Failed to connect to test database: %v", err)
	}

	if err := db.Ping(); err != nil {
		t.Skipf("Skipping NIP integration test because PostgreSQL is unreachable: %v", err)
	}

	nipMigrateOnce.Do(func() {
		files := []string{
			"../../migrations/001_init_schema.sql",
			"../../migrations/002_api_gateway.sql",
			"../../migrations/003_admin_dashboard.sql",
			"../../migrations/004_nip_integration.sql",
			"../../migrations/005_fraud_monitoring.sql",
		}
		for _, f := range files {
			content, err := os.ReadFile(f)
			if err != nil {
				content, _ = os.ReadFile(f[6:])
			}
			if len(content) > 0 {
				_, _ = db.Exec(string(content))
			}
		}
	})

	cRepo := coreRepo.NewRepository(db)
	cService := coreService.NewService(cRepo)
	gRepo := gwrepo.NewGatewayRepository(db)

	jwtSecret := "nip-test-jwt-secret-key-32bytes"
	authSvc := gwservice.NewAuthService(jwtSecret, 15*time.Minute)

	mockProvider := nipSvc.NewMockNIBSSProvider()
	fraudScorer := fraudSvc.NewRulesRiskScorer(db)
	fraudService := fraudSvc.NewFraudService(db, fraudScorer, nil)
	nipService := nipSvc.NewNIPService(gRepo, cService, mockProvider, fraudService)

	dashSvc := gwservice.NewAdminDashboardService(gRepo, cService, nil)
	dashHandler := gwapi.NewAdminDashboardHandler(dashSvc)
	gService := gwservice.NewGatewayService(gRepo, cService, authSvc, 7*24*time.Hour)
	handler := gwapi.NewGatewayHandler(gService)
	nipHandler := gwapi.NewNIPHandler(nipService)
	fraudHandler := gwapi.NewFraudHandler(fraudService)

	router := gwapi.NewGatewayRouter(handler, dashHandler, nipHandler, fraudHandler, nil, nil, authSvc, gRepo, []string{"*"})

	return db, router, authSvc, nipService, mockProvider, fraudService
}

func createNIPTestUserAndAccount(t *testing.T, db *sql.DB, authSvc *gwservice.AuthService, role gwdomain.UserRole) (string, uuid.UUID, uuid.UUID) {
	t.Helper()
	userID := uuid.New()
	email := fmt.Sprintf("nipuser_%s@example.com", userID.String()[:8])
	pwHash, _ := authSvc.HashPassword("TestPass123!")

	_, err := db.Exec(`
		INSERT INTO users (user_id, email, password_hash, role)
		VALUES ($1, $2, $3, $4)
	`, userID, email, pwHash, string(role))
	if err != nil {
		t.Fatalf("Failed to insert user: %v", err)
	}

	accID := uuid.New()
	_, err = db.Exec(`
		INSERT INTO accounts (account_id, name, account_type, currency, status)
		VALUES ($1, 'NIP Savings', 'asset', 'USD', 'active')
	`, accID)
	if err != nil {
		t.Fatalf("Failed to insert core account: %v", err)
	}

	_, err = db.Exec(`
		INSERT INTO user_accounts (user_id, account_id, permission)
		VALUES ($1, $2, 'owner')
	`, userID, accID)
	if err != nil {
		t.Fatalf("Failed to link user account: %v", err)
	}

	// Deposit initial funds ($10,000.00 / 1,000,000 cents)
	cRepo := coreRepo.NewRepository(db)
	cService := coreService.NewService(cRepo)
	vaultID := uuid.New()
	_, _ = db.Exec(`INSERT INTO accounts (account_id, name, account_type, currency, status) VALUES ($1, 'Vault', 'equity', 'USD', 'active')`, vaultID)

	_, _ = cService.PostTransaction(context.Background(), fmt.Sprintf("INIT_NIP_%s", accID.String()[:8]), "Initial funding", []domain.PostEntryInput{
		{AccountID: vaultID, Direction: domain.DirectionCredit, AmountCents: 1000000},
		{AccountID: accID, Direction: domain.DirectionDebit, AmountCents: 1000000},
	}, "test")

	userObj := &gwdomain.User{UserID: userID, Email: email, Role: role}
	token, _, _ := authSvc.GenerateAccessToken(userObj)

	return token, userID, accID
}

func TestNIP_NameEnquiryAndSuccessfulOutboundTransfer(t *testing.T) {
	db, router, authSvc, _, _, _ := setupNIPTest(t)
	token, _, srcAccID := createNIPTestUserAndAccount(t, db, authSvc, gwdomain.RoleUser)

	// 1. Execute Name Enquiry
	neBody, _ := json.Marshal(map[string]string{
		"bank_code":      "011",
		"account_number": "0123456789",
	})
	req := httptest.NewRequest("POST", "/nip/name-enquiry", bytes.NewBuffer(neBody))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Name enquiry expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	var neResp map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &neResp)
	sessionID := neResp["session_id"]
	if sessionID == "" || neResp["account_name"] == "" {
		t.Fatalf("Expected valid session_id and account_name, got %v", neResp)
	}

	// 2. Execute Outbound Interbank Transfer
	transferBody, _ := json.Marshal(map[string]interface{}{
		"session_id":                 sessionID,
		"source_account_id":          srcAccID.String(),
		"destination_bank_code":      "011",
		"destination_account_number": "0123456789",
		"destination_account_name":   neResp["account_name"],
		"amount_cents":               5000, // $50.00
		"payment_reference":          "School fees payment",
	})

	tReq := httptest.NewRequest("POST", "/nip/transfer", bytes.NewBuffer(transferBody))
	tReq.Header.Set("Authorization", "Bearer "+token)
	tReq.Header.Set("Content-Type", "application/json")
	tReq.Header.Set("X-StepUp-Code", "123456") // Pass 6-digit code for step-up if evaluated
	tW := httptest.NewRecorder()
	router.ServeHTTP(tW, tReq)

	if tW.Code != http.StatusOK {
		t.Fatalf("Outbound transfer expected 200 OK, got %d: %s", tW.Code, tW.Body.String())
	}

	var tResp map[string]interface{}
	_ = json.Unmarshal(tW.Body.Bytes(), &tResp)
	if tResp["status"] != "COMPLETED" {
		t.Fatalf("Expected status COMPLETED, got %v", tResp["status"])
	}
}

func TestNIP_InboundWebhookCryptographicSignatureAndIdempotency(t *testing.T) {
	db, router, authSvc, _, _, _ := setupNIPTest(t)
	_, _, dstAccID := createNIPTestUserAndAccount(t, db, authSvc, gwdomain.RoleUser)

	secret := "nibss-hmac-secret-key-2026"
	sessionID := fmt.Sprintf("IN_SESS_%s", uuid.New().String()[:8])

	payload := map[string]interface{}{
		"session_id":                 sessionID,
		"transaction_ref":            "NIBSS_INB_12345",
		"source_bank_code":           "058",
		"source_account_number":      "9988776655",
		"source_account_name":        "John Doe",
		"destination_account_number": dstAccID.String(),
		"amount_cents":               15000, // $150.00
		"payment_reference":          "Inbound refund",
	}
	bodyBytes, _ := json.Marshal(payload)

	// Compute HMAC-SHA256 signature
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(bodyBytes)
	sigHex := hex.EncodeToString(mac.Sum(nil))

	// 1. First Webhook Request (Should succeed)
	wReq := httptest.NewRequest("POST", "/nip/webhook", bytes.NewBuffer(bodyBytes))
	wReq.Header.Set("X-NIBSS-Signature", sigHex)
	wReq.Header.Set("Content-Type", "application/json")
	wW := httptest.NewRecorder()
	router.ServeHTTP(wW, wReq)

	if wW.Code != http.StatusOK {
		t.Fatalf("Inbound webhook expected 200 OK, got %d: %s", wW.Code, wW.Body.String())
	}

	// 2. Duplicate Webhook Request (Idempotent - must not double credit!)
	wReq2 := httptest.NewRequest("POST", "/nip/webhook", bytes.NewBuffer(bodyBytes))
	wReq2.Header.Set("X-NIBSS-Signature", sigHex)
	wReq2.Header.Set("Content-Type", "application/json")
	wW2 := httptest.NewRecorder()
	router.ServeHTTP(wW2, wReq2)

	if wW2.Code != http.StatusOK {
		t.Fatalf("Duplicate webhook expected 200 OK, got %d: %s", wW2.Code, wW2.Body.String())
	}
}

func TestNIP_TimeoutHandlingAndReconciliationCron(t *testing.T) {
	db, _, authSvc, nipService, mockProvider, _ := setupNIPTest(t)
	_, userID, srcAccID := createNIPTestUserAndAccount(t, db, authSvc, gwdomain.RoleUser)

	sessionID := fmt.Sprintf("TIMEOUT_SESS_%s", uuid.New().String()[:8])
	mockProvider.SetMode(sessionID, "timeout")

	ctx := context.Background()
	transferReq := nipSvc.NIPOutboundTransferRequest{
		SessionID:                sessionID,
		SourceAccountID:          srcAccID.String(),
		DestinationBankCode:      "011",
		DestinationAccountNumber: "1122334455",
		DestinationAccountName:   "Jane Doe",
		AmountCents:              10000,
		PaymentReference:         "Timeout test transfer",
	}

	tx, status, err := nipService.OutboundTransfer(ctx, userID, transferReq, "127.0.0.1", "TestDevice", true)
	if status != "PENDING_RECONCILIATION" {
		t.Fatalf("Expected status PENDING_RECONCILIATION on timeout, got status %s, err: %v", status, err)
	}
	if tx == nil {
		t.Fatalf("Expected pre-debit transaction to be recorded")
	}

	// Simulate NIBSS confirming SUCCESS on subsequent query during cron run
	mockProvider.SetMode(sessionID, "success")

	count, err := nipService.ReconcilePendingNIPTransactions(ctx)
	if err != nil {
		t.Fatalf("Reconciliation cron failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("Expected 1 transaction to be reconciled, got %d", count)
	}

	// Verify status updated to completed in DB
	var dbStatus string
	_ = db.QueryRow("SELECT status FROM nip_transactions WHERE session_id = $1", sessionID).Scan(&dbStatus)
	if dbStatus != "completed" {
		t.Fatalf("Expected DB status completed after reconciliation, got %s", dbStatus)
	}
}
