package domain

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// AccountType defines standard accounting classification
type AccountType string

const (
	AccountTypeAsset     AccountType = "asset"
	AccountTypeLiability AccountType = "liability"
	AccountTypeEquity    AccountType = "equity"
	AccountTypeRevenue   AccountType = "revenue"
	AccountTypeExpense   AccountType = "expense"
)

func (at AccountType) IsValid() bool {
	switch at {
	case AccountTypeAsset, AccountTypeLiability, AccountTypeEquity, AccountTypeRevenue, AccountTypeExpense:
		return true
	default:
		return false
	}
}

// AccountStatus defines status of an account
type AccountStatus string

const (
	AccountStatusActive AccountStatus = "active"
	AccountStatusFrozen AccountStatus = "frozen"
	AccountStatusClosed AccountStatus = "closed"
)

func (as AccountStatus) IsValid() bool {
	switch as {
	case AccountStatusActive, AccountStatusFrozen, AccountStatusClosed:
		return true
	default:
		return false
	}
}

// Direction represents debit or credit entry
type Direction string

const (
	DirectionDebit  Direction = "debit"
	DirectionCredit Direction = "credit"
)

func (d Direction) IsValid() bool {
	return d == DirectionDebit || d == DirectionCredit
}

// Account represents an entity in the ledger
type Account struct {
	AccountID   uuid.UUID     `json:"account_id"`
	Name        string        `json:"name"`
	AccountType AccountType   `json:"account_type"`
	Currency    string        `json:"currency"`
	Status      AccountStatus `json:"status"`
	CreatedAt   time.Time     `json:"created_at"`
}

// Transaction represents a financial transaction posting multiple ledger entries
type Transaction struct {
	TransactionID  uuid.UUID     `json:"transaction_id"`
	IdempotencyKey string        `json:"idempotency_key"`
	Description    string        `json:"description"`
	CreatedAt      time.Time     `json:"created_at"`
	ReversedBy     *uuid.UUID    `json:"reversed_by,omitempty"`
	Entries        []LedgerEntry `json:"entries,omitempty"`
}

// LedgerEntry represents an individual debit or credit leg of a transaction
type LedgerEntry struct {
	EntryID       int64     `json:"entry_id"`
	TransactionID uuid.UUID `json:"transaction_id"`
	AccountID     uuid.UUID `json:"account_id"`
	Direction     Direction `json:"direction"`
	AmountCents   int64     `json:"amount_cents"`
	CreatedAt     time.Time `json:"created_at"`
}

// PostEntryInput is input for a single ledger entry leg in PostTransaction
type PostEntryInput struct {
	AccountID   uuid.UUID `json:"account_id"`
	Direction   Direction `json:"direction"`
	AmountCents int64     `json:"amount_cents"`
}

// AuditLog captures audit trail for KYC/AML and operational logging
type AuditLog struct {
	AuditID   int64     `json:"audit_id"`
	Actor     string    `json:"actor"`
	Action    string    `json:"action"`
	Metadata  string    `json:"metadata"` // JSON string
	CreatedAt time.Time `json:"created_at"`
}

// AccountBalance represents balance information
type AccountBalance struct {
	AccountID           uuid.UUID `json:"account_id"`
	AccountName         string    `json:"account_name"`
	AccountType         AccountType `json:"account_type"`
	Currency            string    `json:"currency"`
	ComputedBalanceCents int64    `json:"computed_balance_cents"`
	CachedBalanceCents   int64    `json:"cached_balance_cents"`
	TotalDebitsCents    int64     `json:"total_debits_cents"`
	TotalCreditsCents   int64     `json:"total_credits_cents"`
}

// ReconciliationMismatch flags mismatch between computed entries balance and cached balance
type ReconciliationMismatch struct {
	AccountID           uuid.UUID   `json:"account_id"`
	AccountName         string      `json:"account_name"`
	AccountType         AccountType `json:"account_type"`
	ComputedBalanceCents int64      `json:"computed_balance_cents"`
	CachedBalanceCents   int64      `json:"cached_balance_cents"`
	DifferenceCents     int64       `json:"difference_cents"`
}

// Domain Errors
var (
	ErrAccountNotFound        = errors.New("account not found")
	ErrAccountInactive        = errors.New("account is not active")
	ErrTransactionNotFound    = errors.New("transaction not found")
	ErrTransactionAlreadyReversed = errors.New("transaction is already reversed")
	ErrUnbalancedTransaction  = errors.New("transaction debits do not equal credits")
	ErrInvalidAmount          = errors.New("amount must be greater than zero integer cents")
	ErrInvalidIdempotencyKey  = errors.New("idempotency key is required")
	ErrInvalidAccountType     = errors.New("invalid account type")
	ErrInvalidDirection       = errors.New("invalid direction (must be debit or credit)")
	ErrCurrencyMismatch       = errors.New("all accounts in a transaction must share the same currency")
	ErrMinEntriesRequired     = errors.New("transaction must contain at least 2 entries")
	ErrKYCLimitExceeded       = errors.New("transaction exceeds daily or single transaction limit for KYC tier")
	ErrKYCMaxBalanceExceeded = errors.New("resulting balance exceeds maximum allowed limit for KYC tier")
	ErrLoanNotFound           = errors.New("loan not found")
	ErrLoanAlreadyPaidOff     = errors.New("loan is already fully paid off")
	ErrStandingOrderNotFound  = errors.New("standing order not found")
	ErrBulkPayoutBatchNotFound = errors.New("bulk payout batch not found")
)

// KYCTier defines account limits for a specific tier
type KYCTier struct {
	TierLevel           int    `json:"tier_level"`
	Name                string `json:"name"`
	DailyDebitLimitCents int64  `json:"daily_debit_limit_cents"`
	MaxBalanceCents     int64  `json:"max_balance_cents"`
	SingleTxLimitCents  int64  `json:"single_tx_limit_cents"`
}

// UserKYCLevel links a user to their assigned KYC Tier
type UserKYCLevel struct {
	UserID    uuid.UUID `json:"user_id"`
	TierLevel int       `json:"tier_level"`
	Tier      KYCTier   `json:"tier"`
	UpdatedAt time.Time `json:"updated_at"`
}

// InterestProduct defines interest rules for an account product
type InterestProduct struct {
	ProductID          uuid.UUID `json:"product_id"`
	Name               string    `json:"name"`
	AccountType        string    `json:"account_type"`
	AnnualRatePercent  float64   `json:"annual_rate_percent"`
	DayCountConvention string    `json:"day_count_convention"`
	IsActive           bool      `json:"is_active"`
	CreatedAt          time.Time `json:"created_at"`
}

// InterestAccrual stores daily interest calculation records
type InterestAccrual struct {
	AccrualID          int64     `json:"accrual_id"`
	AccountID          uuid.UUID `json:"account_id"`
	ProductID          uuid.UUID `json:"product_id"`
	AccrualDate        time.Time `json:"accrual_date"`
	BalanceCents       int64     `json:"balance_cents"`
	DailyInterestCents int64     `json:"daily_interest_cents"`
	Posted             bool      `json:"posted"`
	CreatedAt          time.Time `json:"created_at"`
}

// Loan represents a credit account / loan record
type Loan struct {
	LoanID                  uuid.UUID `json:"loan_id"`
	UserID                  uuid.UUID `json:"user_id"`
	AccountID               uuid.UUID `json:"account_id"`
	PrincipalCents          int64     `json:"principal_cents"`
	InterestRatePercent     float64   `json:"interest_rate_percent"`
	TermMonths              int       `json:"term_months"`
	MonthlyInstallmentCents int64     `json:"monthly_installment_cents"`
	OutstandingBalanceCents int64     `json:"outstanding_balance_cents"`
	Status                  string    `json:"status"` // active, paid_off, delinquent, defaulted
	DelinquencyBucket       string    `json:"delinquency_bucket"` // current, 30_days, 60_days, 90_days_default
	StartDate               time.Time `json:"start_date"`
	NextPaymentDue          time.Time `json:"next_payment_due"`
}

// LoanSchedule represents an individual monthly loan repayment installment
type LoanSchedule struct {
	ScheduleID        int64     `json:"schedule_id"`
	LoanID            uuid.UUID `json:"loan_id"`
	InstallmentNumber int       `json:"installment_number"`
	DueDate           time.Time `json:"due_date"`
	PrincipalDueCents int64     `json:"principal_due_cents"`
	InterestDueCents  int64     `json:"interest_due_cents"`
	TotalDueCents     int64     `json:"total_due_cents"`
	PaidCents         int64     `json:"paid_cents"`
	Status            string    `json:"status"` // pending, paid, overdue
}

// StandingOrder represents a recurring transfer mandate
type StandingOrder struct {
	OrderID              uuid.UUID  `json:"order_id"`
	UserID               uuid.UUID  `json:"user_id"`
	SourceAccountID      uuid.UUID  `json:"source_account_id"`
	DestinationAccountID uuid.UUID  `json:"destination_account_id"`
	AmountCents          int64      `json:"amount_cents"`
	Description          string     `json:"description"`
	Frequency            string     `json:"frequency"` // daily, weekly, monthly
	Status               string     `json:"status"`    // active, paused, cancelled
	NextRunAt            time.Time  `json:"next_run_at"`
	LastRunAt            *time.Time `json:"last_run_at,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
}

// BulkPayoutBatch represents a bulk payout batch container
type BulkPayoutBatch struct {
	BatchID           uuid.UUID `json:"batch_id"`
	UserID            uuid.UUID `json:"user_id"`
	SourceAccountID   uuid.UUID `json:"source_account_id"`
	Title             string    `json:"title"`
	TotalCount        int       `json:"total_count"`
	TotalAmountCents  int64     `json:"total_amount_cents"`
	Status            string    `json:"status"` // pending, processing, completed, failed
	CreatedAt         time.Time `json:"created_at"`
	Items             []BulkPayoutItem `json:"items,omitempty"`
}

// BulkPayoutItem represents a single item inside a bulk payout batch
type BulkPayoutItem struct {
	ItemID               uuid.UUID  `json:"item_id"`
	BatchID              uuid.UUID  `json:"batch_id"`
	DestinationAccountID uuid.UUID  `json:"destination_account_id"`
	AmountCents          int64      `json:"amount_cents"`
	Status               string     `json:"status"` // pending, success, failed
	ErrorMessage         string     `json:"error_message,omitempty"`
	TransactionID        *uuid.UUID `json:"transaction_id,omitempty"`
}


// CalculateBalance computes the net balance given total debits and total credits according to AccountType convention
func CalculateBalance(accountType AccountType, totalDebitsCents, totalCreditsCents int64) int64 {
	switch accountType {
	case AccountTypeAsset, AccountTypeExpense:
		// Normal balance is DEBIT
		return totalDebitsCents - totalCreditsCents
	case AccountTypeLiability, AccountTypeEquity, AccountTypeRevenue:
		// Normal balance is CREDIT
		return totalCreditsCents - totalDebitsCents
	default:
		return totalDebitsCents - totalCreditsCents
	}
}

// CalculateBalanceDelta returns the net signed change to balance when adding a debit or credit entry
func CalculateBalanceDelta(accountType AccountType, direction Direction, amountCents int64) int64 {
	switch accountType {
	case AccountTypeAsset, AccountTypeExpense:
		if direction == DirectionDebit {
			return amountCents
		}
		return -amountCents
	case AccountTypeLiability, AccountTypeEquity, AccountTypeRevenue:
		if direction == DirectionCredit {
			return amountCents
		}
		return -amountCents
	default:
		if direction == DirectionDebit {
			return amountCents
		}
		return -amountCents
	}
}

// ValidateTransactionEntries checks that entries are valid and balanced to zero
func ValidateTransactionEntries(entries []PostEntryInput) error {
	if len(entries) < 2 {
		return ErrMinEntriesRequired
	}

	var totalDebits int64
	var totalCredits int64

	for i, entry := range entries {
		if entry.AccountID == uuid.Nil {
			return fmt.Errorf("entry [%d]: account_id is required", i)
		}
		if entry.AmountCents <= 0 {
			return fmt.Errorf("entry [%d]: %w", i, ErrInvalidAmount)
		}
		if !entry.Direction.IsValid() {
			return fmt.Errorf("entry [%d]: %w", i, ErrInvalidDirection)
		}

		if entry.Direction == DirectionDebit {
			totalDebits += entry.AmountCents
		} else {
			totalCredits += entry.AmountCents
		}
	}

	if totalDebits != totalCredits {
		return fmt.Errorf("%w: total debits (%d cents) != total credits (%d cents)", ErrUnbalancedTransaction, totalDebits, totalCredits)
	}

	return nil
}
