package service

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"

	"core-banking-ledger/pkg/domain"
	"core-banking-ledger/pkg/repository"

	"github.com/google/uuid"
)

type LoanEngineService interface {
	OriginateLoan(ctx context.Context, userID, borrowerAccountID, vaultAccountID uuid.UUID, principalCents int64, interestRate float64, termMonths int, actor string) (*domain.Loan, error)
	ProcessLoanRepayment(ctx context.Context, loanID, borrowerAccountID, loanAssetAccountID uuid.UUID, amountCents int64, actor string) (*domain.Loan, error)
	GetLoanDetails(ctx context.Context, loanID uuid.UUID) (*domain.Loan, []domain.LoanSchedule, error)
}

type LoanEngine struct {
	repo          *repository.Repository
	ledgerService LedgerService
}

func NewLoanEngine(repo *repository.Repository, ledgerService LedgerService) *LoanEngine {
	return &LoanEngine{
		repo:          repo,
		ledgerService: ledgerService,
	}
}

// OriginateLoan creates a loan, generates amortization schedule, and disburses principal to borrower account
func (le *LoanEngine) OriginateLoan(ctx context.Context, userID, borrowerAccountID, vaultAccountID uuid.UUID, principalCents int64, interestRate float64, termMonths int, actor string) (*domain.Loan, error) {
	if principalCents <= 0 {
		return nil, fmt.Errorf("principal must be greater than zero")
	}
	if termMonths <= 0 {
		return nil, fmt.Errorf("term months must be greater than zero")
	}
	if interestRate < 0 {
		return nil, fmt.Errorf("interest rate cannot be negative")
	}

	monthlyRate := (interestRate / 100.0) / 12.0
	var monthlyInstallmentCents int64

	if monthlyRate > 0 {
		pmt := float64(principalCents) * (monthlyRate * math.Pow(1+monthlyRate, float64(termMonths))) / (math.Pow(1+monthlyRate, float64(termMonths)) - 1)
		monthlyInstallmentCents = int64(math.Round(pmt))
	} else {
		monthlyInstallmentCents = principalCents / int64(termMonths)
	}

	now := time.Now().UTC()
	loanID := uuid.New()

	loan := &domain.Loan{
		LoanID:                  loanID,
		UserID:                  userID,
		AccountID:               borrowerAccountID,
		PrincipalCents:          principalCents,
		InterestRatePercent:     interestRate,
		TermMonths:              termMonths,
		MonthlyInstallmentCents: monthlyInstallmentCents,
		OutstandingBalanceCents: principalCents,
		Status:                  "active",
		DelinquencyBucket:       "current",
		StartDate:               now,
		NextPaymentDue:          now.AddDate(0, 1, 0),
	}

	// Generate Amortization Schedule
	schedules := make([]domain.LoanSchedule, termMonths)
	remainingBalance := float64(principalCents)

	for i := 1; i <= termMonths; i++ {
		interestDue := remainingBalance * monthlyRate
		interestDueCents := int64(math.Round(interestDue))
		principalDueCents := monthlyInstallmentCents - interestDueCents

		if i == termMonths {
			// Final installment balance adjustment
			principalDueCents = int64(math.Round(remainingBalance))
			monthlyInstallmentCents = principalDueCents + interestDueCents
		}

		schedules[i-1] = domain.LoanSchedule{
			LoanID:            loanID,
			InstallmentNumber: i,
			DueDate:           now.AddDate(0, i, 0),
			PrincipalDueCents: principalDueCents,
			InterestDueCents:  interestDueCents,
			TotalDueCents:     monthlyInstallmentCents,
			PaidCents:         0,
			Status:            "pending",
		}

		remainingBalance -= float64(principalDueCents)
	}

	err := le.repo.ExecuteInSerializableTx(ctx, func(tx *sql.Tx) error {
		if err := le.repo.CreateLoan(ctx, tx, loan); err != nil {
			return err
		}
		if err := le.repo.CreateLoanSchedules(ctx, tx, schedules); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to save loan in database: %w", err)
	}

	// Disburse funds: Debit Bank Vault (Asset), Credit Borrower Account (Liability/Asset)
	idempotencyKey := fmt.Sprintf("loan_disbursement_%s", loanID.String())
	entries := []domain.PostEntryInput{
		{
			AccountID:   vaultAccountID,
			Direction:   domain.DirectionDebit,
			AmountCents: principalCents,
		},
		{
			AccountID:   borrowerAccountID,
			Direction:   domain.DirectionCredit,
			AmountCents: principalCents,
		},
	}

	_, err = le.ledgerService.PostTransaction(ctx, idempotencyKey, fmt.Sprintf("Loan Principal Disbursement for Loan %s", loanID.String()), entries, actor)
	if err != nil {
		return nil, fmt.Errorf("failed disbursing loan funds to borrower: %w", err)
	}

	return loan, nil
}

// ProcessLoanRepayment processes a borrower's loan repayment
func (le *LoanEngine) ProcessLoanRepayment(ctx context.Context, loanID, borrowerAccountID, loanAssetAccountID uuid.UUID, amountCents int64, actor string) (*domain.Loan, error) {
	if amountCents <= 0 {
		return nil, domain.ErrInvalidAmount
	}

	db := le.repo.DB()
	loan, err := le.repo.GetLoanByID(ctx, db, loanID)
	if err != nil {
		return nil, err
	}
	if loan.Status == "paid_off" {
		return nil, domain.ErrLoanAlreadyPaidOff
	}

	// Post Repayment Transaction: Debit Borrower Account, Credit Loan Asset Account
	idempotencyKey := fmt.Sprintf("loan_repayment_%s_%d", loanID.String(), time.Now().UnixNano())
	entries := []domain.PostEntryInput{
		{
			AccountID:   borrowerAccountID,
			Direction:   domain.DirectionDebit,
			AmountCents: amountCents,
		},
		{
			AccountID:   loanAssetAccountID,
			Direction:   domain.DirectionCredit,
			AmountCents: amountCents,
		},
	}

	_, err = le.ledgerService.PostTransaction(ctx, idempotencyKey, fmt.Sprintf("Repayment for Loan %s", loanID.String()), entries, actor)
	if err != nil {
		return nil, fmt.Errorf("loan repayment posting failed: %w", err)
	}

	newBalance := loan.OutstandingBalanceCents - amountCents
	newStatus := "active"
	if newBalance <= 0 {
		newBalance = 0
		newStatus = "paid_off"
	}

	if err := le.repo.UpdateLoanRepayment(ctx, db, loanID, newBalance, newStatus); err != nil {
		return nil, err
	}

	loan.OutstandingBalanceCents = newBalance
	loan.Status = newStatus
	return loan, nil
}

func (le *LoanEngine) GetLoanDetails(ctx context.Context, loanID uuid.UUID) (*domain.Loan, []domain.LoanSchedule, error) {
	db := le.repo.DB()
	loan, err := le.repo.GetLoanByID(ctx, db, loanID)
	if err != nil {
		return nil, nil, err
	}
	schedules, err := le.repo.GetLoanSchedules(ctx, db, loanID)
	if err != nil {
		return nil, nil, err
	}
	return loan, schedules, nil
}
