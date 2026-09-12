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
	if strings.TrimSpace(req.Email) == "" {
		return nil, fmt.Errorf("email cannot be empty")
	}

	if err := s.authSvc.ValidatePasswordStrength(req.Password); err != nil {
		return nil, err
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

	_ = s.repo.LogAuthEvent(ctx, s.repo.DB(), &gwdomain.AuthEventRecord{
		UserID:    &user.UserID,
		Email:     user.Email,
		EventType: "REGISTER_SUCCESS",
	})

	return &gwdomain.UserView{
		UserID:     user.UserID,
		Email:      user.Email,
		Role:       user.Role,
		MFAEnabled: user.MFAEnabled,
		PINSetup:   false,
		IsLocked:   false,
	}, nil
}

func (s *GatewayService) LoginUser(ctx context.Context, req gwdomain.LoginRequest) (*gwdomain.TokenResponse, error) {
	user, err := s.repo.GetUserByEmail(ctx, s.repo.DB(), req.Email)
	if err != nil {
		return nil, gwdomain.ErrInvalidCredentials
	}

	// 1. Account Lockout Check
	if user.LockedUntil != nil && time.Now().UTC().Before(*user.LockedUntil) {
		_ = s.repo.LogAuthEvent(ctx, s.repo.DB(), &gwdomain.AuthEventRecord{
			UserID:    &user.UserID,
			Email:     user.Email,
			EventType: "LOGIN_ATTEMPT_WHILE_LOCKED",
		})
		return nil, gwdomain.ErrAccountLocked
	}

	// 2. Password Check & Failed Attempt Tracking
	if !s.authSvc.CheckPassword(req.Password, user.PasswordHash) {
		count, _ := s.repo.IncrementFailedLoginAttempts(ctx, s.repo.DB(), user.UserID)
		if count >= 5 {
			_ = s.repo.LockAccount(ctx, s.repo.DB(), user.UserID, 15*time.Minute)
			_ = s.repo.LogAuthEvent(ctx, s.repo.DB(), &gwdomain.AuthEventRecord{
				UserID:    &user.UserID,
				Email:     user.Email,
				EventType: "ACCOUNT_LOCKED",
			})
			return nil, gwdomain.ErrAccountLocked
		}
		_ = s.repo.LogAuthEvent(ctx, s.repo.DB(), &gwdomain.AuthEventRecord{
			UserID:    &user.UserID,
			Email:     user.Email,
			EventType: "LOGIN_FAILED",
		})
		return nil, gwdomain.ErrInvalidCredentials
	}

	// Reset failed login attempts on successful password check
	_ = s.repo.ResetFailedLoginAttempts(ctx, s.repo.DB(), user.UserID)

	// 3. MFA Check
	if user.MFAEnabled {
		if req.MFACode == "" {
			return nil, gwdomain.ErrMFARequired
		}
		if !s.authSvc.VerifyMFACode(user.MFASecret, req.MFACode) {
			_ = s.repo.LogAuthEvent(ctx, s.repo.DB(), &gwdomain.AuthEventRecord{
				UserID:    &user.UserID,
				Email:     user.Email,
				EventType: "MFA_FAILED",
			})
			return nil, gwdomain.ErrInvalidMFACode
		}
	}

	_ = s.repo.LogAuthEvent(ctx, s.repo.DB(), &gwdomain.AuthEventRecord{
		UserID:    &user.UserID,
		Email:     user.Email,
		EventType: "LOGIN_SUCCESS",
	})

	return s.issueTokenPair(ctx, user)
}

func (s *GatewayService) LogoutUser(ctx context.Context, userID uuid.UUID, rawRefreshToken string) error {
	if rawRefreshToken != "" {
		tokenHash := s.authSvc.HashRefreshToken(rawRefreshToken)
		record, err := s.repo.GetRefreshTokenByHash(ctx, s.repo.DB(), tokenHash)
		if err == nil && record != nil {
			_ = s.repo.RevokeRefreshToken(ctx, s.repo.DB(), record.TokenID)
		}
	}

	_ = s.repo.LogAuthEvent(ctx, s.repo.DB(), &gwdomain.AuthEventRecord{
		UserID:    &userID,
		EventType: "LOGOUT",
	})

	return nil
}

func (s *GatewayService) RevokeAllSessions(ctx context.Context, userID uuid.UUID) error {
	if err := s.repo.RevokeUserRefreshTokens(ctx, s.repo.DB(), userID); err != nil {
		return err
	}

	_ = s.repo.LogAuthEvent(ctx, s.repo.DB(), &gwdomain.AuthEventRecord{
		UserID:    &userID,
		EventType: "REVOKE_ALL_SESSIONS",
	})

	return nil
}

func (s *GatewayService) ChangePassword(ctx context.Context, userID uuid.UUID, req gwdomain.ChangePasswordRequest) error {
	user, err := s.repo.GetUserByID(ctx, s.repo.DB(), userID)
	if err != nil {
		return err
	}

	if !s.authSvc.CheckPassword(req.OldPassword, user.PasswordHash) {
		return gwdomain.ErrInvalidCredentials
	}

	if err := s.authSvc.ValidatePasswordStrength(req.NewPassword); err != nil {
		return err
	}

	newHash, err := s.authSvc.HashPassword(req.NewPassword)
	if err != nil {
		return err
	}

	if err := s.repo.UpdatePassword(ctx, s.repo.DB(), userID, newHash); err != nil {
		return err
	}

	// Revoke all existing sessions so user must log in again
	_ = s.repo.RevokeUserRefreshTokens(ctx, s.repo.DB(), userID)

	_ = s.repo.LogAuthEvent(ctx, s.repo.DB(), &gwdomain.AuthEventRecord{
		UserID:    &userID,
		Email:     user.Email,
		EventType: "PASSWORD_CHANGED",
	})

	return nil
}

func (s *GatewayService) SetupTransactionPIN(ctx context.Context, userID uuid.UUID, pin string) error {
	pinHash, err := s.authSvc.HashPIN(pin)
	if err != nil {
		return err
	}

	if err := s.repo.SetTransactionPIN(ctx, s.repo.DB(), userID, pinHash); err != nil {
		return err
	}

	_ = s.repo.LogAuthEvent(ctx, s.repo.DB(), &gwdomain.AuthEventRecord{
		UserID:    &userID,
		EventType: "PIN_SETUP",
	})

	return nil
}

func (s *GatewayService) VerifyTransactionPIN(ctx context.Context, userID uuid.UUID, pin string) error {
	user, err := s.repo.GetUserByID(ctx, s.repo.DB(), userID)
	if err != nil {
		return err
	}

	// 1. PIN Lockout check
	if user.PINLockedUntil != nil && time.Now().UTC().Before(*user.PINLockedUntil) {
		_ = s.repo.LogAuthEvent(ctx, s.repo.DB(), &gwdomain.AuthEventRecord{
			UserID:    &userID,
			Email:     user.Email,
			EventType: "PIN_ATTEMPT_WHILE_LOCKED",
		})
		return gwdomain.ErrPINLocked
	}

	// 2. PIN setup check
	if user.PINHash == "" {
		return gwdomain.ErrPINNotSetup
	}

	// 3. PIN verification & failed attempt tracking
	if !s.authSvc.CheckPIN(pin, user.PINHash) {
		count, _ := s.repo.IncrementFailedPINAttempts(ctx, s.repo.DB(), userID)
		if count >= 3 {
			_ = s.repo.LockTransactionPIN(ctx, s.repo.DB(), userID, 1*time.Hour)
			_ = s.repo.LogAuthEvent(ctx, s.repo.DB(), &gwdomain.AuthEventRecord{
				UserID:    &userID,
				Email:     user.Email,
				EventType: "PIN_LOCKED",
			})
			return gwdomain.ErrPINLocked
		}
		_ = s.repo.LogAuthEvent(ctx, s.repo.DB(), &gwdomain.AuthEventRecord{
			UserID:    &userID,
			Email:     user.Email,
			EventType: "PIN_FAILED",
		})
		return gwdomain.ErrInvalidPIN
	}

	// Reset failed PIN attempts
	_ = s.repo.ResetFailedPINAttempts(ctx, s.repo.DB(), userID)

	_ = s.repo.LogAuthEvent(ctx, s.repo.DB(), &gwdomain.AuthEventRecord{
		UserID:    &userID,
		Email:     user.Email,
		EventType: "PIN_VERIFIED",
	})

	return nil
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

	isLocked := user.LockedUntil != nil && time.Now().UTC().Before(*user.LockedUntil)

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
			PINSetup:   user.PINHash != "",
			IsLocked:   isLocked,
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

	isLocked := user.LockedUntil != nil && time.Now().UTC().Before(*user.LockedUntil)

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
			PINSetup:   user.PINHash != "",
			IsLocked:   isLocked,
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
