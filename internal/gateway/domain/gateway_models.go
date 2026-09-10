package domain

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type UserRole string

const (
	RoleUser  UserRole = "user"
	RoleAdmin UserRole = "admin"
)

type User struct {
	UserID       uuid.UUID `json:"user_id"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	Role         UserRole  `json:"role"`
	MFASecret    string    `json:"-"`
	MFAEnabled   bool      `json:"mfa_enabled"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type UserAccount struct {
	UserID     uuid.UUID `json:"user_id"`
	AccountID  uuid.UUID `json:"account_id"`
	Permission string    `json:"permission"` // owner, read, write
	CreatedAt  time.Time `json:"created_at"`
}

type RefreshTokenRecord struct {
	TokenID   uuid.UUID `json:"token_id"`
	UserID    uuid.UUID `json:"user_id"`
	TokenHash string    `json:"-"`
	FamilyID  uuid.UUID `json:"family_id"`
	IsRevoked bool      `json:"is_revoked"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

type APIAuditRecord struct {
	LogID      int64      `json:"log_id"`
	UserID     *uuid.UUID `json:"user_id,omitempty"`
	Endpoint   string     `json:"endpoint"`
	Method     string     `json:"method"`
	AccountID  *uuid.UUID `json:"account_id,omitempty"`
	IPAddress  string     `json:"ip_address"`
	StatusCode int        `json:"status_code"`
	Outcome    string     `json:"outcome"`
	Metadata   any        `json:"metadata,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

type JWTClaims struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Role   string `json:"role"`
	jwt.RegisteredClaims
}

// Custom Gateway Errors
var (
	ErrInvalidCredentials       = errors.New("invalid email or password")
	ErrUserAlreadyExists        = errors.New("user with this email already exists")
	ErrUserNotFound             = errors.New("user not found")
	ErrUnauthorized             = errors.New("unauthorized request")
	ErrForbidden                = errors.New("forbidden: insufficient permissions")
	ErrMFARequired              = errors.New("MFA code required for this action")
	ErrInvalidMFACode           = errors.New("invalid MFA verification code")
	ErrTokenExpired             = errors.New("token has expired")
	ErrTokenRevoked             = errors.New("refresh token has been revoked")
	ErrAccountOwnershipMismatch = errors.New("account does not belong to authenticated user")
	ErrInvalidAmount            = errors.New("invalid monetary amount")
	ErrMissingIdempotencyKey    = errors.New("idempotency key is required")
)

// Request & Response DTOs

type RegisterRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	IsAdmin  bool   `json:"is_admin,omitempty"`
}

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	MFACode  string `json:"mfa_code,omitempty"`
}

type TokenResponse struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	ExpiresIn    int64     `json:"expires_in"` // seconds
	MFAEnabled   bool      `json:"mfa_enabled"`
	User         *UserView `json:"user"`
}

type UserView struct {
	UserID     uuid.UUID `json:"user_id"`
	Email      string    `json:"email"`
	Role       UserRole  `json:"role"`
	MFAEnabled bool      `json:"mfa_enabled"`
}

type MFASetupResponse struct {
	Secret string `json:"secret"`
	QRCode string `json:"qr_code_url"`
}

type MFAVerifyRequest struct {
	Code string `json:"code"`
}

type RefreshTokenRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type TransferRequest struct {
	IdempotencyKey       string `json:"idempotency_key"`
	SourceAccountID      string `json:"source_account_id"`
	DestinationAccountID string `json:"destination_account_id"`
	AmountCents          int64  `json:"amount_cents"`
	Description          string `json:"description"`
}

type CreateGatewayAccountRequest struct {
	Name        string `json:"name"`
	AccountType string `json:"account_type"`
	Currency    string `json:"currency"`
}

type FreezeAccountRequest struct {
	Reason string `json:"reason"`
}

type ReverseTransactionRequest struct {
	Reason string `json:"reason"`
}
