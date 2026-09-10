package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"time"

	gwrepo "core-banking-ledger/internal/gateway/repository"
	"core-banking-ledger/internal/domain"
	coreService "core-banking-ledger/internal/service"

	"github.com/google/uuid"
)

type CardService struct {
	repo        *gwrepo.GatewayRepository
	coreService *coreService.Service
}

func NewCardService(repo *gwrepo.GatewayRepository, coreSvc *coreService.Service) *CardService {
	return &CardService{
		repo:        repo,
		coreService: coreSvc,
	}
}

type IssueCardRequest struct {
	UserID          uuid.UUID `json:"user_id"`
	AccountID       uuid.UUID `json:"account_id"`
	CardBrand       string    `json:"card_brand"` // VISA, MASTERCARD, VERVE
	CardType        string    `json:"card_type"`  // VIRTUAL, PHYSICAL
	CardholderName  string    `json:"cardholder_name"`
	DailyLimitCents int64     `json:"daily_limit_cents"`
}

func (s *CardService) IssueCard(ctx context.Context, req IssueCardRequest) (*gwrepo.PaymentCard, error) {
	if req.UserID == uuid.Nil {
		user, err := s.repo.GetUserByEmail(ctx, s.repo.DB(), "demo@user.com")
		if err == nil && user != nil {
			req.UserID = user.UserID
		}
	}
	if req.AccountID == uuid.Nil {
		var firstAccID uuid.UUID
		err := s.repo.DB().QueryRowContext(ctx, "SELECT account_id FROM accounts LIMIT 1").Scan(&firstAccID)
		if err == nil {
			req.AccountID = firstAccID
		}
	}

	if req.CardBrand == "" {
		req.CardBrand = "VISA"
	}
	if req.CardType == "" {
		req.CardType = "VIRTUAL"
	}
	if req.CardholderName == "" {
		req.CardholderName = "Core Banking Customer"
	}
	if req.DailyLimitCents <= 0 {
		req.DailyLimitCents = 5000000 // Default 50k NGN
	}

	// Generate card number with brand BIN prefix
	var binPrefix string
	switch req.CardBrand {
	case "VISA":
		binPrefix = "4111"
	case "MASTERCARD":
		binPrefix = "5399"
	case "VERVE":
		binPrefix = "5061"
	default:
		binPrefix = "4111"
	}

	randomPart, _ := rand.Int(rand.Reader, big.NewInt(1000000000000))
	cardNumber := fmt.Sprintf("%s%012d", binPrefix, randomPart.Int64())

	cvvNum, _ := rand.Int(rand.Reader, big.NewInt(900))
	cvv := fmt.Sprintf("%03d", cvvNum.Int64()+100)

	now := time.Now().UTC()
	expiryYear := now.Year() + 3

	card := &gwrepo.PaymentCard{
		CardID:            uuid.New(),
		UserID:            req.UserID,
		AccountID:         req.AccountID,
		CardNumber:        cardNumber,
		CardBrand:         req.CardBrand,
		CardType:          req.CardType,
		CardholderName:    req.CardholderName,
		ExpiryMonth:       int(now.Month()),
		ExpiryYear:        expiryYear,
		CVV:               cvv,
		Status:            "ACTIVE",
		DailyLimitCents:   req.DailyLimitCents,
		MonthlyLimitCents: req.DailyLimitCents * 30,
		CreatedAt:         now,
	}

	err := s.repo.CreateCard(ctx, s.repo.DB(), card)
	if err != nil {
		return nil, err
	}

	return card, nil
}

func (s *CardService) GetCardByID(ctx context.Context, cardID uuid.UUID) (*gwrepo.PaymentCard, error) {
	return s.repo.GetCardByID(ctx, s.repo.DB(), cardID)
}

func (s *CardService) GetUserCards(ctx context.Context, userID uuid.UUID) ([]gwrepo.PaymentCard, error) {
	return s.repo.GetCardsByUserID(ctx, s.repo.DB(), userID)
}

func (s *CardService) GetAllCards(ctx context.Context, limit int) ([]gwrepo.PaymentCard, error) {
	return s.repo.GetAllCards(ctx, s.repo.DB(), limit)
}

func (s *CardService) FreezeCard(ctx context.Context, cardID uuid.UUID) error {
	return s.repo.UpdateCardStatus(ctx, s.repo.DB(), cardID, "FROZEN")
}

func (s *CardService) UnfreezeCard(ctx context.Context, cardID uuid.UUID) error {
	return s.repo.UpdateCardStatus(ctx, s.repo.DB(), cardID, "ACTIVE")
}

type SimulatePOSChargeRequest struct {
	CardID       uuid.UUID `json:"card_id"`
	MerchantName string    `json:"merchant_name"`
	AmountCents  int64     `json:"amount_cents"`
}

func (s *CardService) SimulatePOSCharge(ctx context.Context, req SimulatePOSChargeRequest) (*gwrepo.CardTransaction, error) {
	card, err := s.repo.GetCardByID(ctx, s.repo.DB(), req.CardID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	cardTx := &gwrepo.CardTransaction{
		TxID:                 uuid.New(),
		CardID:               card.CardID,
		AccountID:            card.AccountID,
		MerchantName:         req.MerchantName,
		MerchantCategoryCode: "5999",
		AmountCents:          req.AmountCents,
		Currency:             "NGN",
		Status:               "APPROVED",
		CreatedAt:            now,
	}

	if card.Status != "ACTIVE" {
		cardTx.Status = "DECLINED"
		cardTx.DeclineReason = fmt.Sprintf("Card status is %s", card.Status)
		_ = s.repo.CreateCardTransaction(ctx, s.repo.DB(), cardTx)
		return cardTx, fmt.Errorf("card transaction declined: card is %s", card.Status)
	}

	if req.AmountCents > card.DailyLimitCents {
		cardTx.Status = "DECLINED"
		cardTx.DeclineReason = "Exceeds daily card spend limit"
		_ = s.repo.CreateCardTransaction(ctx, s.repo.DB(), cardTx)
		return cardTx, errors.New("card transaction declined: exceeds daily limit")
	}

	// Lookup or use secondary account for double-entry balance (Settlement Pool)
	var settlementAccID uuid.UUID
	_ = s.repo.DB().QueryRowContext(ctx, "SELECT account_id FROM accounts WHERE account_id != $1 LIMIT 1", card.AccountID).Scan(&settlementAccID)
	if settlementAccID == uuid.Nil {
		settlementAccID = card.AccountID
	}

	// Post double-entry transaction using core ledger (Customer Credit decreases customer balance, Settlement Debit increases settlement balance)
	idempotencyKey := fmt.Sprintf("pos-charge-%s-%d", cardTx.TxID.String(), now.UnixNano())
	entries := []domain.PostEntryInput{
		{AccountID: card.AccountID, Direction: domain.DirectionCredit, AmountCents: req.AmountCents},
		{AccountID: settlementAccID, Direction: domain.DirectionDebit, AmountCents: req.AmountCents},
	}

	_, err = s.coreService.PostTransaction(ctx, idempotencyKey, fmt.Sprintf("POS Card Charge: %s", req.MerchantName), entries, "pos_terminal")
	if err != nil {
		cardTx.Status = "DECLINED"
		cardTx.DeclineReason = fmt.Sprintf("Ledger post failed: %v", err)
		_ = s.repo.CreateCardTransaction(ctx, s.repo.DB(), cardTx)
		return cardTx, fmt.Errorf("card charge declined: %v", err)
	}

	_ = s.repo.CreateCardTransaction(ctx, s.repo.DB(), cardTx)
	return cardTx, nil
}
