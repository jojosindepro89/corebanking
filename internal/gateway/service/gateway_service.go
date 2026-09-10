package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"core-banking-ledger/internal/domain"
	gwdomain "core-banking-ledger/internal/gateway/domain"
	gwrepo "core-banking-ledger/internal/gateway/repository"
	coreService "core-banking-ledger/internal/service"

	"github.com/google/uuid"
)

type GatewayService struct {
	repo        *gwrepo.GatewayRepository
	coreSvc     *coreService.Service
	authSvc     *AuthService
	refreshTTL  time.Duration
}

func NewGatewayService(repo *gwrepo.GatewayRepository, coreSvc *coreService.Service, authSvc *AuthService, refreshTTL time.Duration) *GatewayService {
	if refreshTTL == 0 {
		refreshTTL = 7 * 24 * time.Hour
	}
	return &GatewayService{
		repo:       repo,
		coreSvc:    coreSvc,
		authSvc:    authSvc,
		refreshTTL: refreshTTL,
	}
}

// User Registration & Authentication

func (s *GatewayService) RegisterUser(ctx context.Context, req gwdomain.RegisterRequest) (*gwdomain.UserView, error) {
	if strings.TrimSpace(req.Email) == "" || len(req.Password) < 8 {
		return nil, fmt.Errorf("invalid registration input: password must be at least 8 characters")
	}

	hash, err := s.authSvc.HashPassword(req.Password)
	if err != nil {
		return nil, err
	}

	role := gwdomain.RoleUser
	if req.IsAdmin {
		role = gwdomain.RoleAdmin
	}

	now := time.Now().UTC()
	user := &gwdomain.User{
		UserID:       uuid.New(),
		Email:        req.Email,
		PasswordHash: hash,
		Role:         role,
		MFAEnabled:   false,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := s.repo.CreateUser(ctx, s.repo.DB(), user); err != nil {
		return nil, err
	}

	return &gwdomain.UserView{
		UserID:     user.UserID,
		Email:      user.Email,
		Role:       user.Role,
		MFAEnabled: user.MFAEnabled,
	}, nil
}

func (s *GatewayService) LoginUser(ctx context.Context, req gwdomain.LoginRequest) (*gwdomain.TokenResponse, error) {
	user, err := s.repo.GetUserByEmail(ctx, s.repo.DB(), req.Email)
	if err != nil {
		return nil, gwdomain.ErrInvalidCredentials
	}

	if !s.authSvc.CheckPassword(req.Password, user.PasswordHash) {
		return nil, gwdomain.ErrInvalidCredentials
	}

	// Verify MFA if user has enabled MFA
	if user.MFAEnabled {
		if req.MFACode == "" {
			return nil, gwdomain.ErrMFARequired
		}
		if !s.authSvc.VerifyMFACode(user.MFASecret, req.MFACode) {
			return nil, gwdomain.ErrInvalidMFACode
		}
	}

	return s.issueTokenPair(ctx, user)
}

func (s *GatewayService) issueTokenPair(ctx context.Context, user *gwdomain.User) (*gwdomain.TokenResponse, error) {
	accessToken, accessTTL, err := s.authSvc.GenerateAccessToken(user)
	if err != nil {
		return nil, err
	}

	rawRefreshToken, tokenHash, err := s.authSvc.GenerateRefreshToken()
	if err != nil {
		return nil, err
	}

	familyID := s.authSvc.GenerateFamilyID()
	now := time.Now().UTC()
	tokenRecord := &gwdomain.RefreshTokenRecord{
		TokenID:   uuid.New(),
		UserID:    user.UserID,
		TokenHash: tokenHash,
		FamilyID:  familyID,
		IsRevoked: false,
		ExpiresAt: now.Add(s.refreshTTL),
		CreatedAt: now,
	}

	if err := s.repo.SaveRefreshToken(ctx, s.repo.DB(), tokenRecord); err != nil {
		return nil, err
	}

	return &gwdomain.TokenResponse{
		AccessToken:  accessToken,
		RefreshToken: rawRefreshToken,
		TokenType:    "Bearer",
		ExpiresIn:    int64(accessTTL.Seconds()),
		MFAEnabled:   user.MFAEnabled,
		User: &gwdomain.UserView{
			UserID:     user.UserID,
			Email:      user.Email,
			Role:       user.Role,
			MFAEnabled: user.MFAEnabled,
		},
	}, nil
}

// Refresh Token Rotation (with Family Revocation on Reuse)

func (s *GatewayService) RotateRefreshToken(ctx context.Context, rawRefreshToken string) (*gwdomain.TokenResponse, error) {
	if rawRefreshToken == "" {
		return nil, gwdomain.ErrUnauthorized
	}

	tokenHash := s.authSvc.HashRefreshToken(rawRefreshToken)
	record, err := s.repo.GetRefreshTokenByHash(ctx, s.repo.DB(), tokenHash)
	if err != nil {
		return nil, gwdomain.ErrUnauthorized
	}

	// Detect reuse of revoked token -> revoke entire token family for security!
	if record.IsRevoked {
		_ = s.repo.RevokeTokenFamily(ctx, s.repo.DB(), record.FamilyID)
		return nil, gwdomain.ErrTokenRevoked
	}

	if time.Now().UTC().After(record.ExpiresAt) {
		return nil, gwdomain.ErrTokenExpired
	}

	// Revoke current token (single use)
	if err := s.repo.RevokeRefreshToken(ctx, s.repo.DB(), record.TokenID); err != nil {
		return nil, err
	}

	// Fetch User
	user, err := s.repo.GetUserByID(ctx, s.repo.DB(), record.UserID)
	if err != nil {
		return nil, err
	}

	// Issue new token pair preserving family ID
	accessToken, accessTTL, err := s.authSvc.GenerateAccessToken(user)
	if err != nil {
		return nil, err
	}

	newRawRefresh, newHash, err := s.authSvc.GenerateRefreshToken()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	newRecord := &gwdomain.RefreshTokenRecord{
		TokenID:   uuid.New(),
		UserID:    user.UserID,
		TokenHash: newHash,
		FamilyID:  record.FamilyID,
		IsRevoked: false,
		ExpiresAt: now.Add(s.refreshTTL),
		CreatedAt: now,
	}

	if err := s.repo.SaveRefreshToken(ctx, s.repo.DB(), newRecord); err != nil {
		return nil, err
	}

	return &gwdomain.TokenResponse{
		AccessToken:  accessToken,
		RefreshToken: newRawRefresh,
		TokenType:    "Bearer",
		ExpiresIn:    int64(accessTTL.Seconds()),
		MFAEnabled:   user.MFAEnabled,
		User: &gwdomain.UserView{
			UserID:     user.UserID,
			Email:      user.Email,
			Role:       user.Role,
			MFAEnabled: user.MFAEnabled,
		},
	}, nil
}

// MFA Setup & Verification

func (s *GatewayService) SetupMFA(ctx context.Context, userID uuid.UUID) (*gwdomain.MFASetupResponse, error) {
	user, err := s.repo.GetUserByID(ctx, s.repo.DB(), userID)
	if err != nil {
		return nil, err
	}

	secret, qrURL, err := s.authSvc.GenerateMFASecret(user.Email)
	if err != nil {
		return nil, err
	}

	if err := s.repo.UpdateUserMFA(ctx, s.repo.DB(), userID, secret, false); err != nil {
		return nil, err
	}

	return &gwdomain.MFASetupResponse{
		Secret: secret,
		QRCode: qrURL,
	}, nil
}

func (s *GatewayService) VerifyMFASetup(ctx context.Context, userID uuid.UUID, code string) error {
	user, err := s.repo.GetUserByID(ctx, s.repo.DB(), userID)
	if err != nil {
		return err
	}

	if user.MFASecret == "" {
		return fmt.Errorf("MFA not setup for user")
	}

	if !s.authSvc.VerifyMFACode(user.MFASecret, code) {
		return gwdomain.ErrInvalidMFACode
	}

	return s.repo.UpdateUserMFA(ctx, s.repo.DB(), userID, user.MFASecret, true)
}

// Account Operations

func (s *GatewayService) CreateAccount(ctx context.Context, userID uuid.UUID, req gwdomain.CreateGatewayAccountRequest) (*domain.Account, error) {
	if req.Name == "" || req.Currency == "" {
		return nil, fmt.Errorf("account name and currency are required")
	}

	accType := domain.AccountType(strings.ToLower(req.AccountType))
	acc, err := s.coreSvc.CreateAccount(ctx, req.Name, accType, req.Currency, userID.String())
	if err != nil {
		return nil, err
	}

	// Link user ownership
	if err := s.repo.LinkUserAccount(ctx, s.repo.DB(), userID, acc.AccountID, "owner"); err != nil {
		return nil, err
	}

	return acc, nil
}

func (s *GatewayService) GetAccountBalance(ctx context.Context, userID uuid.UUID, userRole gwdomain.UserRole, accountID uuid.UUID) (*domain.AccountBalance, error) {
	if userRole != gwdomain.RoleAdmin {
		owned, err := s.repo.VerifyAccountOwnership(ctx, s.repo.DB(), userID, accountID)
		if err != nil {
			return nil, err
		}
		if !owned {
			return nil, gwdomain.ErrAccountOwnershipMismatch
		}
	}

	return s.coreSvc.GetBalance(ctx, accountID)
}

func (s *GatewayService) GetAccountTransactions(ctx context.Context, userID uuid.UUID, userRole gwdomain.UserRole, accountID uuid.UUID, limit, offset int) ([]domain.LedgerEntry, error) {
	if userRole != gwdomain.RoleAdmin {
		owned, err := s.repo.VerifyAccountOwnership(ctx, s.repo.DB(), userID, accountID)
		if err != nil {
			return nil, err
		}
		if !owned {
			return nil, gwdomain.ErrAccountOwnershipMismatch
		}
	}

	return s.coreSvc.GetTransactionHistory(ctx, accountID, limit, offset)
}

// Money Transfer

func (s *GatewayService) Transfer(ctx context.Context, userID uuid.UUID, req gwdomain.TransferRequest, mfaCode string) (*domain.Transaction, error) {
	if req.IdempotencyKey == "" {
		return nil, gwdomain.ErrMissingIdempotencyKey
	}

	if req.AmountCents <= 0 {
		return nil, gwdomain.ErrInvalidAmount
	}

	srcID, err := uuid.Parse(req.SourceAccountID)
	if err != nil {
		return nil, fmt.Errorf("invalid source account ID")
	}

	dstID, err := uuid.Parse(req.DestinationAccountID)
	if err != nil {
		return nil, fmt.Errorf("invalid destination account ID")
	}

	if srcID == dstID {
		return nil, fmt.Errorf("source and destination accounts must be different")
	}

	// Verify source account ownership
	owned, err := s.repo.VerifyAccountOwnership(ctx, s.repo.DB(), userID, srcID)
	if err != nil {
		return nil, err
	}
	if !owned {
		return nil, gwdomain.ErrAccountOwnershipMismatch
	}

	// Check MFA if user enabled MFA
	user, err := s.repo.GetUserByID(ctx, s.repo.DB(), userID)
	if err != nil {
		return nil, err
	}

	if user.MFAEnabled {
		if mfaCode == "" {
			return nil, gwdomain.ErrMFARequired
		}
		if !s.authSvc.VerifyMFACode(user.MFASecret, mfaCode) {
			return nil, gwdomain.ErrInvalidMFACode
		}
	}

	// Build double-entry balanced entries: Credit Source (decreases source asset balance), Debit Destination
	entries := []domain.PostEntryInput{
		{AccountID: srcID, Direction: domain.DirectionCredit, AmountCents: req.AmountCents},
		{AccountID: dstID, Direction: domain.DirectionDebit, AmountCents: req.AmountCents},
	}

	description := req.Description
	if description == "" {
		description = fmt.Sprintf("Transfer from %s to %s", srcID.String(), dstID.String())
	}

	tx, err := s.coreSvc.PostTransaction(ctx, req.IdempotencyKey, description, entries, userID.String())
	if err == nil && tx != nil && req.AmountCents >= 1000000 { // $10,000 threshold
		_ = s.repo.FlagTransaction(ctx, s.repo.DB(), tx.TransactionID, req.AmountCents, "High-value transaction >= $10,000.00")
	}
	return tx, err
}

// Admin Operations

func (s *GatewayService) AdminGetAccount(ctx context.Context, accountID uuid.UUID) (*domain.AccountBalance, error) {
	return s.coreSvc.GetBalance(ctx, accountID)
}

func (s *GatewayService) AdminFreezeAccount(ctx context.Context, accountID uuid.UUID, reason string) error {
	return s.repo.UpdateAccountStatus(ctx, s.repo.DB(), accountID, string(domain.AccountStatusFrozen))
}

func (s *GatewayService) AdminReverseTransaction(ctx context.Context, txID uuid.UUID, reason string, adminID uuid.UUID) (*domain.Transaction, error) {
	return s.coreSvc.ReverseTransaction(ctx, txID, reason, adminID.String())
}
