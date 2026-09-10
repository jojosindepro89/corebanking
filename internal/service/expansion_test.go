package service_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"core-banking-ledger/internal/domain"
	"core-banking-ledger/internal/repository"
	"core-banking-ledger/internal/service"

	"github.com/google/uuid"
)

func TestExpansion_InterestEngineAccrualAndPosting(t *testing.T) {
	db, ledgerSvc := setupTestDB(t)
	ctx := context.Background()

	repo := repository.NewRepository(db)
	interestEngine := service.NewInterestEngine(repo, ledgerSvc)

	// Create Bank Interest Expense Account (Asset/Expense) & Savings Account (Liability)
	expenseAcc, err := ledgerSvc.CreateAccount(ctx, "Bank Interest Expense", domain.AccountTypeExpense, "USD", "test_admin")
	if err != nil {
		t.Fatalf("Failed to create expense account: %v", err)
	}

	savingsAcc, err := ledgerSvc.CreateAccount(ctx, "Customer Savings Account", domain.AccountTypeAsset, "USD", "test_user")
	if err != nil {
		t.Fatalf("Failed to create savings account: %v", err)
	}

	vaultAcc, err := ledgerSvc.CreateAccount(ctx, "Cash Vault", domain.AccountTypeAsset, "USD", "test_user")
	if err != nil {
		t.Fatalf("Failed to create vault account: %v", err)
	}

	// Deposit $10,000 (1,000,000 cents) into savings account
	entries := []domain.PostEntryInput{
		{AccountID: vaultAcc.AccountID, Direction: domain.DirectionDebit, AmountCents: 1000000},
		{AccountID: savingsAcc.AccountID, Direction: domain.DirectionCredit, AmountCents: 1000000},
	}
	_, err = ledgerSvc.PostTransaction(ctx, fmt.Sprintf("dep_%d", time.Now().UnixNano()), "Initial Deposit", entries, "test_user")
	if err != nil {
		t.Fatalf("Failed deposit: %v", err)
	}

	// Calculate 1 day of accrued interest
	accrualDate := time.Now().UTC()
	count, err := interestEngine.CalculateAndSaveDailyAccruals(ctx, accrualDate)
	if err != nil {
		t.Fatalf("CalculateAndSaveDailyAccruals failed: %v", err)
	}
	if count == 0 {
		t.Logf("Warning: 0 accounts accrued interest (check interest_products table)")
	}

	// Post accrued interest batch
	posted, err := interestEngine.PostAccruedInterestBatch(ctx, expenseAcc.AccountID, "system_eom")
	if err != nil {
		t.Fatalf("PostAccruedInterestBatch failed: %v", err)
	}
	t.Logf("Posted %d interest transactions", posted)
}

func TestExpansion_LoanEngineOriginationAndRepayment(t *testing.T) {
	db, ledgerSvc := setupTestDB(t)
	ctx := context.Background()

	repo := repository.NewRepository(db)
	loanEngine := service.NewLoanEngine(repo, ledgerSvc)

	userID := uuid.New()
	borrowerAcc, _ := ledgerSvc.CreateAccount(ctx, "Borrower Account", domain.AccountTypeAsset, "USD", "borrower")
	vaultAcc, _ := ledgerSvc.CreateAccount(ctx, "Bank Vault", domain.AccountTypeAsset, "USD", "bank")
	loanAssetAcc, _ := ledgerSvc.CreateAccount(ctx, "Loan Portfolio Asset", domain.AccountTypeAsset, "USD", "bank")

	// Originate Loan: $12,000 (1,200,000 cents) at 10% annual rate for 12 months
	loan, err := loanEngine.OriginateLoan(ctx, userID, borrowerAcc.AccountID, vaultAcc.AccountID, 1200000, 10.0, 12, "officer")
	if err != nil {
		t.Fatalf("OriginateLoan failed: %v", err)
	}

	if loan.LoanID == uuid.Nil {
		t.Fatalf("Expected valid LoanID")
	}

	if loan.OutstandingBalanceCents != 1200000 {
		t.Errorf("Expected outstanding balance 1200000, got %d", loan.OutstandingBalanceCents)
	}

	// Fetch details and schedules
	fetchedLoan, schedules, err := loanEngine.GetLoanDetails(ctx, loan.LoanID)
	if err != nil {
		t.Fatalf("GetLoanDetails failed: %v", err)
	}

	if len(schedules) != 12 {
		t.Errorf("Expected 12 repayment schedules, got %d", len(schedules))
	}

	// Process repayment of $1,000 (100,000 cents)
	updatedLoan, err := loanEngine.ProcessLoanRepayment(ctx, fetchedLoan.LoanID, borrowerAcc.AccountID, loanAssetAcc.AccountID, 100000, "borrower")
	if err != nil {
		t.Fatalf("ProcessLoanRepayment failed: %v", err)
	}

	expectedRemaining := int64(1100000)
	if updatedLoan.OutstandingBalanceCents != expectedRemaining {
		t.Errorf("Expected remaining balance %d, got %d", expectedRemaining, updatedLoan.OutstandingBalanceCents)
	}
}

func TestExpansion_StandingOrdersExecution(t *testing.T) {
	db, ledgerSvc := setupTestDB(t)
	ctx := context.Background()

	repo := repository.NewRepository(db)
	standingEngine := service.NewStandingOrderEngine(repo, ledgerSvc)

	userID := uuid.New()
	srcAcc, _ := ledgerSvc.CreateAccount(ctx, "Salary Checking", domain.AccountTypeAsset, "USD", "user")
	dstAcc, _ := ledgerSvc.CreateAccount(ctx, "Auto Savings Sweep", domain.AccountTypeLiability, "USD", "user")

	order := &domain.StandingOrder{
		UserID:               userID,
		SourceAccountID:      srcAcc.AccountID,
		DestinationAccountID: dstAcc.AccountID,
		AmountCents:          5000, // $50.00
		Description:          "Monthly Auto Savings",
		Frequency:            "monthly",
		NextRunAt:            time.Now().UTC().Add(-1 * time.Hour), // Due immediately
	}

	if err := standingEngine.CreateStandingOrder(ctx, order); err != nil {
		t.Fatalf("CreateStandingOrder failed: %v", err)
	}

	executed, err := standingEngine.ProcessDueStandingOrders(ctx, "cron_runner")
	if err != nil {
		t.Fatalf("ProcessDueStandingOrders failed: %v", err)
	}

	if executed < 1 {
		t.Errorf("Expected at least 1 standing order to execute, got %d", executed)
	}
}

func TestExpansion_BulkPayoutBatchProcessing(t *testing.T) {
	db, ledgerSvc := setupTestDB(t)
	ctx := context.Background()

	repo := repository.NewRepository(db)
	standingEngine := service.NewStandingOrderEngine(repo, ledgerSvc)

	userID := uuid.New()
	corporateAcc, _ := ledgerSvc.CreateAccount(ctx, "Corporate Payroll Vault", domain.AccountTypeAsset, "USD", "corp")

	emp1, _ := ledgerSvc.CreateAccount(ctx, "Employee 1", domain.AccountTypeLiability, "USD", "emp1")
	emp2, _ := ledgerSvc.CreateAccount(ctx, "Employee 2", domain.AccountTypeLiability, "USD", "emp2")

	batch := &domain.BulkPayoutBatch{
		UserID:          userID,
		SourceAccountID: corporateAcc.AccountID,
		Title:           "September 2026 Payroll Batch",
		Items: []domain.BulkPayoutItem{
			{DestinationAccountID: emp1.AccountID, AmountCents: 250000}, // $2,500
			{DestinationAccountID: emp2.AccountID, AmountCents: 300000}, // $3,000
		},
	}

	if err := standingEngine.SubmitBulkPayout(ctx, batch); err != nil {
		t.Fatalf("SubmitBulkPayout failed: %v", err)
	}

	if batch.TotalCount != 2 {
		t.Errorf("Expected total count 2, got %d", batch.TotalCount)
	}

	if err := standingEngine.ProcessBulkPayoutBatch(ctx, batch.BatchID, "payroll_admin"); err != nil {
		t.Fatalf("ProcessBulkPayoutBatch failed: %v", err)
	}
}
