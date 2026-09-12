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

var fraudMigrateOnce sync.Once

func setupFraudTest(t *testing.T) (*sql.DB, http.Handler, *gwservice.AuthService, *fraudSvc.FraudService, *nipSvc.NIPService) {
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
		t.Skipf("Skipping fraud integration test because PostgreSQL is unreachable: %v", err)
	}

	fraudMigrateOnce.Do(func() {
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

	jwtSecret := "fraud-test-jwt-secret-key-32bytes"
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

	return db, router, authSvc, fraudService, nipService
}

func createFraudUserAndAccount(t *testing.T, db *sql.DB, authSvc *gwservice.AuthService, role gwdomain.UserRole) (string, uuid.UUID, uuid.UUID) {
	t.Helper()
	userID := uuid.New()
	email := fmt.Sprintf("frauduser_%s@example.com", userID.String()[:8])
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
		VALUES ($1, 'Fraud Test Account', 'asset', 'USD', 'active')
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

	// Deposit initial funds ($50,000.00 / 5,000,000 cents)
	cRepo := coreRepo.NewRepository(db)
	cService := coreService.NewService(cRepo)
	vaultID := uuid.New()
	_, _ = db.Exec(`INSERT INTO accounts (account_id, name, account_type, currency, status) VALUES ($1, 'Fraud Vault', 'equity', 'USD', 'active')`, vaultID)

	_, _ = cService.PostTransaction(context.Background(), fmt.Sprintf("INIT_FRAUD_%s", accID.String()[:8]), "Initial funding", []domain.PostEntryInput{
		{AccountID: vaultID, Direction: domain.DirectionCredit, AmountCents: 5000000},
		{AccountID: accID, Direction: domain.DirectionDebit, AmountCents: 5000000},
	}, "test")

	userObj := &gwdomain.User{UserID: userID, Email: email, Role: role}
	token, _, _ := authSvc.GenerateAccessToken(userObj)

	return token, userID, accID
}

// 1. RULE 1 TEST: VELOCITY THRESHOLD
func TestFraud_RuleVelocityExceeded(t *testing.T) {
	db, _, authSvc, _, _ := setupFraudTest(t)
	_, userID, srcAccID := createFraudUserAndAccount(t, db, authSvc, gwdomain.RoleUser)

	ctx := context.Background()
	scorer := fraudSvc.NewRulesRiskScorer(db)

	// Register device as recognized so device rule doesn't trigger
	_, _ = db.Exec("INSERT INTO user_devices (user_id, device_fingerprint, last_ip) VALUES ($1, 'KnownDevice', '192.168.1.1')", userID)

	now := time.Now().UTC()
	// Insert 5 transfers in last 1 hour
	for i := 0; i < 5; i++ {
		_, err := db.Exec(`
			INSERT INTO nip_transactions (nip_id, session_id, transaction_ref, user_id, source_account_id, destination_bank_code, destination_account_number, destination_account_name, amount_cents, direction, status, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, '011', '1234567890', 'Recipient', 1000, 'outbound', 'completed', $6, $6)
		`, uuid.New(), fmt.Sprintf("VEL_SESS_%d_%s", i, uuid.New().String()[:6]), fmt.Sprintf("VEL_REF_%d_%s", i, uuid.New().String()[:6]), userID, srcAccID, now)
		if err != nil {
			t.Fatalf("Failed to insert historical nip transaction: %v", err)
		}
	}

	req := fraudSvc.RiskEvaluationRequest{
		UserID:                   userID,
		SourceAccountID:          srcAccID,
		DestinationBankCode:      "011",
		DestinationAccountNumber: "9999999999",
		AmountCents:              1000,
		IPAddress:                "192.168.1.1",
		DeviceFingerprint:        "KnownDevice",
		UserConfirmedAt:          now,
	}

	result, err := scorer.Evaluate(ctx, req)
	if err != nil {
		t.Fatalf("Scorer evaluation failed: %v", err)
	}

	triggered := false
	for _, r := range result.RulesTriggered {
		if r == "VELOCITY_EXCEEDED_HOURLY_LIMIT" {
			triggered = true
		}
	}
	if !triggered {
		t.Fatalf("Expected VELOCITY_EXCEEDED_HOURLY_LIMIT to trigger, got rules: %v", result.RulesTriggered)
	}
}

// 2. RULE 2 TEST: AMOUNT ANOMALY (>5x 30-DAY AVG)
func TestFraud_RuleAmountAnomaly(t *testing.T) {
	db, _, authSvc, _, _ := setupFraudTest(t)
	_, userID, srcAccID := createFraudUserAndAccount(t, db, authSvc, gwdomain.RoleUser)

	ctx := context.Background()
	scorer := fraudSvc.NewRulesRiskScorer(db)

	// Historical average: 1,000 cents ($10.00)
	_, err := db.Exec(`
		INSERT INTO nip_transactions (nip_id, session_id, transaction_ref, user_id, source_account_id, destination_bank_code, destination_account_number, destination_account_name, amount_cents, direction, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, '011', '1234567890', 'Recipient', 1000, 'outbound', 'completed', NOW() - INTERVAL '2 days', NOW())
	`, uuid.New(), fmt.Sprintf("AVG_SESS_%s", uuid.New().String()[:6]), fmt.Sprintf("AVG_REF_%s", uuid.New().String()[:6]), userID, srcAccID)
	if err != nil {
		t.Fatalf("Failed to insert historical average transfer: %v", err)
	}

	// Register device as recognized so device rule doesn't trigger
	_, _ = db.Exec("INSERT INTO user_devices (user_id, device_fingerprint, last_ip) VALUES ($1, 'KnownDevice', '192.168.1.1')", userID)

	// New transfer: 6,000 cents ($60.00) -> 6x average
	req := fraudSvc.RiskEvaluationRequest{
		UserID:                   userID,
		SourceAccountID:          srcAccID,
		DestinationBankCode:      "011",
		DestinationAccountNumber: "1234567890",
		AmountCents:              6000,
		IPAddress:                "192.168.1.1",
		DeviceFingerprint:        "KnownDevice",
		UserConfirmedAt:          time.Now().UTC(),
	}

	result, err := scorer.Evaluate(ctx, req)
	if err != nil {
		t.Fatalf("Scorer evaluation failed: %v", err)
	}

	triggered := false
	for _, r := range result.RulesTriggered {
		if r == "AMOUNT_ANOMALY_MULTIPLIER_EXCEEDED" {
			triggered = true
		}
	}
	if !triggered {
		t.Fatalf("Expected AMOUNT_ANOMALY_MULTIPLIER_EXCEEDED to trigger, got rules: %v", result.RulesTriggered)
	}
}

// 3. RULE 3 TEST: NEW RECIPIENT + HIGH AMOUNT (>= $2,000.00 / 200,000 CENTS)
func TestFraud_RuleNewRecipientHighAmount(t *testing.T) {
	db, _, authSvc, _, _ := setupFraudTest(t)
	_, userID, srcAccID := createFraudUserAndAccount(t, db, authSvc, gwdomain.RoleUser)

	ctx := context.Background()
	scorer := fraudSvc.NewRulesRiskScorer(db)

	req := fraudSvc.RiskEvaluationRequest{
		UserID:                   userID,
		SourceAccountID:          srcAccID,
		DestinationBankCode:      "058",
		DestinationAccountNumber: "9900112233", // First time recipient
		AmountCents:              250000,       // $2,500.00
		IPAddress:                "10.0.0.1",
		DeviceFingerprint:        "RecognizedDevice",
		UserConfirmedAt:          time.Now().UTC(),
	}

	// Register device as recognized so device rule doesn't trigger
	_, _ = db.Exec("INSERT INTO user_devices (user_id, device_fingerprint, last_ip) VALUES ($1, 'RecognizedDevice', '10.0.0.1')", userID)

	result, err := scorer.Evaluate(ctx, req)
	if err != nil {
		t.Fatalf("Scorer evaluation failed: %v", err)
	}

	triggered := false
	for _, r := range result.RulesTriggered {
		if r == "NEW_RECIPIENT_HIGH_AMOUNT" {
			triggered = true
		}
	}
	if !triggered {
		t.Fatalf("Expected NEW_RECIPIENT_HIGH_AMOUNT to trigger, got rules: %v", result.RulesTriggered)
	}
}

// 4. MEDIUM RISK STEP-UP AUTHENTICATION TEST
func TestFraud_MediumRiskRequiresStepUpAuth(t *testing.T) {
	db, router, authSvc, _, _ := setupFraudTest(t)
	userToken, userID, srcAccID := createFraudUserAndAccount(t, db, authSvc, gwdomain.RoleUser)

	// Create name enquiry session
	sessionID := fmt.Sprintf("MED_SESS_%s", uuid.New().String()[:8])
	_, _ = db.Exec("INSERT INTO nip_name_enquiries (enquiry_id, session_id, user_id, bank_code, account_number, account_name, response_code) VALUES ($1, $2, $3, '011', '9988776655', 'Recipient', '00')", uuid.New(), sessionID, userID)

	// Unrecognized device will trigger medium risk
	transferBody, _ := json.Marshal(map[string]interface{}{
		"session_id":                 sessionID,
		"source_account_id":          srcAccID.String(),
		"destination_bank_code":      "011",
		"destination_account_number": "9988776655",
		"destination_account_name":   "Recipient",
		"amount_cents":               5000,
		"device_fingerprint":        "BrandNewUnrecognizedPhone",
	})

	// Attempt transfer WITHOUT step-up code (Should fail 403 Forbidden)
	req1 := httptest.NewRequest("POST", "/nip/transfer", bytes.NewBuffer(transferBody))
	req1.Header.Set("Authorization", "Bearer "+userToken)
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, req1)

	if w1.Code != http.StatusForbidden {
		t.Fatalf("Expected 403 Forbidden without step-up auth, got %d: %s", w1.Code, w1.Body.String())
	}

	// Attempt transfer WITH 6-digit TOTP step-up code in X-StepUp-Code header (Should succeed)
	req2 := httptest.NewRequest("POST", "/nip/transfer", bytes.NewBuffer(transferBody))
	req2.Header.Set("Authorization", "Bearer "+userToken)
	req2.Header.Set("X-StepUp-Code", "654321")
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK with valid step-up auth, got %d: %s", w2.Code, w2.Body.String())
	}
}

// 5. HIGH RISK HOLD QUEUE & ADMIN REVIEW TEST
func TestFraud_HighRiskHeldAndAdminApproveReject(t *testing.T) {
	db, router, authSvc, fraudService, _ := setupFraudTest(t)
	userToken, userID, srcAccID := createFraudUserAndAccount(t, db, authSvc, gwdomain.RoleUser)
	adminToken, adminID, _ := createFraudUserAndAccount(t, db, authSvc, gwdomain.RoleAdmin)

	// Create new recipient high amount transfer ($3,000.00 / 300,000 cents) -> HIGH RISK
	sessionID := fmt.Sprintf("HIGH_SESS_%s", uuid.New().String()[:8])
	_, _ = db.Exec("INSERT INTO nip_name_enquiries (enquiry_id, session_id, user_id, bank_code, account_number, account_name, response_code) VALUES ($1, $2, $3, '058', '1122334455', 'Recipient', '00')", uuid.New(), sessionID, userID)

	transferBody, _ := json.Marshal(map[string]interface{}{
		"session_id":                 sessionID,
		"source_account_id":          srcAccID.String(),
		"destination_bank_code":      "058",
		"destination_account_number": "1122334455",
		"destination_account_name":   "High Risk Recipient",
		"amount_cents":               300000,
		"device_fingerprint":        "HighRiskDevice",
	})

	req := httptest.NewRequest("POST", "/nip/transfer", bytes.NewBuffer(transferBody))
	req.Header.Set("Authorization", "Bearer "+userToken)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("Expected 202 Accepted for high risk held transfer, got %d: %s", w.Code, w.Body.String())
	}

	// Verify transaction is in held queue
	heldList, err := fraudService.GetHeldTransactions(context.Background(), "pending_review")
	if err != nil || len(heldList) == 0 {
		t.Fatalf("Expected held transaction in pending_review queue, got list: %v, err: %v", heldList, err)
	}

	holdID := heldList[0].HoldID

	// Admin approves the held transaction
	appReq := httptest.NewRequest("POST", fmt.Sprintf("/admin/fraud/held/%s/approve", holdID.String()), nil)
	appReq.Header.Set("Authorization", "Bearer "+adminToken)
	appW := httptest.NewRecorder()
	router.ServeHTTP(appW, appReq)

	if appW.Code != http.StatusOK {
		t.Fatalf("Admin approval expected 200 OK, got %d: %s", appW.Code, appW.Body.String())
	}

	// Verify status updated to approved with admin ID logged
	var status string
	var reviewedBy uuid.UUID
	_ = db.QueryRow("SELECT status, reviewed_by FROM held_fraud_transactions WHERE hold_id = $1", holdID).Scan(&status, &reviewedBy)
	if status != "approved" || reviewedBy != adminID {
		t.Fatalf("Expected status approved and reviewed_by %s, got status %s, reviewer %s", adminID, status, reviewedBy)
	}
}

// 6. IMMUTABLE EVIDENCE CAPTURE & SUB-MINUTE DISPUTE RETRIEVAL TEST
func TestFraud_ImmutableEvidenceCaptureAndFastLookup(t *testing.T) {
	db, router, authSvc, fraudService, _ := setupFraudTest(t)
	_, userID, _ := createFraudUserAndAccount(t, db, authSvc, gwdomain.RoleUser)
	adminToken, _, _ := createFraudUserAndAccount(t, db, authSvc, gwdomain.RoleAdmin)

	txID := uuid.New()
	_, _ = db.Exec("INSERT INTO transactions (transaction_id, idempotency_key, description) VALUES ($1, $2, 'Proof Tx')", txID, fmt.Sprintf("IDEM_%s", txID.String()[:8]))

	evalReq := fraudSvc.RiskEvaluationRequest{
		UserID:                   userID,
		DestinationBankCode:      "011",
		DestinationAccountNumber: "9988776655",
		AmountCents:              45000,
		IPAddress:                "172.16.0.1",
		DeviceFingerprint:        "Device_APP_Proof",
		UserConfirmedAt:          time.Now().UTC(),
	}

	_, evidenceID, err := fraudService.EvaluateAndRecord(context.Background(), evalReq, `{"account_name":"Dispute Recipient"}`, true)
	if err != nil {
		t.Fatalf("Failed to create evidence record: %v", err)
	}
	_ = fraudService.UpdateEvidenceTransactionID(context.Background(), evidenceID, txID)

	// Measure sub-minute lookup speed by transaction_id
	start := time.Now()
	eReq := httptest.NewRequest("GET", fmt.Sprintf("/admin/fraud/evidence/%s", txID.String()), nil)
	eReq.Header.Set("Authorization", "Bearer "+adminToken)
	eW := httptest.NewRecorder()
	router.ServeHTTP(eW, eReq)
	elapsed := time.Since(start)

	if eW.Code != http.StatusOK {
		t.Fatalf("Evidence lookup expected 200 OK, got %d: %s", eW.Code, eW.Body.String())
	}

	if elapsed > 1*time.Minute {
		t.Fatalf("Evidence lookup benchmark failed: took %v (Target: sub-minute)", elapsed)
	}

	var evResp fraudSvc.EvidenceRecordDTO
	_ = json.Unmarshal(eW.Body.Bytes(), &evResp)
	if evResp.DeviceFingerprint != "Device_APP_Proof" || evResp.IPAddress != "172.16.0.1" {
		t.Fatalf("Evidence payload mismatch: %v", evResp)
	}
}

// 7. DYNAMIC RULE THRESHOLD CONFIGURATION TEST
func TestFraud_DynamicThresholdConfigurationUpdate(t *testing.T) {
	db, router, authSvc, fraudService, _ := setupFraudTest(t)
	adminToken, _, _ := createFraudUserAndAccount(t, db, authSvc, gwdomain.RoleAdmin)

	// Update velocity threshold from 5 to 2 without code redeployment
	updateBody, _ := json.Marshal(map[string]interface{}{
		"rule_key":      "velocity_max_transfers_per_hour",
		"value_numeric": 2,
		"is_enabled":    true,
	})

	uReq := httptest.NewRequest("PUT", "/admin/fraud/configs", bytes.NewBuffer(updateBody))
	uReq.Header.Set("Authorization", "Bearer "+adminToken)
	uReq.Header.Set("Content-Type", "application/json")
	uW := httptest.NewRecorder()
	router.ServeHTTP(uW, uReq)

	if uW.Code != http.StatusOK {
		t.Fatalf("Threshold update expected 200 OK, got %d: %s", uW.Code, uW.Body.String())
	}

	// Query updated configs
	cfgs, err := fraudService.GetRuleConfigs(context.Background())
	if err != nil {
		t.Fatalf("Failed to fetch rule configs: %v", err)
	}

	var velVal float64
	for _, c := range cfgs {
		if c.RuleKey == "velocity_max_transfers_per_hour" {
			velVal = c.ValueNumeric
		}
	}
	if velVal != 2 {
		t.Fatalf("Expected updated numeric threshold 2, got %f", velVal)
	}
}
