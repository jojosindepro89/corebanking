package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"core-banking-ledger/internal/domain"
	gwrepo "core-banking-ledger/internal/gateway/repository"
	coreService "core-banking-ledger/internal/service"

	"github.com/google/uuid"
)

type ReconciliationRunner struct {
	repo       *gwrepo.GatewayRepository
	coreSvc    *coreService.Service
	webhookURL string
}

func NewReconciliationRunner(repo *gwrepo.GatewayRepository, coreSvc *coreService.Service) *ReconciliationRunner {
	webhook := os.Getenv("ALERT_WEBHOOK_URL")
	return &ReconciliationRunner{
		repo:       repo,
		coreSvc:    coreSvc,
		webhookURL: webhook,
	}
}

type ReconciliationResult struct {
	RunID           uuid.UUID                        `json:"run_id"`
	TotalAccounts   int                              `json:"total_accounts"`
	MismatchesCount int                              `json:"mismatches_count"`
	SystemBalanced  bool                             `json:"system_balanced"`
	TotalDebits     int64                            `json:"total_debits"`
	TotalCredits    int64                            `json:"total_credits"`
	Mismatches      []domain.ReconciliationMismatch `json:"mismatches"`
	TriggeredBy     string                           `json:"triggered_by"`
	CreatedAt       time.Time                        `json:"created_at"`
}

func (r *ReconciliationRunner) RunReconciliation(ctx context.Context, triggeredBy string) (*ReconciliationResult, error) {
	now := time.Now().UTC()
	runID := uuid.New()

	// 1. Reconcile all individual accounts (Computed vs Cached)
	mismatches, err := r.coreSvc.ReconcileAllAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("reconciliation failed: %w", err)
	}

	// 2. Check system-wide invariant (Total Debits == Total Credits)
	totalDebits, totalCredits, err := r.repo.GetSystemWideTotals(ctx, r.repo.DB())
	if err != nil {
		return nil, fmt.Errorf("system wide totals check failed: %w", err)
	}

	systemBalanced := (totalDebits == totalCredits)
	mismatchesCount := len(mismatches)

	// Fetch all accounts count
	allAccounts, _ := r.coreSvc.GetAllAccounts(ctx)
	totalAccounts := len(allAccounts)

	mismatchesJSON, _ := json.Marshal(mismatches)

	res := &ReconciliationResult{
		RunID:           runID,
		TotalAccounts:   totalAccounts,
		MismatchesCount: mismatchesCount,
		SystemBalanced:  systemBalanced,
		TotalDebits:     totalDebits,
		TotalCredits:    totalCredits,
		Mismatches:      mismatches,
		TriggeredBy:     triggeredBy,
		CreatedAt:       now,
	}

	// Save record to DB
	recRecord := &gwrepo.ReconciliationRunRecord{
		RunID:           runID,
		TotalAccounts:   totalAccounts,
		MismatchesCount: mismatchesCount,
		SystemBalanced:  systemBalanced,
		Discrepancies:   string(mismatchesJSON),
		TriggeredBy:     triggeredBy,
		CreatedAt:       now,
	}
	_ = r.repo.SaveReconciliationRun(ctx, r.repo.DB(), recRecord)

	// High-Severity Alerting if mismatch or imbalanced system
	if mismatchesCount > 0 || !systemBalanced {
		alertMsg := fmt.Sprintf("[CRITICAL_LEDGER_ALERT] High severity mismatch detected! RunID: %s, Mismatches: %d, Debits: %d, Credits: %d",
			runID, mismatchesCount, totalDebits, totalCredits)
		log.Printf("%s", alertMsg)
		r.sendAlertWebhook(alertMsg, res)
	} else {
		log.Printf("[RECONCILIATION_OK] Ledger fully balanced. RunID: %s, Accounts: %d, Debits/Credits: %d", runID, totalAccounts, totalDebits)
	}

	return res, nil
}

func (r *ReconciliationRunner) sendAlertWebhook(message string, res *ReconciliationResult) {
	if r.webhookURL == "" {
		return
	}
	payload := map[string]any{
		"text":    message,
		"details": res,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}

	go func() {
		client := &http.Client{Timeout: 5 * time.Second}
		_, _ = client.Post(r.webhookURL, "application/json", bytes.NewBuffer(body))
	}()
}

func (r *ReconciliationRunner) StartCron(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 15 * time.Minute
	}

	ticker := time.NewTicker(interval)
	go func() {
		for {
			select {
			case <-ctx.Done():
				ticker.Stop()
				return
			case <-ticker.C:
				_, _ = r.RunReconciliation(ctx, "scheduled_cron")
			}
		}
	}()
}
