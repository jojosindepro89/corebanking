package nip

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"core-banking-ledger/internal/domain"
	gwrepo "core-banking-ledger/internal/gateway/repository"
	"core-banking-ledger/internal/gateway/service/fraud"
	coreService "core-banking-ledger/internal/service"

	"github.com/google/uuid"
)

type NIPService struct {
	repo          *gwrepo.GatewayRepository
	coreSvc       *coreService.Service
	provider      NIBSSProvider
	fraudSvc      *fraud.FraudService
	webhookSecret string
}

func NewNIPService(repo *gwrepo.GatewayRepository, coreSvc *coreService.Service, provider NIBSSProvider, fraudSvc *fraud.FraudService) *NIPService {
	secret := os.Getenv("NIBSS_WEBHOOK_SECRET")
	if secret == "" {
		secret = "nibss-hmac-secret-key-2026"
	}
	return &NIPService{
		repo:          repo,
		coreSvc:       coreSvc,
		provider:      provider,
		fraudSvc:      fraudSvc,
		webhookSecret: secret,
	}
}

type NIPOutboundTransferRequest struct {
	SessionID                string `json:"session_id"`
	SourceAccountID          string `json:"source_account_id"`
	DestinationBankCode      string `json:"destination_bank_code"`
	DestinationAccountNumber string `json:"destination_account_number"`
	DestinationAccountName   string `json:"destination_account_name"`
	AmountCents              int64  `json:"amount_cents"`
	PaymentReference         string `json:"payment_reference"`
	StepUpCode               string `json:"step_up_code,omitempty"`
	DeviceFingerprint        string `json:"device_fingerprint,omitempty"`
}

type NIPInboundWebhookPayload struct {
	SessionID                string `json:"session_id"`
	TransactionRef           string `json:"transaction_ref"`
	SourceBankCode           string `json:"source_bank_code"`
	SourceAccountNumber      string `json:"source_account_number"`
	SourceAccountName        string `json:"source_account_name"`
	DestinationAccountNumber string `json:"destination_account_number"`
	AmountCents              int64  `json:"amount_cents"`
	PaymentReference         string `json:"payment_reference"`
}

// 1. NAME ENQUIRY

func (s *NIPService) NameEnquiry(ctx context.Context, userID uuid.UUID, bankCode, accountNumber string) (string, string, error) {
	accName, sessionID, respCode, err := s.provider.NameEnquiry(ctx, bankCode, accountNumber)
	if err != nil {
		return "", "", err
	}
	if respCode != "00" {
		return "", "", fmt.Errorf("NIBSS Name Enquiry failed with response code %s", respCode)
	}

	// Record enquiry result
	query := `
		INSERT INTO nip_name_enquiries (enquiry_id, session_id, user_id, bank_code, account_number, account_name, response_code, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (session_id) DO NOTHING
	`
	_, _ = s.repo.DB().ExecContext(ctx, query, uuid.New(), sessionID, userID, bankCode, accountNumber, accName, respCode, time.Now().UTC())

	return accName, sessionID, nil
}

// 2. OUTBOUND INTERBANK TRANSFER

func (s *NIPService) OutboundTransfer(ctx context.Context, userID uuid.UUID, req NIPOutboundTransferRequest, ipAddress, deviceInfo string, isStepUpValidated bool) (*domain.Transaction, string, error) {
	if req.SessionID == "" {
		return nil, "", fmt.Errorf("session_id is required from name enquiry")
	}
	if req.AmountCents <= 0 {
		return nil, "", fmt.Errorf("invalid amount")
	}

	srcAccID, err := uuid.Parse(req.SourceAccountID)
	if err != nil {
		return nil, "", fmt.Errorf("invalid source_account_id")
	}

	// Verify account ownership
	owned, err := s.repo.VerifyAccountOwnership(ctx, s.repo.DB(), userID, srcAccID)
	if err != nil || !owned {
		return nil, "", fmt.Errorf("source account does not belong to authenticated user")
	}

	// ---------------------------------------------------------------------
	// FRAUD DETECTION & RISK SCORING EVALUATION
	// ---------------------------------------------------------------------
	if req.DeviceFingerprint == "" {
		req.DeviceFingerprint = deviceInfo
	}

	evalReq := fraud.RiskEvaluationRequest{
		UserID:                   userID,
		SourceAccountID:          srcAccID,
		DestinationBankCode:      req.DestinationBankCode,
		DestinationAccountNumber: req.DestinationAccountNumber,
		AmountCents:              req.AmountCents,
		IPAddress:                ipAddress,
		DeviceFingerprint:        req.DeviceFingerprint,
		UserConfirmedAt:          time.Now().UTC(),
	}

	// Fetch Name Enquiry JSON result if present
	var nameEnquiryJSON string
	_ = s.repo.DB().QueryRowContext(ctx, "SELECT json_build_object('account_name', account_name, 'bank_code', bank_code, 'account_number', account_number)::text FROM nip_name_enquiries WHERE session_id = $1", req.SessionID).Scan(&nameEnquiryJSON)

	riskResult, evidenceID, err := s.fraudSvc.EvaluateAndRecord(ctx, evalReq, nameEnquiryJSON, isStepUpValidated)
	if err != nil {
		return nil, "", fmt.Errorf("fraud risk evaluation error: %w", err)
	}

	// Check Medium Risk Step-Up requirement
	if riskResult.StepUpRequired && !isStepUpValidated {
		return nil, "STEP_UP_AUTH_REQUIRED", fmt.Errorf("medium risk transaction detected: step-up authentication (TOTP 6-digit code) required in X-StepUp-Code header")
	}

	// Check High Risk Hold requirement
	if riskResult.Level == fraud.RiskHigh || riskResult.HoldForReview {
		return nil, "HELD_FOR_REVIEW", fmt.Errorf("high risk transaction detected: transaction held in compliance queue for manual review (Evidence ID: %s)", evidenceID.String())
	}

	// ---------------------------------------------------------------------
	// LEDGER PRE-DEBIT & INTERBANK CLEARING
	// ---------------------------------------------------------------------

	// Get or Create Settlement Account for Outbound NIP Clearing
	settlementAccID, err := s.getOrCreateSettlementAccount(ctx, "NIP Outbound Settlement", "USD")
	if err != nil {
		return nil, "", fmt.Errorf("failed to obtain NIP settlement account: %w", err)
	}

	txRef := fmt.Sprintf("NIP_OUT_%s_%s", req.SessionID, uuid.New().String()[:8])
	now := time.Now().UTC()

	// Step A: Pre-debit sender in Core Ledger FIRST (Debit Sender Asset, Credit Settlement)
	entries := []domain.PostEntryInput{
		{AccountID: srcAccID, Direction: domain.DirectionCredit, AmountCents: req.AmountCents},
		{AccountID: settlementAccID, Direction: domain.DirectionDebit, AmountCents: req.AmountCents},
	}

	ledgerTx, err := s.coreSvc.PostTransaction(ctx, txRef, fmt.Sprintf("Outbound NIP Transfer to %s (%s)", req.DestinationAccountName, req.DestinationBankCode), entries, userID.String())
	if err != nil {
		return nil, "", fmt.Errorf("pre-debit ledger transaction failed: %w", err)
	}

	// Attach transaction_id to evidence record for sub-minute audit retrieval
	_ = s.fraudSvc.UpdateEvidenceTransactionID(ctx, evidenceID, ledgerTx.TransactionID)

	// Step B: Record NIP transaction record
	nipID := uuid.New()
	insertNIPQuery := `
		INSERT INTO nip_transactions (nip_id, session_id, transaction_ref, user_id, source_account_id, destination_bank_code, destination_account_number, destination_account_name, amount_cents, direction, status, ledger_tx_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'outbound', 'pending_nibss', $10, $11, $11)
	`
	_, err = s.repo.DB().ExecContext(ctx, insertNIPQuery, nipID, req.SessionID, txRef, userID, srcAccID, req.DestinationBankCode, req.DestinationAccountNumber, req.DestinationAccountName, req.AmountCents, ledgerTx.TransactionID, now)
	if err != nil {
		return nil, "", fmt.Errorf("failed to record NIP transaction: %w", err)
	}

	// Step C: Record APP Fraud Audit Trail
	s.logNIPAuditTrail(ctx, req.SessionID, userID, ipAddress, deviceInfo, req)

	// Step D: Execute Interbank Transfer via NIBSS Provider
	srcAcc, _ := s.coreSvc.GetBalance(ctx, srcAccID)
	srcAccName := "Customer"
	if srcAcc != nil {
		srcAccName = srcAcc.AccountName
	}

	nibssResp, err := s.provider.SingleTransfer(ctx, SingleTransferRequest{
		SessionID:                req.SessionID,
		TransactionRef:           txRef,
		SourceBankCode:           "090001",
		SourceAccountNumber:      srcAccID.String(),
		SourceAccountName:        srcAccName,
		DestinationBankCode:      req.DestinationBankCode,
		DestinationAccountNumber: req.DestinationAccountNumber,
		DestinationAccountName:   req.DestinationAccountName,
		AmountCents:              req.AmountCents,
		PaymentReference:         req.PaymentReference,
	})

	if err != nil || nibssResp == nil {
		nibssResp = &NIBSSResponse{Status: StatusPendingTimeout, SessionID: req.SessionID, TransactionRef: txRef}
	}

	// Step E: Handle explicit NIBSS response states (SUCCESS, FAILED, PENDING_TIMEOUT)
	switch nibssResp.Status {
	case StatusSuccess:
		_ = s.updateNIPStatus(ctx, req.SessionID, "completed", nil)
		return ledgerTx, "COMPLETED", nil

	case StatusFailed:
		// Confirmed failure: Immediately reverse pre-debit to refund sender!
		reversalTx, revErr := s.coreSvc.ReverseTransaction(ctx, ledgerTx.TransactionID, "NIP transfer failed at destination bank", userID.String())
		var revTxID *uuid.UUID
		if revErr == nil && reversalTx != nil {
			revTxID = &reversalTx.TransactionID
		}
		_ = s.updateNIPStatus(ctx, req.SessionID, "failed", revTxID)
		return ledgerTx, "FAILED", fmt.Errorf("transfer failed at destination bank: %s", nibssResp.Message)

	case StatusPendingTimeout:
		// Timeout/Unknown state: Do NOT refund or double-send. Queue for status reconciliation cron!
		_ = s.updateNIPStatus(ctx, req.SessionID, "pending_reconciliation", nil)
		return ledgerTx, "PENDING_RECONCILIATION", nil

	default:
		_ = s.updateNIPStatus(ctx, req.SessionID, "pending_reconciliation", nil)
		return ledgerTx, "PENDING_RECONCILIATION", nil
	}
}

// 3. INBOUND WEBHOOK HANDLER

func (s *NIPService) InboundWebhook(ctx context.Context, rawBody []byte, signatureHeader string, payload NIPInboundWebhookPayload) error {
	// Step A: Verify Cryptographic Signature (HMAC-SHA256)
	if !s.verifyWebhookSignature(rawBody, signatureHeader) {
		return fmt.Errorf("invalid webhook signature")
	}

	if payload.SessionID == "" || payload.DestinationAccountNumber == "" || payload.AmountCents <= 0 {
		return fmt.Errorf("invalid inbound webhook payload")
	}

	// Step B: Resolve target receiving account ID by account number or fallback to user account
	dstAccID, err := uuid.Parse(payload.DestinationAccountNumber)
	if err != nil {
		return fmt.Errorf("invalid destination account number format")
	}

	settlementAccID, err := s.getOrCreateSettlementAccount(ctx, "NIP Inbound Settlement", "USD")
	if err != nil {
		return fmt.Errorf("failed to obtain NIP inbound settlement account: %w", err)
	}

	// Step C: Idempotency Key derived from NIBSS Session ID to prevent double-crediting
	idempotencyKey := fmt.Sprintf("nip_inbound_%s", payload.SessionID)

	entries := []domain.PostEntryInput{
		{AccountID: settlementAccID, Direction: domain.DirectionCredit, AmountCents: payload.AmountCents},
		{AccountID: dstAccID, Direction: domain.DirectionDebit, AmountCents: payload.AmountCents},
	}

	tx, err := s.coreSvc.PostTransaction(ctx, idempotencyKey, fmt.Sprintf("Inbound NIP Deposit from %s (%s)", payload.SourceAccountName, payload.SourceBankCode), entries, "nibss_webhook")
	if err != nil {
		return fmt.Errorf("failed to credit receiving account in ledger: %w", err)
	}

	// Step D: Record NIP transaction
	now := time.Now().UTC()
	query := `
		INSERT INTO nip_transactions (nip_id, session_id, transaction_ref, user_id, source_account_id, destination_bank_code, destination_account_number, destination_account_name, amount_cents, direction, status, ledger_tx_id, created_at, updated_at)
		SELECT $1, $2, $3, ua.user_id, $4, $5, $6, $7, $8, 'inbound', 'completed', $9, $10, $10
		FROM user_accounts ua WHERE ua.account_id = $4 LIMIT 1
		ON CONFLICT (session_id) DO NOTHING
	`
	_, _ = s.repo.DB().ExecContext(ctx, query, uuid.New(), payload.SessionID, payload.TransactionRef, dstAccID, payload.SourceBankCode, payload.DestinationAccountNumber, payload.SourceAccountName, payload.AmountCents, tx.TransactionID, now)

	return nil
}

// 4. RECONCILIATION CRON FOR TIMED-OUT TRANSACTIONS

func (s *NIPService) ReconcilePendingNIPTransactions(ctx context.Context) (int, error) {
	query := `
		SELECT session_id, transaction_ref, ledger_tx_id, user_id
		FROM nip_transactions
		WHERE status = 'pending_reconciliation'
	`
	rows, err := s.repo.DB().QueryContext(ctx, query)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	type pendingRecord struct {
		sessionID string
		txRef     string
		ledgerTxID uuid.UUID
		userID    uuid.UUID
	}

	var list []pendingRecord
	for rows.Next() {
		var rec pendingRecord
		if err := rows.Scan(&rec.sessionID, &rec.txRef, &rec.ledgerTxID, &rec.userID); err == nil {
			list = append(list, rec)
		}
	}
	_ = rows.Close()

	resolvedCount := 0
	for _, p := range list {
		resp, err := s.provider.QueryTransactionStatus(ctx, p.sessionID, p.txRef)
		if err != nil || resp == nil {
			continue
		}

		if resp.Status == StatusSuccess {
			_ = s.updateNIPStatus(ctx, p.sessionID, "completed", nil)
			resolvedCount++
		} else if resp.Status == StatusFailed {
			revTx, revErr := s.coreSvc.ReverseTransaction(ctx, p.ledgerTxID, "NIP reconciliation confirmed interbank failure", p.userID.String())
			var revTxID *uuid.UUID
			if revErr == nil && revTx != nil {
				revTxID = &revTx.TransactionID
			}
			_ = s.updateNIPStatus(ctx, p.sessionID, "failed", revTxID)
			resolvedCount++
		}
	}

	return resolvedCount, nil
}

// Helper Methods

func (s *NIPService) verifyWebhookSignature(rawBody []byte, signature string) bool {
	if signature == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(s.webhookSecret))
	mac.Write(rawBody)
	expectedSig := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(strings.ToLower(signature)), []byte(strings.ToLower(expectedSig)))
}

func (s *NIPService) updateNIPStatus(ctx context.Context, sessionID, status string, reversalTxID *uuid.UUID) error {
	query := `
		UPDATE nip_transactions
		SET status = $1, reversal_tx_id = $2, updated_at = $3
		WHERE session_id = $4
	`
	_, err := s.repo.DB().ExecContext(ctx, query, status, reversalTxID, time.Now().UTC(), sessionID)
	return err
}

func (s *NIPService) getOrCreateSettlementAccount(ctx context.Context, name, currency string) (uuid.UUID, error) {
	query := `SELECT account_id FROM accounts WHERE name = $1 LIMIT 1`
	var id uuid.UUID
	err := s.repo.DB().QueryRowContext(ctx, query, name).Scan(&id)
	if err == nil {
		return id, nil
	}

	acc, err := s.coreSvc.CreateAccount(ctx, name, domain.AccountTypeEquity, currency, "system_settlement")
	if err != nil {
		return uuid.Nil, err
	}
	return acc.AccountID, nil
}

func (s *NIPService) logNIPAuditTrail(ctx context.Context, sessionID string, userID uuid.UUID, ip, device string, req NIPOutboundTransferRequest) {
	metaJSON, _ := json.Marshal(req)
	query := `
		INSERT INTO nip_audit_trail (session_id, user_id, ip_address, device_info, name_enquiry_result, user_confirmed_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`
	now := time.Now().UTC()
	_, _ = s.repo.DB().ExecContext(ctx, query, sessionID, userID, ip, device, string(metaJSON), now, now)
}

func (s *NIPService) StartReconciliationCron(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	ticker := time.NewTicker(interval)
	go func() {
		for {
			select {
			case <-ctx.Done():
				ticker.Stop()
				return
			case <-ticker.C:
				count, err := s.ReconcilePendingNIPTransactions(ctx)
				if err == nil && count > 0 {
					log.Printf("[NIP_CRON] Successfully reconciled %d pending NIP transactions", count)
				}
			}
		}
	}()
}
