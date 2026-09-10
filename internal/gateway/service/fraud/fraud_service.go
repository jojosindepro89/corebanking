package fraud

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
)

type NotificationService interface {
	NotifyUser(ctx context.Context, userID uuid.UUID, title, message string) error
}

type MockNotificationService struct{}

func (m *MockNotificationService) NotifyUser(ctx context.Context, userID uuid.UUID, title, message string) error {
	log.Printf("[NOTIFICATION-MOCK] User %s | Title: %s | Message: %s", userID.String(), title, message)
	return nil
}

type FraudService struct {
	db                  *sql.DB
	scorer              RiskScorer
	notificationService NotificationService
}

func NewFraudService(db *sql.DB, scorer RiskScorer, notif NotificationService) *FraudService {
	if notif == nil {
		notif = &MockNotificationService{}
	}
	if scorer == nil {
		scorer = NewRulesRiskScorer(db)
	}
	return &FraudService{
		db:                  db,
		scorer:              scorer,
		notificationService: notif,
	}
}

type HeldTransactionDTO struct {
	HoldID                    uuid.UUID  `json:"hold_id"`
	TransactionID             *uuid.UUID `json:"transaction_id"`
	UserID                    uuid.UUID  `json:"user_id"`
	EvidenceID                uuid.UUID  `json:"evidence_id"`
	AmountCents               int64      `json:"amount_cents"`
	Status                    string     `json:"status"`
	ReviewedBy                *uuid.UUID `json:"reviewed_by"`
	RejectionReason           string     `json:"rejection_reason"`
	CreatedAt                 time.Time  `json:"created_at"`
	RiskScore                 string     `json:"risk_score"`
	RulesTriggered            []string   `json:"rules_triggered"`
	DeviceFingerprint         string     `json:"device_fingerprint"`
	IPAddress                 string     `json:"ip_address"`
	DestinationAccountNumber string     `json:"destination_account_number"`
}

type EvidenceRecordDTO struct {
	EvidenceID        uuid.UUID       `json:"evidence_id"`
	TransactionID     *uuid.UUID      `json:"transaction_id"`
	UserID            uuid.UUID       `json:"user_id"`
	SessionID         string          `json:"session_id"`
	NameEnquiryResult json.RawMessage `json:"name_enquiry_result"`
	UserConfirmedAt   time.Time       `json:"user_confirmed_at"`
	DeviceFingerprint string          `json:"device_fingerprint"`
	IPAddress         string          `json:"ip_address"`
	RiskScore         string          `json:"risk_score"`
	RulesTriggered    []string        `json:"rules_triggered"`
	StepUpRequired    bool            `json:"stepup_required"`
	StepUpCompleted   bool            `json:"stepup_completed"`
	CreatedAt         time.Time       `json:"created_at"`
}

type RuleConfigDTO struct {
	RuleKey      string    `json:"rule_key"`
	ValueNumeric float64   `json:"value_numeric"`
	ValueString  string    `json:"value_string"`
	IsEnabled    bool      `json:"is_enabled"`
	Description  string    `json:"description"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (s *FraudService) EvaluateAndRecord(
	ctx context.Context,
	req RiskEvaluationRequest,
	nameEnquiryJSON string,
	stepUpValidated bool,
) (*RiskEvaluationResult, uuid.UUID, error) {
	// 1. Evaluate risk score using pluggable scorer FIRST
	result, err := s.scorer.Evaluate(ctx, req)
	if err != nil {
		return nil, uuid.Nil, fmt.Errorf("risk evaluation failed: %w", err)
	}

	// 2. Record / update device fingerprint AFTER risk evaluation
	if req.DeviceFingerprint != "" {
		_, _ = s.db.ExecContext(ctx, `
			INSERT INTO user_devices (user_id, device_fingerprint, last_ip, created_at, last_used_at)
			VALUES ($1, $2, $3, NOW(), NOW())
			ON CONFLICT (user_id, device_fingerprint)
			DO UPDATE SET last_ip = EXCLUDED.last_ip, last_used_at = NOW()
		`, req.UserID, req.DeviceFingerprint, req.IPAddress)
	}

	// Convert rules triggered to JSON
	rulesJSON, _ := json.Marshal(result.RulesTriggered)
	if nameEnquiryJSON == "" {
		nameEnquiryJSON = "{}"
	}

	// 3. Store immutable APP dispute evidence record
	var evidenceID uuid.UUID
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO fraud_evidence_records (
			user_id, session_id, name_enquiry_result, user_confirmed_at,
			device_fingerprint, ip_address, risk_score, rules_triggered,
			stepup_required, stepup_completed, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NOW())
		RETURNING evidence_id
	`,
		req.UserID,
		req.DestinationBankCode+"-"+req.DestinationAccountNumber,
		nameEnquiryJSON,
		req.UserConfirmedAt,
		req.DeviceFingerprint,
		req.IPAddress,
		string(result.Level),
		rulesJSON,
		result.StepUpRequired,
		stepUpValidated,
	).Scan(&evidenceID)
	if err != nil {
		return nil, uuid.Nil, fmt.Errorf("failed to save evidence record: %w", err)
	}

	// 4. If HIGH risk, place into held queue and notify user
	if result.Level == RiskHigh || result.HoldForReview {
		var holdID uuid.UUID
		err = s.db.QueryRowContext(ctx, `
			INSERT INTO held_fraud_transactions (
				user_id, evidence_id, amount_cents, status, created_at, updated_at
			) VALUES ($1, $2, $3, 'pending_review', NOW(), NOW())
			RETURNING hold_id
		`, req.UserID, evidenceID, req.AmountCents).Scan(&holdID)
		if err != nil {
			return nil, evidenceID, fmt.Errorf("failed to create held transaction record: %w", err)
		}

		_ = s.notificationService.NotifyUser(
			ctx,
			req.UserID,
			"Transaction Flagged for Security Review",
			fmt.Sprintf("Your outbound transfer of %d cents has been held for manual compliance review. Ref: %s", req.AmountCents, holdID.String()),
		)
	}

	return result, evidenceID, nil
}

func (s *FraudService) UpdateEvidenceTransactionID(ctx context.Context, evidenceID uuid.UUID, txID uuid.UUID) error {
	_, err := s.db.ExecContext(ctx, "UPDATE fraud_evidence_records SET transaction_id = $1 WHERE evidence_id = $2", txID, evidenceID)
	if err != nil {
		return fmt.Errorf("failed to update evidence tx id: %w", err)
	}
	_, err = s.db.ExecContext(ctx, "UPDATE held_fraud_transactions SET transaction_id = $1 WHERE evidence_id = $2", txID, evidenceID)
	return err
}

func (s *FraudService) GetHeldTransactions(ctx context.Context, statusFilter string) ([]HeldTransactionDTO, error) {
	query := `
		SELECT h.hold_id, h.transaction_id, h.user_id, h.evidence_id, h.amount_cents, h.status,
		       h.reviewed_by, COALESCE(h.rejection_reason, ''), h.created_at,
		       e.risk_score, e.rules_triggered, e.device_fingerprint, e.ip_address
		FROM held_fraud_transactions h
		JOIN fraud_evidence_records e ON h.evidence_id = e.evidence_id
	`
	args := []interface{}{}
	if statusFilter != "" {
		query += " WHERE h.status = $1"
		args = append(args, statusFilter)
	}
	query += " ORDER BY h.created_at DESC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query held transactions: %w", err)
	}
	defer rows.Close()

	list := []HeldTransactionDTO{}
	for rows.Next() {
		var dto HeldTransactionDTO
		var rulesJSON []byte
		err := rows.Scan(
			&dto.HoldID, &dto.TransactionID, &dto.UserID, &dto.EvidenceID, &dto.AmountCents, &dto.Status,
			&dto.ReviewedBy, &dto.RejectionReason, &dto.CreatedAt,
			&dto.RiskScore, &rulesJSON, &dto.DeviceFingerprint, &dto.IPAddress,
		)
		if err != nil {
			return nil, err
		}
		_ = json.Unmarshal(rulesJSON, &dto.RulesTriggered)
		list = append(list, dto)
	}
	return list, nil
}

func (s *FraudService) ApproveHeldTransaction(ctx context.Context, holdID uuid.UUID, adminID uuid.UUID) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE held_fraud_transactions
		SET status = 'approved', reviewed_by = $1, updated_at = NOW()
		WHERE hold_id = $2 AND status = 'pending_review'
	`, adminID, holdID)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("held transaction not found or already processed")
	}
	return nil
}

func (s *FraudService) RejectHeldTransaction(ctx context.Context, holdID uuid.UUID, adminID uuid.UUID, reason string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE held_fraud_transactions
		SET status = 'rejected', reviewed_by = $1, rejection_reason = $2, updated_at = NOW()
		WHERE hold_id = $3 AND status = 'pending_review'
	`, adminID, reason, holdID)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("held transaction not found or already processed")
	}
	return nil
}

func (s *FraudService) GetAPPEvidenceByTxID(ctx context.Context, txID uuid.UUID) (*EvidenceRecordDTO, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT evidence_id, transaction_id, user_id, session_id, name_enquiry_result,
		       user_confirmed_at, device_fingerprint, ip_address, risk_score, rules_triggered,
		       stepup_required, stepup_completed, created_at
		FROM fraud_evidence_records
		WHERE transaction_id = $1
	`, txID)

	var dto EvidenceRecordDTO
	var rulesJSON []byte
	err := row.Scan(
		&dto.EvidenceID, &dto.TransactionID, &dto.UserID, &dto.SessionID, &dto.NameEnquiryResult,
		&dto.UserConfirmedAt, &dto.DeviceFingerprint, &dto.IPAddress, &dto.RiskScore, &rulesJSON,
		&dto.StepUpRequired, &dto.StepUpCompleted, &dto.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("evidence record not found for transaction %s: %w", txID, err)
	}
	_ = json.Unmarshal(rulesJSON, &dto.RulesTriggered)
	return &dto, nil
}

func (s *FraudService) GetRuleConfigs(ctx context.Context) ([]RuleConfigDTO, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT rule_key, value_numeric, value_string, is_enabled, description, updated_at FROM fraud_rule_configs ORDER BY rule_key ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	configs := []RuleConfigDTO{}
	for rows.Next() {
		var cfg RuleConfigDTO
		if err := rows.Scan(&cfg.RuleKey, &cfg.ValueNumeric, &cfg.ValueString, &cfg.IsEnabled, &cfg.Description, &cfg.UpdatedAt); err != nil {
			return nil, err
		}
		configs = append(configs, cfg)
	}
	return configs, nil
}

func (s *FraudService) UpdateRuleConfig(ctx context.Context, ruleKey string, valNumeric float64, valString string, isEnabled bool) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO fraud_rule_configs (rule_key, value_numeric, value_string, is_enabled, updated_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (rule_key)
		DO UPDATE SET value_numeric = EXCLUDED.value_numeric, value_string = EXCLUDED.value_string, is_enabled = EXCLUDED.is_enabled, updated_at = NOW()
	`, ruleKey, valNumeric, valString, isEnabled)
	return err
}
