package service

import (
	"context"
	"fmt"
	"time"

	"core-banking-ledger/internal/domain"
	gwdomain "core-banking-ledger/internal/gateway/domain"
	gwrepo "core-banking-ledger/internal/gateway/repository"
	coreService "core-banking-ledger/internal/service"

	"github.com/google/uuid"
)

type AdminDashboardService struct {
	repo       *gwrepo.GatewayRepository
	coreSvc    *coreService.Service
	reconciler *ReconciliationRunner
	flagLimit  int64 // default 1,000,000 cents ($10,000.00)
}

func NewAdminDashboardService(repo *gwrepo.GatewayRepository, coreSvc *coreService.Service, reconciler *ReconciliationRunner) *AdminDashboardService {
	return &AdminDashboardService{
		repo:       repo,
		coreSvc:    coreSvc,
		reconciler: reconciler,
		flagLimit:  1000000, // $10,000.00
	}
}

type SystemHealthOverview struct {
	SystemBalanced         bool                              `json:"system_balanced"`
	TotalDebits            int64                             `json:"total_debits"`
	TotalCredits           int64                             `json:"total_credits"`
	Difference             int64                             `json:"difference"`
	LastReconciliation     *gwrepo.ReconciliationRunRecord   `json:"last_reconciliation,omitempty"`
	PendingFlaggedCount    int                               `json:"pending_flagged_count"`
	PendingKYCAppCount     int                               `json:"pending_kyc_count"`
	Timestamp              time.Time                         `json:"timestamp"`
}

func (s *AdminDashboardService) GetSystemHealth(ctx context.Context) (*SystemHealthOverview, error) {
	debits, credits, err := s.repo.GetSystemWideTotals(ctx, s.repo.DB())
	if err != nil {
		return nil, err
	}

	diff := debits - credits
	systemBalanced := (diff == 0)

	runs, _ := s.repo.GetRecentReconciliationRuns(ctx, s.repo.DB(), 1)
	var lastRun *gwrepo.ReconciliationRunRecord
	if len(runs) > 0 {
		lastRun = &runs[0]
	}

	flaggedList, _ := s.repo.GetFlaggedTransactions(ctx, s.repo.DB(), "pending_review")
	kycList, _ := s.repo.GetKYCApplications(ctx, s.repo.DB(), "pending_review")

	return &SystemHealthOverview{
		SystemBalanced:      systemBalanced,
		TotalDebits:         debits,
		TotalCredits:        credits,
		Difference:          diff,
		LastReconciliation:  lastRun,
		PendingFlaggedCount: len(flaggedList),
		PendingKYCAppCount:  len(kycList),
		Timestamp:           time.Now().UTC(),
	}, nil
}

func (s *AdminDashboardService) RunOnDemandReconciliation(ctx context.Context, adminID uuid.UUID) (*ReconciliationResult, error) {
	return s.reconciler.RunReconciliation(ctx, fmt.Sprintf("admin_%s", adminID.String()))
}

func (s *AdminDashboardService) GetReconciliationHistory(ctx context.Context, limit int) ([]gwrepo.ReconciliationRunRecord, error) {
	return s.repo.GetRecentReconciliationRuns(ctx, s.repo.DB(), limit)
}

func (s *AdminDashboardService) GetFlaggedTransactions(ctx context.Context, status string) ([]gwrepo.FlaggedTransactionRecord, error) {
	return s.repo.GetFlaggedTransactions(ctx, s.repo.DB(), status)
}

func (s *AdminDashboardService) ReviewFlaggedTransaction(ctx context.Context, flagID uuid.UUID, status string, adminID uuid.UUID) error {
	if status != "approved" && status != "dismissed" {
		return fmt.Errorf("invalid status: must be 'approved' or 'dismissed'")
	}
	return s.repo.ReviewFlaggedTransaction(ctx, s.repo.DB(), flagID, status, adminID)
}

func (s *AdminDashboardService) CheckAndFlagHighValueTx(ctx context.Context, txID uuid.UUID, amountCents int64) {
	if amountCents >= s.flagLimit {
		_ = s.repo.FlagTransaction(ctx, s.repo.DB(), txID, amountCents, fmt.Sprintf("High-value transaction >= $%d", s.flagLimit/100))
	}
}

func (s *AdminDashboardService) GetKYCApplications(ctx context.Context, status string) ([]gwrepo.KYCApplicationRecord, error) {
	return s.repo.GetKYCApplications(ctx, s.repo.DB(), status)
}

func (s *AdminDashboardService) SubmitKYCApplication(ctx context.Context, userID uuid.UUID, fullName, dob, idNumber string) (*gwrepo.KYCApplicationRecord, error) {
	app := &gwrepo.KYCApplicationRecord{
		KYCID:     uuid.New(),
		UserID:    userID,
		FullName:  fullName,
		DOB:       dob,
		IDNumber:  idNumber,
		Status:    "pending_review",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	if err := s.repo.CreateKYCApplication(ctx, s.repo.DB(), app); err != nil {
		return nil, err
	}
	return app, nil
}

func (s *AdminDashboardService) ReviewKYCApplication(ctx context.Context, kycID uuid.UUID, status, reason string, adminID uuid.UUID) error {
	if status != "approved" && status != "rejected" {
		return fmt.Errorf("invalid status: must be 'approved' or 'rejected'")
	}
	return s.repo.ReviewKYCApplication(ctx, s.repo.DB(), kycID, status, reason, adminID)
}

func (s *AdminDashboardService) UnfreezeAccount(ctx context.Context, accountID uuid.UUID, reason string, adminID uuid.UUID) error {
	return s.repo.UpdateAccountStatus(ctx, s.repo.DB(), accountID, string(domain.AccountStatusActive))
}

func (s *AdminDashboardService) SearchAuditLogs(ctx context.Context, userIDStr, endpoint, outcome string, limit, offset int) ([]gwdomain.APIAuditRecord, error) {
	return s.repo.SearchAPIAuditLogs(ctx, s.repo.DB(), userIDStr, endpoint, outcome, limit, offset)
}
