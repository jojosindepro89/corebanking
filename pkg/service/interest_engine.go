package service

import (
	"context"
	"fmt"
	"time"

	"core-banking-ledger/pkg/domain"
	"core-banking-ledger/pkg/repository"

	"github.com/google/uuid"
)

type InterestEngineService interface {
	CalculateAndSaveDailyAccruals(ctx context.Context, accrualDate time.Time) (int, error)
	PostAccruedInterestBatch(ctx context.Context, expenseAccountID uuid.UUID, actor string) (int, error)
}

type InterestEngine struct {
	repo          *repository.Repository
	ledgerService LedgerService
}

func NewInterestEngine(repo *repository.Repository, ledgerService LedgerService) *InterestEngine {
	return &InterestEngine{
		repo:          repo,
		ledgerService: ledgerService,
	}
}

// CalculateAndSaveDailyAccruals computes daily accrued interest for active savings accounts
func (ie *InterestEngine) CalculateAndSaveDailyAccruals(ctx context.Context, accrualDate time.Time) (int, error) {
	db := ie.repo.DB()

	products, err := ie.repo.GetActiveInterestProducts(ctx, db)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch interest products: %w", err)
	}
	if len(products) == 0 {
		return 0, nil
	}

	accounts, err := ie.repo.GetAllAccounts(ctx, db)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch accounts: %w", err)
	}

	savedCount := 0
	for _, acc := range accounts {
		if acc.Status != domain.AccountStatusActive {
			continue
		}

		// Find matching product by account type
		var matchedProduct *domain.InterestProduct
		for _, p := range products {
			if p.AccountType == string(acc.AccountType) {
				matchedProduct = &p
				break
			}
		}
		if matchedProduct == nil {
			continue
		}

		cachedBal, err := ie.repo.GetCachedBalance(ctx, db, acc.AccountID)
		if err != nil || cachedBal <= 0 {
			continue // No positive balance to accrue interest on
		}

		// 30/360 day-count convention formula: (Balance * AnnualRate / 100) / 360
		dailyInterest := float64(cachedBal) * (matchedProduct.AnnualRatePercent / 100.0) / 360.0
		dailyInterestCents := int64(dailyInterest)
		if dailyInterestCents <= 0 {
			dailyInterestCents = 1 // Minimum 1 cent for active balance if rate yields fraction
		}

		accrual := &domain.InterestAccrual{
			AccountID:          acc.AccountID,
			ProductID:          matchedProduct.ProductID,
			AccrualDate:        accrualDate,
			BalanceCents:       cachedBal,
			DailyInterestCents: dailyInterestCents,
			Posted:             false,
			CreatedAt:          time.Now().UTC(),
		}

		if err := ie.repo.SaveInterestAccrual(ctx, db, accrual); err != nil {
			return savedCount, fmt.Errorf("failed saving accrual for account %s: %w", acc.AccountID, err)
		}
		savedCount++
	}

	return savedCount, nil
}

// PostAccruedInterestBatch posts all unposted interest accruals to the general ledger
func (ie *InterestEngine) PostAccruedInterestBatch(ctx context.Context, expenseAccountID uuid.UUID, actor string) (int, error) {
	db := ie.repo.DB()

	unposted, err := ie.repo.GetUnpostedInterestAccruals(ctx, db)
	if err != nil {
		return 0, fmt.Errorf("failed fetching unposted accruals: %w", err)
	}
	if len(unposted) == 0 {
		return 0, nil
	}

	// Group accrued interest by account_id
	accountTotals := make(map[uuid.UUID]int64)
	accrualIDs := make([]int64, len(unposted))
	for i, a := range unposted {
		accountTotals[a.AccountID] += a.DailyInterestCents
		accrualIDs[i] = a.AccrualID
	}

	postedCount := 0
	for accID, totalInterest := range accountTotals {
		if totalInterest <= 0 {
			continue
		}

		targetAcc, err := ie.ledgerService.GetBalance(ctx, accID)
		if err != nil || targetAcc == nil {
			continue
		}

		expAcc, err := ie.ledgerService.GetBalance(ctx, expenseAccountID)
		if err != nil || expAcc == nil || expAcc.Currency != targetAcc.Currency {
			continue // Skip accounts with currency mismatch relative to the provided expense account
		}

		idempotencyKey := fmt.Sprintf("eom_interest_%s_%d", accID, time.Now().UnixNano())
		entries := []domain.PostEntryInput{
			{
				AccountID:   expenseAccountID, // Bank Interest Expense Account
				Direction:   domain.DirectionDebit,
				AmountCents: totalInterest,
			},
			{
				AccountID:   accID, // Savings Customer Account
				Direction:   domain.DirectionCredit,
				AmountCents: totalInterest,
			},
		}

		_, err = ie.ledgerService.PostTransaction(ctx, idempotencyKey, "End of Month Interest Posting", entries, actor)
		if err != nil {
			return postedCount, fmt.Errorf("failed posting interest transaction for account %s: %w", accID, err)
		}
		postedCount++
	}

	// Mark accruals as posted
	if err := ie.repo.MarkAccrualsAsPosted(ctx, db, accrualIDs); err != nil {
		return postedCount, fmt.Errorf("failed marking accruals as posted: %w", err)
	}

	return postedCount, nil
}
