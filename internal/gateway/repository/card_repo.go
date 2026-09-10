package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"core-banking-ledger/internal/repository"

	"github.com/google/uuid"
)

type PaymentCard struct {
	CardID            uuid.UUID `json:"card_id"`
	UserID            uuid.UUID `json:"user_id"`
	AccountID         uuid.UUID `json:"account_id"`
	CardNumber        string    `json:"card_number"`
	CardBrand         string    `json:"card_brand"`
	CardType          string    `json:"card_type"`
	CardholderName    string    `json:"cardholder_name"`
	ExpiryMonth       int       `json:"expiry_month"`
	ExpiryYear        int       `json:"expiry_year"`
	CVV               string    `json:"cvv"`
	PINHash           string    `json:"-"`
	Status            string    `json:"status"`
	DailyLimitCents   int64     `json:"daily_limit_cents"`
	MonthlyLimitCents int64     `json:"monthly_limit_cents"`
	CreatedAt         time.Time `json:"created_at"`
}

type CardTransaction struct {
	TxID                   uuid.UUID `json:"tx_id"`
	CardID                 uuid.UUID `json:"card_id"`
	AccountID              uuid.UUID `json:"account_id"`
	MerchantName           string    `json:"merchant_name"`
	MerchantCategoryCode   string    `json:"merchant_category_code"`
	AmountCents            int64     `json:"amount_cents"`
	Currency               string    `json:"currency"`
	Status                 string    `json:"status"`
	DeclineReason          string    `json:"decline_reason,omitempty"`
	CreatedAt              time.Time `json:"created_at"`
}

func (r *GatewayRepository) CreateCard(ctx context.Context, exec repository.Executable, card *PaymentCard) error {
	query := `
		INSERT INTO payment_cards (card_id, user_id, account_id, card_number, card_brand, card_type, cardholder_name, expiry_month, expiry_year, cvv, pin_hash, status, daily_limit_cents, monthly_limit_cents, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
	`
	pinVal := sql.NullString{String: card.PINHash, Valid: card.PINHash != ""}
	_, err := exec.ExecContext(ctx, query,
		card.CardID, card.UserID, card.AccountID, card.CardNumber, card.CardBrand, card.CardType, card.CardholderName, card.ExpiryMonth, card.ExpiryYear, card.CVV, pinVal, card.Status, card.DailyLimitCents, card.MonthlyLimitCents, card.CreatedAt,
	)
	return err
}

func (r *GatewayRepository) GetCardByID(ctx context.Context, exec repository.Executable, cardID uuid.UUID) (*PaymentCard, error) {
	query := `
		SELECT card_id, user_id, account_id, card_number, card_brand, card_type, cardholder_name, expiry_month, expiry_year, cvv, COALESCE(pin_hash, ''), status, daily_limit_cents, monthly_limit_cents, created_at
		FROM payment_cards
		WHERE card_id = $1
	`
	card := &PaymentCard{}
	err := exec.QueryRowContext(ctx, query, cardID).Scan(
		&card.CardID, &card.UserID, &card.AccountID, &card.CardNumber, &card.CardBrand, &card.CardType, &card.CardholderName, &card.ExpiryMonth, &card.ExpiryYear, &card.CVV, &card.PINHash, &card.Status, &card.DailyLimitCents, &card.MonthlyLimitCents, &card.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("card not found")
		}
		return nil, err
	}
	return card, nil
}

func (r *GatewayRepository) GetCardsByUserID(ctx context.Context, exec repository.Executable, userID uuid.UUID) ([]PaymentCard, error) {
	query := `
		SELECT card_id, user_id, account_id, card_number, card_brand, card_type, cardholder_name, expiry_month, expiry_year, cvv, COALESCE(pin_hash, ''), status, daily_limit_cents, monthly_limit_cents, created_at
		FROM payment_cards
		WHERE user_id = $1
		ORDER BY created_at DESC
	`
	rows, err := exec.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cards []PaymentCard
	for rows.Next() {
		var c PaymentCard
		if err := rows.Scan(&c.CardID, &c.UserID, &c.AccountID, &c.CardNumber, &c.CardBrand, &c.CardType, &c.CardholderName, &c.ExpiryMonth, &c.ExpiryYear, &c.CVV, &c.PINHash, &c.Status, &c.DailyLimitCents, &c.MonthlyLimitCents, &c.CreatedAt); err != nil {
			return nil, err
		}
		cards = append(cards, c)
	}
	return cards, rows.Err()
}

func (r *GatewayRepository) GetAllCards(ctx context.Context, exec repository.Executable, limit int) ([]PaymentCard, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `
		SELECT card_id, user_id, account_id, card_number, card_brand, card_type, cardholder_name, expiry_month, expiry_year, cvv, COALESCE(pin_hash, ''), status, daily_limit_cents, monthly_limit_cents, created_at
		FROM payment_cards
		ORDER BY created_at DESC
		LIMIT $1
	`
	rows, err := exec.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cards []PaymentCard
	for rows.Next() {
		var c PaymentCard
		if err := rows.Scan(&c.CardID, &c.UserID, &c.AccountID, &c.CardNumber, &c.CardBrand, &c.CardType, &c.CardholderName, &c.ExpiryMonth, &c.ExpiryYear, &c.CVV, &c.PINHash, &c.Status, &c.DailyLimitCents, &c.MonthlyLimitCents, &c.CreatedAt); err != nil {
			return nil, err
		}
		cards = append(cards, c)
	}
	return cards, rows.Err()
}

func (r *GatewayRepository) UpdateCardStatus(ctx context.Context, exec repository.Executable, cardID uuid.UUID, status string) error {
	query := `UPDATE payment_cards SET status = $1 WHERE card_id = $2`
	_, err := exec.ExecContext(ctx, query, status, cardID)
	return err
}

func (r *GatewayRepository) UpdateCardLimits(ctx context.Context, exec repository.Executable, cardID uuid.UUID, dailyLimit, monthlyLimit int64) error {
	query := `UPDATE payment_cards SET daily_limit_cents = $1, monthly_limit_cents = $2 WHERE card_id = $3`
	_, err := exec.ExecContext(ctx, query, dailyLimit, monthlyLimit, cardID)
	return err
}

func (r *GatewayRepository) CreateCardTransaction(ctx context.Context, exec repository.Executable, tx *CardTransaction) error {
	query := `
		INSERT INTO card_transactions (tx_id, card_id, account_id, merchant_name, merchant_category_code, amount_cents, currency, status, decline_reason, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`
	reasonVal := sql.NullString{String: tx.DeclineReason, Valid: tx.DeclineReason != ""}
	_, err := exec.ExecContext(ctx, query,
		tx.TxID, tx.CardID, tx.AccountID, tx.MerchantName, tx.MerchantCategoryCode, tx.AmountCents, tx.Currency, tx.Status, reasonVal, tx.CreatedAt,
	)
	return err
}
