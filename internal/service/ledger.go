package service

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"core-banking-ledger/internal/domain"
	"core-banking-ledger/internal/repository"

	"github.com/google/uuid"
)

type LedgerService interface {
	CreateAccount(ctx context.Context, name string, accountType domain.AccountType, currency string, actor string) (*domain.Account, error)
	PostTransaction(ctx context.Context, idempotencyKey string, description string, entries []domain.PostEntryInput, actor string) (*domain.Transaction, error)
	GetBalance(ctx context.Context, accountID uuid.UUID) (*domain.AccountBalance, error)
	GetTransactionHistory(ctx context.Context, accountID uuid.UUID, limit, offset int) ([]domain.LedgerEntry, error)
	ReverseTransaction(ctx context.Context, transactionID uuid.UUID, reason string, actor string) (*domain.Transaction, error)
	ReconcileAllAccounts(ctx context.Context) ([]domain.ReconciliationMismatch, error)
}

type Service struct {
	repo *repository.Repository
}

func NewService(repo *repository.Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) CreateAccount(ctx context.Context, name string, accountType domain.AccountType, currency string, actor string) (*domain.Account, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("account name cannot be empty")
	}
	if !accountType.IsValid() {
		return nil, domain.ErrInvalidAccountType
	}
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if len(currency) != 3 {
		return nil, fmt.Errorf("currency must be a 3-letter ISO code (e.g. USD, EUR)")
	}
	if strings.TrimSpace(actor) == "" {
		actor = "system"
	}

	acc := &domain.Account{
		AccountID:   uuid.New(),
		Name:        name,
		AccountType: accountType,
		Currency:    currency,
		Status:      domain.AccountStatusActive,
		CreatedAt:   time.Now().UTC(),
	}

	err := s.repo.ExecuteInSerializableTx(ctx, func(tx *sql.Tx) error {
		if err := s.repo.CreateAccount(ctx, tx, acc); err != nil {
			return err
		}
		return s.repo.CreateAuditLog(ctx, tx, actor, "CREATE_ACCOUNT", map[string]any{
			"account_id":   acc.AccountID,
			"name":         acc.Name,
			"account_type": acc.AccountType,
			"currency":     acc.Currency,
		}, acc.CreatedAt)
	})

	if err != nil {
		return nil, err
	}

	return acc, nil
}

func (s *Service) PostTransaction(ctx context.Context, idempotencyKey string, description string, entries []domain.PostEntryInput, actor string) (*domain.Transaction, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		return nil, domain.ErrInvalidIdempotencyKey
	}
	description = strings.TrimSpace(description)
	if description == "" {
		return nil, fmt.Errorf("description is required")
	}
	if strings.TrimSpace(actor) == "" {
		actor = "system"
	}

	if err := domain.ValidateTransactionEntries(entries); err != nil {
		return nil, err
	}

	var resultTx *domain.Transaction

	err := s.repo.ExecuteInSerializableTx(ctx, func(tx *sql.Tx) error {
		// 1. Idempotency Check: if key exists, return original transaction without double posting
		existingTx, err := s.repo.GetTransactionByIdempotencyKey(ctx, tx, idempotencyKey)
		if err != nil {
			return err
		}
		if existingTx != nil {
			resultTx = existingTx
			return nil
		}

		// 2. Fetch and validate participating accounts
		accountIDsMap := make(map[uuid.UUID]bool)
		var accountIDs []uuid.UUID
		for _, e := range entries {
			if !accountIDsMap[e.AccountID] {
				accountIDsMap[e.AccountID] = true
				accountIDs = append(accountIDs, e.AccountID)
			}
		}

		sort.Slice(accountIDs, func(i, j int) bool {
			return accountIDs[i].String() < accountIDs[j].String()
		})

		accountsMap, err := s.repo.GetAccountsByIDsForUpdate(ctx, tx, accountIDs)
		if err != nil {
			return err
		}

		var transactionCurrency string
		for _, id := range accountIDs {
			acc, exists := accountsMap[id]
			if !exists {
				return fmt.Errorf("%w: ID %s", domain.ErrAccountNotFound, id)
			}
			if acc.Status != domain.AccountStatusActive {
				return fmt.Errorf("%w: account %s (%s) is %s", domain.ErrAccountInactive, acc.Name, id, acc.Status)
			}
			if transactionCurrency == "" {
				transactionCurrency = acc.Currency
			} else if acc.Currency != transactionCurrency {
				return fmt.Errorf("%w: account %s has currency %s, expected %s", domain.ErrCurrencyMismatch, acc.Name, acc.Currency, transactionCurrency)
			}
		}

		now := time.Now().UTC()
		txObj := &domain.Transaction{
			TransactionID:  uuid.New(),
			IdempotencyKey: idempotencyKey,
			Description:    description,
			CreatedAt:      now,
		}

		if err := s.repo.CreateTransaction(ctx, tx, txObj); err != nil {
			return err
		}

		ledgerEntriesInput := make([]domain.LedgerEntry, len(entries))
		for i, e := range entries {
			ledgerEntriesInput[i] = domain.LedgerEntry{
				TransactionID: txObj.TransactionID,
				AccountID:     e.AccountID,
				Direction:     e.Direction,
				AmountCents:   e.AmountCents,
				CreatedAt:     now,
			}
		}

		postedEntries, err := s.repo.CreateLedgerEntries(ctx, tx, ledgerEntriesInput)
		if err != nil {
			return err
		}
		txObj.Entries = postedEntries

		// 3. Update cached balances
		for _, e := range entries {
			acc := accountsMap[e.AccountID]
			delta := domain.CalculateBalanceDelta(acc.AccountType, e.Direction, e.AmountCents)
			if err := s.repo.UpdateCachedBalance(ctx, tx, e.AccountID, delta, now); err != nil {
				return err
			}
		}

		// 4. Audit Log
		if err := s.repo.CreateAuditLog(ctx, tx, actor, "POST_TRANSACTION", map[string]any{
			"transaction_id":  txObj.TransactionID,
			"idempotency_key": txObj.IdempotencyKey,
			"description":     txObj.Description,
			"entries_count":   len(postedEntries),
		}, now); err != nil {
			return err
		}

		resultTx = txObj
		return nil
	})

	if err != nil {
		return nil, err
	}

	return resultTx, nil
}

func (s *Service) GetBalance(ctx context.Context, accountID uuid.UUID) (*domain.AccountBalance, error) {
	if accountID == uuid.Nil {
		return nil, fmt.Errorf("account_id is required")
	}

	db := s.repo.DB()

	// Fetch account metadata
	acc, err := s.repo.GetAccount(ctx, db, accountID)
	if err != nil {
		return nil, err
	}

	// Compute real balance from full ledger entries history (Single Source of Truth)
	totalDebits, totalCredits, err := s.repo.GetAccountEntriesSum(ctx, db, accountID)
	if err != nil {
		return nil, err
	}

	computedBalance := domain.CalculateBalance(acc.AccountType, totalDebits, totalCredits)

	// Fetch cached balance for comparison
	cachedBalance, err := s.repo.GetCachedBalance(ctx, db, accountID)
	if err != nil {
		cachedBalance = 0
	}

	return &domain.AccountBalance{
		AccountID:            acc.AccountID,
		AccountName:          acc.Name,
		AccountType:          acc.AccountType,
		Currency:             acc.Currency,
		ComputedBalanceCents: computedBalance,
		CachedBalanceCents:   cachedBalance,
		TotalDebitsCents:     totalDebits,
		TotalCreditsCents:    totalCredits,
	}, nil
}

func (s *Service) GetTransactionHistory(ctx context.Context, accountID uuid.UUID, limit, offset int) ([]domain.LedgerEntry, error) {
	if accountID == uuid.Nil {
		return nil, fmt.Errorf("account_id is required")
	}

	db := s.repo.DB()
	_, err := s.repo.GetAccount(ctx, db, accountID)
	if err != nil {
		return nil, err
	}

	return s.repo.GetLedgerEntriesByAccountID(ctx, db, accountID, limit, offset)
}

func (s *Service) ReverseTransaction(ctx context.Context, transactionID uuid.UUID, reason string, actor string) (*domain.Transaction, error) {
	if transactionID == uuid.Nil {
		return nil, fmt.Errorf("transaction_id is required")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "Transaction reversal"
	}
	if strings.TrimSpace(actor) == "" {
		actor = "system"
	}

	var reversalTx *domain.Transaction

	err := s.repo.ExecuteInSerializableTx(ctx, func(tx *sql.Tx) error {
		// 1. Fetch original transaction
		origTx, err := s.repo.GetTransactionByID(ctx, tx, transactionID)
		if err != nil {
			return err
		}

		if origTx.ReversedBy != nil {
			return domain.ErrTransactionAlreadyReversed
		}

		// 2. Prepare inverted entries (debts -> credits, credits -> debts)
		reversalEntriesInput := make([]domain.PostEntryInput, len(origTx.Entries))
		accountIDsMap := make(map[uuid.UUID]bool)
		var accountIDs []uuid.UUID

		for i, e := range origTx.Entries {
			var newDir domain.Direction
			if e.Direction == domain.DirectionDebit {
				newDir = domain.DirectionCredit
			} else {
				newDir = domain.DirectionDebit
			}

			reversalEntriesInput[i] = domain.PostEntryInput{
				AccountID:   e.AccountID,
				Direction:   newDir,
				AmountCents: e.AmountCents,
			}

			if !accountIDsMap[e.AccountID] {
				accountIDsMap[e.AccountID] = true
				accountIDs = append(accountIDs, e.AccountID)
			}
		}

		accountsMap, err := s.repo.GetAccountsByIDs(ctx, tx, accountIDs)
		if err != nil {
			return err
		}

		reversalIdempotencyKey := fmt.Sprintf("reversal_%s", origTx.TransactionID.String())
		now := time.Now().UTC()

		revTxObj := &domain.Transaction{
			TransactionID:  uuid.New(),
			IdempotencyKey: reversalIdempotencyKey,
			Description:    fmt.Sprintf("Reversal of %s: %s", origTx.TransactionID.String(), reason),
			CreatedAt:      now,
		}

		if err := s.repo.CreateTransaction(ctx, tx, revTxObj); err != nil {
			return err
		}

		ledgerEntriesInput := make([]domain.LedgerEntry, len(reversalEntriesInput))
		for i, e := range reversalEntriesInput {
			ledgerEntriesInput[i] = domain.LedgerEntry{
				TransactionID: revTxObj.TransactionID,
				AccountID:     e.AccountID,
				Direction:     e.Direction,
				AmountCents:   e.AmountCents,
				CreatedAt:     now,
			}
		}

		postedEntries, err := s.repo.CreateLedgerEntries(ctx, tx, ledgerEntriesInput)
		if err != nil {
			return err
		}
		revTxObj.Entries = postedEntries

		// Update original transaction's reversed_by pointer
		if err := s.repo.UpdateTransactionReversedBy(ctx, tx, origTx.TransactionID, revTxObj.TransactionID); err != nil {
			return err
		}

		// Update cached balances
		for _, e := range reversalEntriesInput {
			acc := accountsMap[e.AccountID]
			delta := domain.CalculateBalanceDelta(acc.AccountType, e.Direction, e.AmountCents)
			if err := s.repo.UpdateCachedBalance(ctx, tx, e.AccountID, delta, now); err != nil {
				return err
			}
		}

		// Audit Log
		if err := s.repo.CreateAuditLog(ctx, tx, actor, "REVERSE_TRANSACTION", map[string]any{
			"original_transaction_id": origTx.TransactionID,
			"reversal_transaction_id": revTxObj.TransactionID,
			"reason":                  reason,
		}, now); err != nil {
			return err
		}

		reversalTx = revTxObj
		return nil
	})

	if err != nil {
		return nil, err
	}

	return reversalTx, nil
}

func (s *Service) ReconcileAllAccounts(ctx context.Context) ([]domain.ReconciliationMismatch, error) {
	db := s.repo.DB()

	// Fetch all accounts
	accounts, err := s.repo.GetAllAccounts(ctx, db)
	if err != nil {
		return nil, err
	}

	// Fetch actual sums from ledger entries (single source of truth)
	entriesSumMap, err := s.repo.GetAllAccountsEntriesSum(ctx, db)
	if err != nil {
		return nil, err
	}

	// Fetch cached balances
	cachedBalancesMap, err := s.repo.GetAllCachedBalances(ctx, db)
	if err != nil {
		return nil, err
	}

	var mismatches []domain.ReconciliationMismatch

	for _, acc := range accounts {
		entryTotals := entriesSumMap[acc.AccountID]
		computedBalance := domain.CalculateBalance(acc.AccountType, entryTotals.TotalDebitsCents, entryTotals.TotalCreditsCents)
		cachedBalance := cachedBalancesMap[acc.AccountID]

		if computedBalance != cachedBalance {
			mismatches = append(mismatches, domain.ReconciliationMismatch{
				AccountID:            acc.AccountID,
				AccountName:          acc.Name,
				AccountType:          acc.AccountType,
				ComputedBalanceCents: computedBalance,
				CachedBalanceCents:   cachedBalance,
				DifferenceCents:     computedBalance - cachedBalance,
			})
		}
	}

	return mismatches, nil
}

func (s *Service) GetAllAccounts(ctx context.Context) ([]*domain.Account, error) {
	return s.repo.GetAllAccounts(ctx, s.repo.DB())
}
