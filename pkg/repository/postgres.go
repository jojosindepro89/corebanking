package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"core-banking-ledger/pkg/domain"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// Executable interface abstracts *sql.DB and *sql.Tx
type Executable interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) DB() *sql.DB {
	return r.db
}

// ExecuteInSerializableTx executes the given function within a PostgreSQL SERIALIZABLE transaction.
// It automatically retries on serialization failure (error code 40001) up to maxRetries times with backoff.
func (r *Repository) ExecuteInSerializableTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	const maxRetries = 15
	var err error

	for attempt := 0; attempt < maxRetries; attempt++ {
		err = r.runTxOnce(ctx, fn)
		if err == nil {
			return nil
		}

		if isSerializationFailure(err) {
			// Backoff with exponential increase + jitter: (15ms * 2^attempt) + random(0-50ms)
			backoff := time.Duration(15*(1<<attempt)) * time.Millisecond
			if backoff > 500*time.Millisecond {
				backoff = 500 * time.Millisecond
			}
			jitter := time.Duration(rand.Intn(50)) * time.Millisecond
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff + jitter):
				continue
			}
		}

		// Non-retriable error
		return err
	}

	return fmt.Errorf("transaction failed after %d retries due to serialization failure: %w", maxRetries, err)
}

func (r *Repository) runTxOnce(ctx context.Context, fn func(tx *sql.Tx) error) (err error) {
	opts := &sql.TxOptions{
		Isolation: sql.LevelSerializable,
	}

	tx, err := r.db.BeginTx(ctx, opts)
	if err != nil {
		return err
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		} else if err != nil {
			_ = tx.Rollback()
		}
	}()

	if err = fn(tx); err != nil {
		return err
	}

	return tx.Commit()
}

func isSerializationFailure(err error) bool {
	if err == nil {
		return false
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return pqErr.Code == "40001" || pqErr.Code == "40P01" // 40001 serialization_failure, 40P01 deadlock_detected
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "40001") || strings.Contains(errStr, "40p01") || strings.Contains(errStr, "could not serialize") || strings.Contains(errStr, "deadlock detected")
}

// Account Repository Methods

func (r *Repository) CreateAccount(ctx context.Context, exec Executable, acc *domain.Account) error {
	queryAcc := `
		INSERT INTO accounts (account_id, name, account_type, currency, status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`
	_, err := exec.ExecContext(ctx, queryAcc, acc.AccountID, acc.Name, string(acc.AccountType), acc.Currency, string(acc.Status), acc.CreatedAt)
	if err != nil {
		return err
	}

	queryBal := `
		INSERT INTO account_balances (account_id, cached_balance_cents, updated_at)
		VALUES ($1, 0, $2)
	`
	_, err = exec.ExecContext(ctx, queryBal, acc.AccountID, acc.CreatedAt)
	return err
}

func (r *Repository) GetAccount(ctx context.Context, exec Executable, accountID uuid.UUID) (*domain.Account, error) {
	query := `
		SELECT account_id, name, account_type, currency, status, created_at
		FROM accounts
		WHERE account_id = $1
	`
	acc := &domain.Account{}
	var accTypeStr, statusStr string
	err := exec.QueryRowContext(ctx, query, accountID).Scan(
		&acc.AccountID, &acc.Name, &accTypeStr, &acc.Currency, &statusStr, &acc.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrAccountNotFound
		}
		return nil, err
	}
	acc.AccountType = domain.AccountType(accTypeStr)
	acc.Status = domain.AccountStatus(statusStr)
	return acc, nil
}

func (r *Repository) GetAccountsByIDs(ctx context.Context, exec Executable, ids []uuid.UUID) (map[uuid.UUID]*domain.Account, error) {
	if len(ids) == 0 {
		return make(map[uuid.UUID]*domain.Account), nil
	}

	// Use ANY($1) array query with deterministic sorting to prevent deadlocks
	query := `
		SELECT account_id, name, account_type, currency, status, created_at
		FROM accounts
		WHERE account_id = ANY($1)
		ORDER BY account_id ASC
	`
	rows, err := exec.QueryContext(ctx, query, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[uuid.UUID]*domain.Account)
	for rows.Next() {
		acc := &domain.Account{}
		var accTypeStr, statusStr string
		if err := rows.Scan(&acc.AccountID, &acc.Name, &accTypeStr, &acc.Currency, &statusStr, &acc.CreatedAt); err != nil {
			return nil, err
		}
		acc.AccountType = domain.AccountType(accTypeStr)
		acc.Status = domain.AccountStatus(statusStr)
		result[acc.AccountID] = acc
	}

	return result, rows.Err()
}

// GetAccountsByIDsForUpdate locks account rows deterministically (sorted by account_id) with FOR UPDATE to prevent concurrency race conditions.
func (r *Repository) GetAccountsByIDsForUpdate(ctx context.Context, exec Executable, ids []uuid.UUID) (map[uuid.UUID]*domain.Account, error) {
	if len(ids) == 0 {
		return make(map[uuid.UUID]*domain.Account), nil
	}

	query := `
		SELECT account_id, name, account_type, currency, status, created_at
		FROM accounts
		WHERE account_id = ANY($1)
		ORDER BY account_id ASC
		FOR UPDATE
	`
	rows, err := exec.QueryContext(ctx, query, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[uuid.UUID]*domain.Account)
	for rows.Next() {
		acc := &domain.Account{}
		var accTypeStr, statusStr string
		if err := rows.Scan(&acc.AccountID, &acc.Name, &accTypeStr, &acc.Currency, &statusStr, &acc.CreatedAt); err != nil {
			return nil, err
		}
		acc.AccountType = domain.AccountType(accTypeStr)
		acc.Status = domain.AccountStatus(statusStr)
		result[acc.AccountID] = acc
	}

	return result, rows.Err()
}

func (r *Repository) GetAllAccounts(ctx context.Context, exec Executable) ([]*domain.Account, error) {
	query := `
		SELECT account_id, name, account_type, currency, status, created_at
		FROM accounts
		ORDER BY created_at ASC
	`
	rows, err := exec.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var accounts []*domain.Account
	for rows.Next() {
		acc := &domain.Account{}
		var accTypeStr, statusStr string
		if err := rows.Scan(&acc.AccountID, &acc.Name, &accTypeStr, &acc.Currency, &statusStr, &acc.CreatedAt); err != nil {
			return nil, err
		}
		acc.AccountType = domain.AccountType(accTypeStr)
		acc.Status = domain.AccountStatus(statusStr)
		accounts = append(accounts, acc)
	}

	return accounts, rows.Err()
}

// Transaction & Ledger Entries Methods

func (r *Repository) GetTransactionByIdempotencyKey(ctx context.Context, exec Executable, key string) (*domain.Transaction, error) {
	query := `
		SELECT transaction_id, idempotency_key, description, created_at, reversed_by
		FROM transactions
		WHERE idempotency_key = $1
	`
	tx := &domain.Transaction{}
	err := exec.QueryRowContext(ctx, query, key).Scan(
		&tx.TransactionID, &tx.IdempotencyKey, &tx.Description, &tx.CreatedAt, &tx.ReversedBy,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil // Not found
		}
		return nil, err
	}

	entries, err := r.GetLedgerEntriesByTxID(ctx, exec, tx.TransactionID)
	if err != nil {
		return nil, err
	}
	tx.Entries = entries
	return tx, nil
}

func (r *Repository) GetTransactionByID(ctx context.Context, exec Executable, txID uuid.UUID) (*domain.Transaction, error) {
	query := `
		SELECT transaction_id, idempotency_key, description, created_at, reversed_by
		FROM transactions
		WHERE transaction_id = $1
	`
	tx := &domain.Transaction{}
	err := exec.QueryRowContext(ctx, query, txID).Scan(
		&tx.TransactionID, &tx.IdempotencyKey, &tx.Description, &tx.CreatedAt, &tx.ReversedBy,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrTransactionNotFound
		}
		return nil, err
	}

	entries, err := r.GetLedgerEntriesByTxID(ctx, exec, tx.TransactionID)
	if err != nil {
		return nil, err
	}
	tx.Entries = entries
	return tx, nil
}

func (r *Repository) CreateTransaction(ctx context.Context, exec Executable, tx *domain.Transaction) error {
	query := `
		INSERT INTO transactions (transaction_id, idempotency_key, description, created_at, reversed_by)
		VALUES ($1, $2, $3, $4, $5)
	`
	_, err := exec.ExecContext(ctx, query, tx.TransactionID, tx.IdempotencyKey, tx.Description, tx.CreatedAt, tx.ReversedBy)
	return err
}

func (r *Repository) UpdateTransactionReversedBy(ctx context.Context, exec Executable, txID uuid.UUID, reversedByID uuid.UUID) error {
	query := `
		UPDATE transactions
		SET reversed_by = $1
		WHERE transaction_id = $2
	`
	res, err := exec.ExecContext(ctx, query, reversedByID, txID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return domain.ErrTransactionNotFound
	}
	return nil
}

func (r *Repository) CreateLedgerEntries(ctx context.Context, exec Executable, entries []domain.LedgerEntry) ([]domain.LedgerEntry, error) {
	query := `
		INSERT INTO ledger_entries (transaction_id, account_id, direction, amount_cents, created_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING entry_id
	`
	resultEntries := make([]domain.LedgerEntry, len(entries))
	for i, entry := range entries {
		var entryID int64
		err := exec.QueryRowContext(ctx, query, entry.TransactionID, entry.AccountID, string(entry.Direction), entry.AmountCents, entry.CreatedAt).Scan(&entryID)
		if err != nil {
			return nil, err
		}
		entry.EntryID = entryID
		resultEntries[i] = entry
	}

	return resultEntries, nil
}

func (r *Repository) GetLedgerEntriesByTxID(ctx context.Context, exec Executable, txID uuid.UUID) ([]domain.LedgerEntry, error) {
	query := `
		SELECT entry_id, transaction_id, account_id, direction, amount_cents, created_at
		FROM ledger_entries
		WHERE transaction_id = $1
		ORDER BY entry_id ASC
	`
	rows, err := exec.QueryContext(ctx, query, txID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []domain.LedgerEntry
	for rows.Next() {
		var e domain.LedgerEntry
		var dirStr string
		if err := rows.Scan(&e.EntryID, &e.TransactionID, &e.AccountID, &dirStr, &e.AmountCents, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Direction = domain.Direction(dirStr)
		entries = append(entries, e)
	}

	return entries, rows.Err()
}

func (r *Repository) GetLedgerEntriesByAccountID(ctx context.Context, exec Executable, accountID uuid.UUID, limit, offset int) ([]domain.LedgerEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	query := `
		SELECT entry_id, transaction_id, account_id, direction, amount_cents, created_at
		FROM ledger_entries
		WHERE account_id = $1
		ORDER BY created_at DESC, entry_id DESC
		LIMIT $2 OFFSET $3
	`
	rows, err := exec.QueryContext(ctx, query, accountID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []domain.LedgerEntry
	for rows.Next() {
		var e domain.LedgerEntry
		var dirStr string
		if err := rows.Scan(&e.EntryID, &e.TransactionID, &e.AccountID, &dirStr, &e.AmountCents, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Direction = domain.Direction(dirStr)
		entries = append(entries, e)
	}

	return entries, rows.Err()
}

// Balance and Aggregation Methods

func (r *Repository) GetAccountEntriesSum(ctx context.Context, exec Executable, accountID uuid.UUID) (totalDebitsCents int64, totalCreditsCents int64, err error) {
	query := `
		SELECT 
			COALESCE(SUM(CASE WHEN direction = 'debit' THEN amount_cents ELSE 0 END), 0) AS total_debits,
			COALESCE(SUM(CASE WHEN direction = 'credit' THEN amount_cents ELSE 0 END), 0) AS total_credits
		FROM ledger_entries
		WHERE account_id = $1
	`
	err = exec.QueryRowContext(ctx, query, accountID).Scan(&totalDebitsCents, &totalCreditsCents)
	return totalDebitsCents, totalCreditsCents, err
}

type AccountEntryTotals struct {
	AccountID         uuid.UUID
	TotalDebitsCents  int64
	TotalCreditsCents int64
}

func (r *Repository) GetAllAccountsEntriesSum(ctx context.Context, exec Executable) (map[uuid.UUID]AccountEntryTotals, error) {
	query := `
		SELECT 
			account_id,
			COALESCE(SUM(CASE WHEN direction = 'debit' THEN amount_cents ELSE 0 END), 0) AS total_debits,
			COALESCE(SUM(CASE WHEN direction = 'credit' THEN amount_cents ELSE 0 END), 0) AS total_credits
		FROM ledger_entries
		GROUP BY account_id
	`
	rows, err := exec.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[uuid.UUID]AccountEntryTotals)
	for rows.Next() {
		var t AccountEntryTotals
		if err := rows.Scan(&t.AccountID, &t.TotalDebitsCents, &t.TotalCreditsCents); err != nil {
			return nil, err
		}
		result[t.AccountID] = t
	}

	return result, rows.Err()
}

func (r *Repository) UpdateCachedBalance(ctx context.Context, exec Executable, accountID uuid.UUID, deltaCents int64, updatedAt time.Time) error {
	query := `
		UPDATE account_balances
		SET cached_balance_cents = cached_balance_cents + $1,
		    updated_at = $2
		WHERE account_id = $3
	`
	_, err := exec.ExecContext(ctx, query, deltaCents, updatedAt, accountID)
	return err
}

func (r *Repository) GetCachedBalance(ctx context.Context, exec Executable, accountID uuid.UUID) (int64, error) {
	query := `
		SELECT cached_balance_cents
		FROM account_balances
		WHERE account_id = $1
	`
	var balance int64
	err := exec.QueryRowContext(ctx, query, accountID).Scan(&balance)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, domain.ErrAccountNotFound
		}
		return 0, err
	}
	return balance, nil
}

func (r *Repository) GetAllCachedBalances(ctx context.Context, exec Executable) (map[uuid.UUID]int64, error) {
	query := `
		SELECT account_id, cached_balance_cents
		FROM account_balances
	`
	rows, err := exec.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[uuid.UUID]int64)
	for rows.Next() {
		var id uuid.UUID
		var bal int64
		if err := rows.Scan(&id, &bal); err != nil {
			return nil, err
		}
		result[id] = bal
	}

	return result, rows.Err()
}

// Audit Log Methods

func (r *Repository) CreateAuditLog(ctx context.Context, exec Executable, actor, action string, meta any, createdAt time.Time) error {
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		metaJSON = []byte("{}")
	}

	query := `
		INSERT INTO audit_log (actor, action, metadata, created_at)
		VALUES ($1, $2, $3, $4)
	`
	_, err = exec.ExecContext(ctx, query, actor, action, string(metaJSON), createdAt)
	return err
}

// --- CORE BANKING EXPANSION METHODS ---

// KYC Tier Repository Methods

func (r *Repository) GetKYCTier(ctx context.Context, exec Executable, tierLevel int) (*domain.KYCTier, error) {
	query := `
		SELECT tier_level, name, daily_debit_limit_cents, max_balance_cents, single_tx_limit_cents
		FROM kyc_tiers
		WHERE tier_level = $1
	`
	tier := &domain.KYCTier{}
	err := exec.QueryRowContext(ctx, query, tierLevel).Scan(
		&tier.TierLevel, &tier.Name, &tier.DailyDebitLimitCents, &tier.MaxBalanceCents, &tier.SingleTxLimitCents,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Fallback default tier 1
			return &domain.KYCTier{
				TierLevel: 1, Name: "Tier 1 (Basic)", DailyDebitLimitCents: 5000000, MaxBalanceCents: 30000000, SingleTxLimitCents: 2000000,
			}, nil
		}
		return nil, err
	}
	return tier, nil
}

func (r *Repository) GetUserKYCLevel(ctx context.Context, exec Executable, userID uuid.UUID) (*domain.UserKYCLevel, error) {
	query := `
		SELECT u.user_id, u.tier_level, t.name, t.daily_debit_limit_cents, t.max_balance_cents, t.single_tx_limit_cents, u.updated_at
		FROM user_kyc_levels u
		JOIN kyc_tiers t ON u.tier_level = t.tier_level
		WHERE u.user_id = $1
	`
	uk := &domain.UserKYCLevel{}
	err := exec.QueryRowContext(ctx, query, userID).Scan(
		&uk.UserID, &uk.TierLevel, &uk.Tier.Name, &uk.Tier.DailyDebitLimitCents, &uk.Tier.MaxBalanceCents, &uk.Tier.SingleTxLimitCents, &uk.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Default fallback for user
			t, _ := r.GetKYCTier(ctx, exec, 1)
			return &domain.UserKYCLevel{
				UserID: userID, TierLevel: 1, Tier: *t, UpdatedAt: time.Now(),
			}, nil
		}
		return nil, err
	}
	uk.Tier.TierLevel = uk.TierLevel
	return uk, nil
}

func (r *Repository) SetUserKYCLevel(ctx context.Context, exec Executable, userID uuid.UUID, tierLevel int) error {
	query := `
		INSERT INTO user_kyc_levels (user_id, tier_level, updated_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (user_id) DO UPDATE SET tier_level = EXCLUDED.tier_level, updated_at = NOW()
	`
	_, err := exec.ExecContext(ctx, query, userID, tierLevel)
	return err
}

func (r *Repository) GetUser24HourDebitTotal(ctx context.Context, exec Executable, accountIDs []uuid.UUID) (int64, error) {
	if len(accountIDs) == 0 {
		return 0, nil
	}
	query := `
		SELECT COALESCE(SUM(amount_cents), 0)
		FROM ledger_entries
		WHERE account_id = ANY($1)
		  AND direction = 'debit'
		  AND created_at >= NOW() - INTERVAL '24 hours'
	`
	var total int64
	err := exec.QueryRowContext(ctx, query, pq.Array(accountIDs)).Scan(&total)
	return total, err
}

// Interest Accrual Methods

func (r *Repository) GetActiveInterestProducts(ctx context.Context, exec Executable) ([]domain.InterestProduct, error) {
	query := `
		SELECT product_id, name, account_type, annual_rate_percent, day_count_convention, is_active, created_at
		FROM interest_products
		WHERE is_active = TRUE
	`
	rows, err := exec.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var products []domain.InterestProduct
	for rows.Next() {
		var p domain.InterestProduct
		if err := rows.Scan(&p.ProductID, &p.Name, &p.AccountType, &p.AnnualRatePercent, &p.DayCountConvention, &p.IsActive, &p.CreatedAt); err != nil {
			return nil, err
		}
		products = append(products, p)
	}
	return products, rows.Err()
}

func (r *Repository) SaveInterestAccrual(ctx context.Context, exec Executable, accrual *domain.InterestAccrual) error {
	query := `
		INSERT INTO interest_accruals (account_id, product_id, accrual_date, balance_cents, daily_interest_cents, posted, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (account_id, accrual_date) DO UPDATE
		SET balance_cents = EXCLUDED.balance_cents,
		    daily_interest_cents = EXCLUDED.daily_interest_cents
	`
	_, err := exec.ExecContext(ctx, query, accrual.AccountID, accrual.ProductID, accrual.AccrualDate, accrual.BalanceCents, accrual.DailyInterestCents, accrual.Posted, accrual.CreatedAt)
	return err
}

func (r *Repository) GetUnpostedInterestAccruals(ctx context.Context, exec Executable) ([]domain.InterestAccrual, error) {
	query := `
		SELECT accrual_id, account_id, product_id, accrual_date, balance_cents, daily_interest_cents, posted, created_at
		FROM interest_accruals
		WHERE posted = FALSE
	`
	rows, err := exec.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var accruals []domain.InterestAccrual
	for rows.Next() {
		var a domain.InterestAccrual
		if err := rows.Scan(&a.AccrualID, &a.AccountID, &a.ProductID, &a.AccrualDate, &a.BalanceCents, &a.DailyInterestCents, &a.Posted, &a.CreatedAt); err != nil {
			return nil, err
		}
		accruals = append(accruals, a)
	}
	return accruals, rows.Err()
}

func (r *Repository) MarkAccrualsAsPosted(ctx context.Context, exec Executable, accrualIDs []int64) error {
	if len(accrualIDs) == 0 {
		return nil
	}
	query := `
		UPDATE interest_accruals
		SET posted = TRUE
		WHERE accrual_id = ANY($1)
	`
	_, err := exec.ExecContext(ctx, query, pq.Array(accrualIDs))
	return err
}

// Loan Management Methods

func (r *Repository) CreateLoan(ctx context.Context, exec Executable, loan *domain.Loan) error {
	query := `
		INSERT INTO loans (loan_id, user_id, account_id, principal_cents, interest_rate_percent, term_months, monthly_installment_cents, outstanding_balance_cents, status, delinquency_bucket, start_date, next_payment_due)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`
	_, err := exec.ExecContext(ctx, query, loan.LoanID, loan.UserID, loan.AccountID, loan.PrincipalCents, loan.InterestRatePercent, loan.TermMonths, loan.MonthlyInstallmentCents, loan.OutstandingBalanceCents, loan.Status, loan.DelinquencyBucket, loan.StartDate, loan.NextPaymentDue)
	return err
}

func (r *Repository) CreateLoanSchedules(ctx context.Context, exec Executable, schedules []domain.LoanSchedule) error {
	query := `
		INSERT INTO loan_schedules (loan_id, installment_number, due_date, principal_due_cents, interest_due_cents, total_due_cents, paid_cents, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`
	for _, s := range schedules {
		_, err := exec.ExecContext(ctx, query, s.LoanID, s.InstallmentNumber, s.DueDate, s.PrincipalDueCents, s.InterestDueCents, s.TotalDueCents, s.PaidCents, s.Status)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) GetLoanByID(ctx context.Context, exec Executable, loanID uuid.UUID) (*domain.Loan, error) {
	query := `
		SELECT loan_id, user_id, account_id, principal_cents, interest_rate_percent, term_months, monthly_installment_cents, outstanding_balance_cents, status, delinquency_bucket, start_date, next_payment_due
		FROM loans
		WHERE loan_id = $1
	`
	l := &domain.Loan{}
	err := exec.QueryRowContext(ctx, query, loanID).Scan(
		&l.LoanID, &l.UserID, &l.AccountID, &l.PrincipalCents, &l.InterestRatePercent, &l.TermMonths, &l.MonthlyInstallmentCents, &l.OutstandingBalanceCents, &l.Status, &l.DelinquencyBucket, &l.StartDate, &l.NextPaymentDue,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrLoanNotFound
		}
		return nil, err
	}
	return l, nil
}

func (r *Repository) GetLoanSchedules(ctx context.Context, exec Executable, loanID uuid.UUID) ([]domain.LoanSchedule, error) {
	query := `
		SELECT schedule_id, loan_id, installment_number, due_date, principal_due_cents, interest_due_cents, total_due_cents, paid_cents, status
		FROM loan_schedules
		WHERE loan_id = $1
		ORDER BY installment_number ASC
	`
	rows, err := exec.QueryContext(ctx, query, loanID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var schedules []domain.LoanSchedule
	for rows.Next() {
		var s domain.LoanSchedule
		if err := rows.Scan(&s.ScheduleID, &s.LoanID, &s.InstallmentNumber, &s.DueDate, &s.PrincipalDueCents, &s.InterestDueCents, &s.TotalDueCents, &s.PaidCents, &s.Status); err != nil {
			return nil, err
		}
		schedules = append(schedules, s)
	}
	return schedules, rows.Err()
}

func (r *Repository) UpdateLoanRepayment(ctx context.Context, exec Executable, loanID uuid.UUID, newOutstandingCents int64, newStatus string) error {
	query := `
		UPDATE loans
		SET outstanding_balance_cents = $1, status = $2
		WHERE loan_id = $3
	`
	_, err := exec.ExecContext(ctx, query, newOutstandingCents, newStatus, loanID)
	return err
}

// Standing Orders Methods

func (r *Repository) CreateStandingOrder(ctx context.Context, exec Executable, order *domain.StandingOrder) error {
	query := `
		INSERT INTO standing_orders (order_id, user_id, source_account_id, destination_account_id, amount_cents, description, frequency, status, next_run_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`
	_, err := exec.ExecContext(ctx, query, order.OrderID, order.UserID, order.SourceAccountID, order.DestinationAccountID, order.AmountCents, order.Description, order.Frequency, order.Status, order.NextRunAt, order.CreatedAt)
	return err
}

func (r *Repository) GetDueStandingOrders(ctx context.Context, exec Executable) ([]domain.StandingOrder, error) {
	query := `
		SELECT order_id, user_id, source_account_id, destination_account_id, amount_cents, description, frequency, status, next_run_at, last_run_at, created_at
		FROM standing_orders
		WHERE status = 'active' AND next_run_at <= NOW()
	`
	rows, err := exec.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orders []domain.StandingOrder
	for rows.Next() {
		var o domain.StandingOrder
		if err := rows.Scan(&o.OrderID, &o.UserID, &o.SourceAccountID, &o.DestinationAccountID, &o.AmountCents, &o.Description, &o.Frequency, &o.Status, &o.NextRunAt, &o.LastRunAt, &o.CreatedAt); err != nil {
			return nil, err
		}
		orders = append(orders, o)
	}
	return orders, rows.Err()
}

func (r *Repository) UpdateStandingOrderNextRun(ctx context.Context, exec Executable, orderID uuid.UUID, nextRunAt time.Time, lastRunAt time.Time) error {
	query := `
		UPDATE standing_orders
		SET next_run_at = $1, last_run_at = $2
		WHERE order_id = $3
	`
	_, err := exec.ExecContext(ctx, query, nextRunAt, lastRunAt, orderID)
	return err
}

// Bulk Payout Methods

func (r *Repository) CreateBulkPayoutBatch(ctx context.Context, exec Executable, batch *domain.BulkPayoutBatch) error {
	query := `
		INSERT INTO bulk_payout_batches (batch_id, user_id, source_account_id, title, total_count, total_amount_cents, status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`
	_, err := exec.ExecContext(ctx, query, batch.BatchID, batch.UserID, batch.SourceAccountID, batch.Title, batch.TotalCount, batch.TotalAmountCents, batch.Status, batch.CreatedAt)
	if err != nil {
		return err
	}

	itemQuery := `
		INSERT INTO bulk_payout_items (item_id, batch_id, destination_account_id, amount_cents, status)
		VALUES ($1, $2, $3, $4, $5)
	`
	for _, item := range batch.Items {
		_, err := exec.ExecContext(ctx, itemQuery, item.ItemID, batch.BatchID, item.DestinationAccountID, item.AmountCents, item.Status)
		if err != nil {
			return err
		}
	}

	return nil
}

func (r *Repository) GetBulkPayoutBatchByID(ctx context.Context, exec Executable, batchID uuid.UUID) (*domain.BulkPayoutBatch, error) {
	query := `
		SELECT batch_id, user_id, source_account_id, title, total_count, total_amount_cents, status, created_at
		FROM bulk_payout_batches
		WHERE batch_id = $1
	`
	b := &domain.BulkPayoutBatch{}
	err := exec.QueryRowContext(ctx, query, batchID).Scan(
		&b.BatchID, &b.UserID, &b.SourceAccountID, &b.Title, &b.TotalCount, &b.TotalAmountCents, &b.Status, &b.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, domain.ErrBulkPayoutBatchNotFound
		}
		return nil, err
	}

	itemQuery := `
		SELECT item_id, batch_id, destination_account_id, amount_cents, status, error_message, transaction_id
		FROM bulk_payout_items
		WHERE batch_id = $1
	`
	rows, err := exec.QueryContext(ctx, itemQuery, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var item domain.BulkPayoutItem
		var errMsg sql.NullString
		var txID uuid.NullUUID
		if err := rows.Scan(&item.ItemID, &item.BatchID, &item.DestinationAccountID, &item.AmountCents, &item.Status, &errMsg, &txID); err != nil {
			return nil, err
		}
		if errMsg.Valid {
			item.ErrorMessage = errMsg.String
		}
		if txID.Valid {
			item.TransactionID = &txID.UUID
		}
		b.Items = append(b.Items, item)
	}

	return b, rows.Err()
}

func (r *Repository) UpdateBulkPayoutBatchStatus(ctx context.Context, exec Executable, batchID uuid.UUID, status string) error {
	query := `
		UPDATE bulk_payout_batches
		SET status = $1
		WHERE batch_id = $2
	`
	_, err := exec.ExecContext(ctx, query, status, batchID)
	return err
}

func (r *Repository) UpdateBulkPayoutItemStatus(ctx context.Context, exec Executable, itemID uuid.UUID, status string, errMsg string, txID *uuid.UUID) error {
	query := `
		UPDATE bulk_payout_items
		SET status = $1, error_message = $2, transaction_id = $3
		WHERE item_id = $4
	`
	_, err := exec.ExecContext(ctx, query, status, errMsg, txID, itemID)
	return err
}

