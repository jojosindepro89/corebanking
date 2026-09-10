package fraud

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
)

type RiskLevel string

const (
	RiskLow    RiskLevel = "LOW"
	RiskMedium RiskLevel = "MEDIUM"
	RiskHigh   RiskLevel = "HIGH"
)

type RiskEvaluationRequest struct {
	UserID                   uuid.UUID `json:"user_id"`
	SourceAccountID          uuid.UUID `json:"source_account_id"`
	DestinationBankCode      string    `json:"destination_bank_code"`
	DestinationAccountNumber string    `json:"destination_account_number"`
	AmountCents              int64     `json:"amount_cents"`
	IPAddress                string    `json:"ip_address"`
	DeviceFingerprint        string    `json:"device_fingerprint"`
	UserConfirmedAt          time.Time `json:"user_confirmed_at"`
}

type RiskEvaluationResult struct {
	Level           RiskLevel `json:"level"`
	RulesTriggered  []string  `json:"rules_triggered"`
	Score           int       `json:"score"` // 0-100
	StepUpRequired  bool      `json:"step_up_required"`
	HoldForReview   bool      `json:"hold_for_review"`
}

type RiskScorer interface {
	Evaluate(ctx context.Context, req RiskEvaluationRequest) (*RiskEvaluationResult, error)
}

type RulesRiskScorer struct {
	db *sql.DB
}

func NewRulesRiskScorer(db *sql.DB) *RulesRiskScorer {
	return &RulesRiskScorer{db: db}
}

func (s *RulesRiskScorer) getRuleNumeric(ctx context.Context, ruleKey string, defaultValue float64) float64 {
	var val float64
	err := s.db.QueryRowContext(ctx, "SELECT value_numeric FROM fraud_rule_configs WHERE rule_key = $1 AND is_enabled = true", ruleKey).Scan(&val)
	if err != nil {
		return defaultValue
	}
	return val
}

func (s *RulesRiskScorer) Evaluate(ctx context.Context, req RiskEvaluationRequest) (*RiskEvaluationResult, error) {
	rulesTriggered := []string{}
	score := 0

	// 1. Velocity Rule: Count transfers by this user in last 1 hour against dynamic threshold
	maxHourly := int(s.getRuleNumeric(ctx, "velocity_max_transfers_per_hour", 5))
	var transfersLastHour int
	hourAgo := time.Now().UTC().Add(-1 * time.Hour)
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM nip_transactions WHERE user_id = $1 AND created_at >= $2", req.UserID, hourAgo).Scan(&transfersLastHour)
	if err == nil && transfersLastHour >= maxHourly {
		rulesTriggered = append(rulesTriggered, "VELOCITY_EXCEEDED_HOURLY_LIMIT")
		score += 35
	}

	// 2. Amount Anomaly Rule: > multiplier x trailing 30-day average transfer size
	multiplier := s.getRuleNumeric(ctx, "amount_anomaly_multiplier", 5.0)
	var avgAmount int64
	monthAgo := time.Now().UTC().Add(-30 * 24 * time.Hour)
	err = s.db.QueryRowContext(ctx, "SELECT COALESCE(AVG(amount_cents)::bigint, 0) FROM nip_transactions WHERE user_id = $1 AND created_at >= $2", req.UserID, monthAgo).Scan(&avgAmount)
	if err == nil && avgAmount > 0 && float64(req.AmountCents) >= (multiplier*float64(avgAmount)) {
		rulesTriggered = append(rulesTriggered, "AMOUNT_ANOMALY_MULTIPLIER_EXCEEDED")
		score += 30
	}

	// 3. New Device / Location Rule
	var deviceExists bool
	err = s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM user_devices WHERE user_id = $1 AND device_fingerprint = $2)", req.UserID, req.DeviceFingerprint).Scan(&deviceExists)
	if err == nil && !deviceExists && req.DeviceFingerprint != "" {
		rulesTriggered = append(rulesTriggered, "NEW_UNRECOGNIZED_DEVICE")
		score += 25
	}

	// 4. New Recipient + High Amount Rule (First transfer to account & amount >= dynamic threshold)
	newRecipientHighAmount := int64(s.getRuleNumeric(ctx, "new_recipient_high_amount_cents", 200000))
	var recipientExists bool
	err = s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM nip_transactions WHERE user_id = $1 AND destination_account_number = $2 AND status = 'completed')", req.UserID, req.DestinationAccountNumber).Scan(&recipientExists)
	if err == nil && !recipientExists && req.AmountCents >= newRecipientHighAmount {
		rulesTriggered = append(rulesTriggered, "NEW_RECIPIENT_HIGH_AMOUNT")
		score += 40
	}

	// Calculate Risk Level based on cumulative score
	level := RiskLow
	stepUpRequired := false
	holdForReview := false

	if score >= 50 {
		level = RiskHigh
		holdForReview = true
	} else if score >= 25 || len(rulesTriggered) > 0 {
		level = RiskMedium
		stepUpRequired = true
	}

	return &RiskEvaluationResult{
		Level:          level,
		RulesTriggered: rulesTriggered,
		Score:          score,
		StepUpRequired: stepUpRequired,
		HoldForReview:  holdForReview,
	}, nil
}
