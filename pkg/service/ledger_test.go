package service_test

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"sync"
	"testing"
	"time"

	"core-banking-ledger/pkg/domain"
	"core-banking-ledger/pkg/repository"
	"core-banking-ledger/pkg/service"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

var migrateOnce sync.Once

func setupTestDB(t *testing.T) (*sql.DB, *service.Service) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		dbURL = os.Getenv("DATABASE_URL")
	}
	if dbURL == "" {
		dbURL = "postgres://postgres:postgrespassword@localhost:5432/ledger_db?sslmode=disable"
	}

	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		t.Fatalf("Failed to open test database connection: %v", err)
	}

	if err := db.Ping(); err != nil {
		t.Skipf("Skipping integration/DB test because PostgreSQL is not reachable: %v", err)
	}

	migrateOnce.Do(func() {
		schemaBytes, err := os.ReadFile("../../migrations/001_init_schema.sql")
		if err != nil {
			schemaBytes, _ = os.ReadFile("migrations/001_init_schema.sql")
		}
		if len(schemaBytes) > 0 {
			_, _ = db.Exec(string(schemaBytes))
		}
	})

	repo := repository.NewRepository(db)
	svc := service.NewService(repo)
	return db, svc
}

// 1. UNIT TESTS

func TestUnit_BalancedTransactionSucceeds(t *testing.T) {
	_, svc := setupTestDB(t)
	ctx := context.Background()

	// Create Asset account (Cash) and Liability account (Customer Deposit)
	cashAcc, err := svc.CreateAccount(ctx, "Cash Account", domain.AccountTypeAsset, "USD", "test_user")
	if err != nil {
		t.Fatalf("CreateAccount failed: %v", err)
	}

	depositAcc, err := svc.CreateAccount(ctx, "Customer Deposit", domain.AccountTypeLiability, "USD", "test_user")
	if err != nil {
		t.Fatalf("CreateAccount failed: %v", err)
	}

	// Post balanced transaction: $100 (10,000 cents)
	idempotencyKey := fmt.Sprintf("tx_balanced_%s", uuid.New().String())
	entries := []domain.PostEntryInput{
		{AccountID: cashAcc.AccountID, Direction: domain.DirectionDebit, AmountCents: 10000},
		{AccountID: depositAcc.AccountID, Direction: domain.DirectionCredit, AmountCents: 10000},
	}

	tx, err := svc.PostTransaction(ctx, idempotencyKey, "Initial Deposit", entries, "test_user")
	if err != nil {
		t.Fatalf("PostTransaction failed: %v", err)
	}

	if tx == nil || tx.TransactionID == uuid.Nil {
		t.Fatalf("Expected valid transaction response, got nil")
	}

	// Verify balances
	cashBal, err := svc.GetBalance(ctx, cashAcc.AccountID)
	if err != nil {
		t.Fatalf("GetBalance failed: %v", err)
	}
	if cashBal.ComputedBalanceCents != 10000 {
		t.Errorf("Expected cash balance 10000 cents, got %d", cashBal.ComputedBalanceCents)
	}

	depositBal, err := svc.GetBalance(ctx, depositAcc.AccountID)
	if err != nil {
		t.Fatalf("GetBalance failed: %v", err)
	}
	if depositBal.ComputedBalanceCents != 10000 {
		t.Errorf("Expected deposit balance 10000 cents, got %d", depositBal.ComputedBalanceCents)
	}
}

func TestUnit_UnbalancedTransactionRejected(t *testing.T) {
	_, svc := setupTestDB(t)
	ctx := context.Background()

	acc1, _ := svc.CreateAccount(ctx, "Asset 1", domain.AccountTypeAsset, "USD", "test_user")
	acc2, _ := svc.CreateAccount(ctx, "Liability 1", domain.AccountTypeLiability, "USD", "test_user")

	idempotencyKey := fmt.Sprintf("tx_unbalanced_%s", uuid.New().String())
	entries := []domain.PostEntryInput{
		{AccountID: acc1.AccountID, Direction: domain.DirectionDebit, AmountCents: 10000},
		{AccountID: acc2.AccountID, Direction: domain.DirectionCredit, AmountCents: 5000}, // Unbalanced!
	}

	_, err := svc.PostTransaction(ctx, idempotencyKey, "Unbalanced transfer", entries, "test_user")
	if err == nil {
		t.Fatalf("Expected error for unbalanced transaction, got nil")
	}
}

func TestUnit_DuplicateIdempotencyKeyReturnsOriginal(t *testing.T) {
	_, svc := setupTestDB(t)
	ctx := context.Background()

	acc1, _ := svc.CreateAccount(ctx, "Asset Idem", domain.AccountTypeAsset, "USD", "test_user")
	acc2, _ := svc.CreateAccount(ctx, "Revenue Idem", domain.AccountTypeRevenue, "USD", "test_user")

	idempotencyKey := fmt.Sprintf("idem_key_%s", uuid.New().String())
	entries := []domain.PostEntryInput{
		{AccountID: acc1.AccountID, Direction: domain.DirectionDebit, AmountCents: 2500},
		{AccountID: acc2.AccountID, Direction: domain.DirectionCredit, AmountCents: 2500},
	}

	// First Post
	tx1, err := svc.PostTransaction(ctx, idempotencyKey, "Payment 1", entries, "test_user")
	if err != nil {
		t.Fatalf("First PostTransaction failed: %v", err)
	}

	// Duplicate Post with identical idempotency key
	tx2, err := svc.PostTransaction(ctx, idempotencyKey, "Payment 1 Duplicate", entries, "test_user")
	if err != nil {
		t.Fatalf("Duplicate PostTransaction failed: %v", err)
	}

	if tx1.TransactionID != tx2.TransactionID {
		t.Errorf("Expected identical transaction ID %s, got %s", tx1.TransactionID, tx2.TransactionID)
	}

	// Balance should be exactly 2500, not 5000
	bal1, _ := svc.GetBalance(ctx, acc1.AccountID)
	if bal1.ComputedBalanceCents != 2500 {
		t.Errorf("Expected balance 2500 (no double-posting), got %d", bal1.ComputedBalanceCents)
	}
}

func TestUnit_ReversalCorrectlyOffsetsTransaction(t *testing.T) {
	_, svc := setupTestDB(t)
	ctx := context.Background()

	acc1, _ := svc.CreateAccount(ctx, "Rev Account 1", domain.AccountTypeAsset, "USD", "test_user")
	acc2, _ := svc.CreateAccount(ctx, "Rev Account 2", domain.AccountTypeLiability, "USD", "test_user")

	entries := []domain.PostEntryInput{
		{AccountID: acc1.AccountID, Direction: domain.DirectionDebit, AmountCents: 5000},
		{AccountID: acc2.AccountID, Direction: domain.DirectionCredit, AmountCents: 5000},
	}

	tx, err := svc.PostTransaction(ctx, fmt.Sprintf("orig_%s", uuid.New().String()), "Original Tx", entries, "test_user")
	if err != nil {
		t.Fatalf("PostTransaction failed: %v", err)
	}

	// Reverse transaction
	revTx, err := svc.ReverseTransaction(ctx, tx.TransactionID, "Customer requested refund", "test_user")
	if err != nil {
		t.Fatalf("ReverseTransaction failed: %v", err)
	}

	if revTx == nil {
		t.Fatalf("Expected reversal transaction, got nil")
	}

	// Assert balances are back to 0
	bal1, _ := svc.GetBalance(ctx, acc1.AccountID)
	if bal1.ComputedBalanceCents != 0 {
		t.Errorf("Expected acc1 balance 0 after reversal, got %d", bal1.ComputedBalanceCents)
	}

	bal2, _ := svc.GetBalance(ctx, acc2.AccountID)
	if bal2.ComputedBalanceCents != 0 {
		t.Errorf("Expected acc2 balance 0 after reversal, got %d", bal2.ComputedBalanceCents)
	}

	// Attempting second reversal should fail
	_, err = svc.ReverseTransaction(ctx, tx.TransactionID, "Second refund attempt", "test_user")
	if err == nil {
		t.Fatalf("Expected error when reversing already-reversed transaction, got nil")
	}
}

// 2. CONCURRENCY TEST

func TestConcurrency_SimultaneousTransactionsNoLostUpdates(t *testing.T) {
	_, svc := setupTestDB(t)
	ctx := context.Background()

	mainAcc, _ := svc.CreateAccount(ctx, "Main Operating Account", domain.AccountTypeAsset, "USD", "test_user")
	targetAcc, _ := svc.CreateAccount(ctx, "Settlement Pool", domain.AccountTypeLiability, "USD", "test_user")

	const numGoroutines = 30
	const amountPerTx = 100 // 100 cents per transaction

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	errChan := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(index int) {
			defer wg.Done()
			idemKey := fmt.Sprintf("conc_tx_%d_%s", index, uuid.New().String())
			entries := []domain.PostEntryInput{
				{AccountID: mainAcc.AccountID, Direction: domain.DirectionDebit, AmountCents: amountPerTx},
				{AccountID: targetAcc.AccountID, Direction: domain.DirectionCredit, AmountCents: amountPerTx},
			}
			_, err := svc.PostTransaction(ctx, idemKey, fmt.Sprintf("Concurrent Tx %d", index), entries, "concurrent_tester")
			if err != nil {
				errChan <- err
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	for err := range errChan {
		t.Fatalf("Concurrent transaction failed: %v", err)
	}

	expectedBalance := int64(numGoroutines * amountPerTx)

	mainBal, err := svc.GetBalance(ctx, mainAcc.AccountID)
	if err != nil {
		t.Fatalf("GetBalance failed: %v", err)
	}

	if mainBal.ComputedBalanceCents != expectedBalance {
		t.Errorf("CONCURRENCY BUG: Expected balance %d, got %d", expectedBalance, mainBal.ComputedBalanceCents)
	}

	// Run reconciliation check on test accounts
	mismatches, err := svc.ReconcileAllAccounts(ctx)
	if err != nil {
		t.Fatalf("ReconcileAllAccounts failed: %v", err)
	}
	for _, m := range mismatches {
		if m.AccountID == mainAcc.AccountID || m.AccountID == targetAcc.AccountID {
			t.Errorf("RECONCILIATION BUG: Found mismatch for concurrent account %s (%s): diff=%d", m.AccountID, m.AccountName, m.DifferenceCents)
		}
	}
}

// 3. PROPERTY-BASED / FUZZ TEST

func TestProperty_SystemDebitsAlwaysEqualSystemCredits(t *testing.T) {
	db, svc := setupTestDB(t)
	ctx := context.Background()

	// Create pool of 5 accounts
	accounts := make([]*domain.Account, 5)
	accountTypes := []domain.AccountType{domain.AccountTypeAsset, domain.AccountTypeLiability, domain.AccountTypeEquity, domain.AccountTypeRevenue, domain.AccountTypeExpense}

	for i := 0; i < 5; i++ {
		acc, err := svc.CreateAccount(ctx, fmt.Sprintf("Fuzz Acc %d", i), accountTypes[i], "USD", "fuzzer")
		if err != nil {
			t.Fatalf("CreateAccount failed: %v", err)
		}
		accounts[i] = acc
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	const numRandomTx = 50

	for i := 0; i < numRandomTx; i++ {
		acc1Idx := rng.Intn(len(accounts))
		acc2Idx := rng.Intn(len(accounts))
		for acc2Idx == acc1Idx {
			acc2Idx = rng.Intn(len(accounts))
		}

		amountCents := int64(rng.Intn(5000) + 1)
		idemKey := fmt.Sprintf("fuzz_tx_%d_%s", i, uuid.New().String())

		entries := []domain.PostEntryInput{
			{AccountID: accounts[acc1Idx].AccountID, Direction: domain.DirectionDebit, AmountCents: amountCents},
			{AccountID: accounts[acc2Idx].AccountID, Direction: domain.DirectionCredit, AmountCents: amountCents},
		}

		_, err := svc.PostTransaction(ctx, idemKey, fmt.Sprintf("Fuzz Tx %d", i), entries, "fuzzer")
		if err != nil {
			t.Fatalf("Fuzz PostTransaction failed: %v", err)
		}
	}

	// Verify system-wide invariant: sum(all debits) == sum(all credits)
	var totalDebits int64
	var totalCredits int64

	query := `
		SELECT 
			COALESCE(SUM(CASE WHEN direction = 'debit' THEN amount_cents ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN direction = 'credit' THEN amount_cents ELSE 0 END), 0)
		FROM ledger_entries
	`
	if err := db.QueryRowContext(ctx, query).Scan(&totalDebits, &totalCredits); err != nil {
		t.Fatalf("Failed to query total ledger entry debits/credits: %v", err)
	}

	if totalDebits != totalCredits {
		t.Fatalf("SYSTEM INVARIANT VIOLATION: Total system debits (%d) != Total system credits (%d)", totalDebits, totalCredits)
	}
}

// 4. APPEND-ONLY TRIGGER TEST

func TestDatabase_AppendOnlyTriggerPreventsUpdateAndDelete(t *testing.T) {
	db, svc := setupTestDB(t)
	ctx := context.Background()

	acc1, _ := svc.CreateAccount(ctx, "Append Only 1", domain.AccountTypeAsset, "USD", "test_user")
	acc2, _ := svc.CreateAccount(ctx, "Append Only 2", domain.AccountTypeLiability, "USD", "test_user")

	entries := []domain.PostEntryInput{
		{AccountID: acc1.AccountID, Direction: domain.DirectionDebit, AmountCents: 1000},
		{AccountID: acc2.AccountID, Direction: domain.DirectionCredit, AmountCents: 1000},
	}

	tx, err := svc.PostTransaction(ctx, fmt.Sprintf("ao_%s", uuid.New().String()), "Append Only Tx", entries, "test_user")
	if err != nil {
		t.Fatalf("PostTransaction failed: %v", err)
	}

	entryID := tx.Entries[0].EntryID

	// Attempt UPDATE on ledger_entries
	_, err = db.ExecContext(ctx, "UPDATE ledger_entries SET amount_cents = 9999 WHERE entry_id = $1", entryID)
	if err == nil {
		t.Fatalf("EXPECTED TRIGGER FAILURE: UPDATE on ledger_entries succeeded when it should be forbidden!")
	}

	// Attempt DELETE on ledger_entries
	_, err = db.ExecContext(ctx, "DELETE FROM ledger_entries WHERE entry_id = $1", entryID)
	if err == nil {
		t.Fatalf("EXPECTED TRIGGER FAILURE: DELETE on ledger_entries succeeded when it should be forbidden!")
	}
}
