package service

import (
	"context"
	"fmt"
	"time"

	"core-banking-ledger/internal/domain"
	"core-banking-ledger/internal/repository"

	"github.com/google/uuid"
)

type StandingOrderEngineService interface {
	CreateStandingOrder(ctx context.Context, order *domain.StandingOrder) error
	ProcessDueStandingOrders(ctx context.Context, actor string) (int, error)
	SubmitBulkPayout(ctx context.Context, batch *domain.BulkPayoutBatch) error
	ProcessBulkPayoutBatch(ctx context.Context, batchID uuid.UUID, actor string) error
}

type StandingOrderEngine struct {
	repo          *repository.Repository
	ledgerService LedgerService
}

func NewStandingOrderEngine(repo *repository.Repository, ledgerService LedgerService) *StandingOrderEngine {
	return &StandingOrderEngine{
		repo:          repo,
		ledgerService: ledgerService,
	}
}

func (soe *StandingOrderEngine) CreateStandingOrder(ctx context.Context, order *domain.StandingOrder) error {
	if order.AmountCents <= 0 {
		return domain.ErrInvalidAmount
	}
	if order.SourceAccountID == uuid.Nil || order.DestinationAccountID == uuid.Nil {
		return fmt.Errorf("source and destination account IDs are required")
	}

	order.OrderID = uuid.New()
	order.Status = "active"
	order.CreatedAt = time.Now().UTC()
	if order.NextRunAt.IsZero() {
		order.NextRunAt = order.CreatedAt
	}

	return soe.repo.CreateStandingOrder(ctx, soe.repo.DB(), order)
}

func (soe *StandingOrderEngine) ProcessDueStandingOrders(ctx context.Context, actor string) (int, error) {
	db := soe.repo.DB()
	dueOrders, err := soe.repo.GetDueStandingOrders(ctx, db)
	if err != nil {
		return 0, fmt.Errorf("failed fetching due standing orders: %w", err)
	}

	executedCount := 0
	for _, order := range dueOrders {
		idempotencyKey := fmt.Sprintf("so_%s_%d", order.OrderID.String(), order.NextRunAt.Unix())
		entries := []domain.PostEntryInput{
			{
				AccountID:   order.SourceAccountID,
				Direction:   domain.DirectionDebit,
				AmountCents: order.AmountCents,
			},
			{
				AccountID:   order.DestinationAccountID,
				Direction:   domain.DirectionCredit,
				AmountCents: order.AmountCents,
			},
		}

		desc := fmt.Sprintf("Standing Order Execution: %s", order.Description)
		_, err := soe.ledgerService.PostTransaction(ctx, idempotencyKey, desc, entries, actor)
		now := time.Now().UTC()

		if err != nil {
			// Failed execution - log and continue to next
			continue
		}

		// Calculate next run date
		var nextRun time.Time
		switch order.Frequency {
		case "daily":
			nextRun = order.NextRunAt.AddDate(0, 0, 1)
		case "weekly":
			nextRun = order.NextRunAt.AddDate(0, 0, 7)
		case "monthly":
			nextRun = order.NextRunAt.AddDate(0, 1, 0)
		default:
			nextRun = order.NextRunAt.AddDate(0, 1, 0)
		}

		_ = soe.repo.UpdateStandingOrderNextRun(ctx, db, order.OrderID, nextRun, now)
		executedCount++
	}

	return executedCount, nil
}

func (soe *StandingOrderEngine) SubmitBulkPayout(ctx context.Context, batch *domain.BulkPayoutBatch) error {
	if len(batch.Items) == 0 {
		return fmt.Errorf("bulk payout batch must contain at least one item")
	}

	batch.BatchID = uuid.New()
	batch.Status = "pending"
	batch.CreatedAt = time.Now().UTC()

	var totalAmount int64
	for i := range batch.Items {
		batch.Items[i].ItemID = uuid.New()
		batch.Items[i].BatchID = batch.BatchID
		batch.Items[i].Status = "pending"
		totalAmount += batch.Items[i].AmountCents
	}

	batch.TotalCount = len(batch.Items)
	batch.TotalAmountCents = totalAmount

	return soe.repo.CreateBulkPayoutBatch(ctx, soe.repo.DB(), batch)
}

func (soe *StandingOrderEngine) ProcessBulkPayoutBatch(ctx context.Context, batchID uuid.UUID, actor string) error {
	db := soe.repo.DB()
	batch, err := soe.repo.GetBulkPayoutBatchByID(ctx, db, batchID)
	if err != nil {
		return err
	}

	if err := soe.repo.UpdateBulkPayoutBatchStatus(ctx, db, batchID, "processing"); err != nil {
		return err
	}

	allSuccess := true
	for _, item := range batch.Items {
		idempotencyKey := fmt.Sprintf("bulk_%s_item_%s", batchID.String(), item.ItemID.String())
		entries := []domain.PostEntryInput{
			{
				AccountID:   batch.SourceAccountID,
				Direction:   domain.DirectionDebit,
				AmountCents: item.AmountCents,
			},
			{
				AccountID:   item.DestinationAccountID,
				Direction:   domain.DirectionCredit,
				AmountCents: item.AmountCents,
			},
		}

		tx, err := soe.ledgerService.PostTransaction(ctx, idempotencyKey, fmt.Sprintf("Bulk Payout: %s", batch.Title), entries, actor)
		if err != nil {
			allSuccess = false
			_ = soe.repo.UpdateBulkPayoutItemStatus(ctx, db, item.ItemID, "failed", err.Error(), nil)
		} else {
			_ = soe.repo.UpdateBulkPayoutItemStatus(ctx, db, item.ItemID, "success", "", &tx.TransactionID)
		}
	}

	finalStatus := "completed"
	if !allSuccess {
		finalStatus = "failed"
	}

	return soe.repo.UpdateBulkPayoutBatchStatus(ctx, db, batchID, finalStatus)
}
