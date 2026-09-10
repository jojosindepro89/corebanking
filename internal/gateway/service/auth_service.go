package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	gwdomain "core-banking-ledger/internal/gateway/domain"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
)

type AuthService struct {
	jwtSecret string
	accessTTL time.Duration
}

func NewAuthService(jwtSecret string, accessTTL time.Duration) *AuthService {
	if accessTTL == 0 {
		accessTTL = 15 * time.Minute
	}
	return &AuthService{
		jwtSecret: jwtSecret,
		accessTTL: accessTTL,
	}
}

// Password handling with bcrypt (Cost: 12)
func (s *AuthService) HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	return string(bytes), err
}

func (s *AuthService) CheckPassword(password, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// Access Token (JWT) management
func (s *AuthService) GenerateAccessToken(user *gwdomain.User) (string, time.Duration, error) {
	now := time.Now().UTC()
	claims := &gwdomain.JWTClaims{
		UserID: user.UserID.String(),
		Email:  user.Email,
		Role:   string(user.Role),
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(s.accessTTL)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Subject:   user.UserID.String(),
			Issuer:    "core-banking-gateway",
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString([]byte(s.jwtSecret))
	return tokenString, s.accessTTL, err
}

func (s *AuthService) ParseAccessToken(tokenString string) (*gwdomain.JWTClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &gwdomain.JWTClaims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, gwdomain.ErrUnauthorized
		}
		return []byte(s.jwtSecret), nil
	})

	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, gwdomain.ErrTokenExpired
		}
		return nil, gwdomain.ErrUnauthorized
	}

	claims, ok := token.Claims.(*gwdomain.JWTClaims)
	if !ok || !token.Valid {
		return nil, gwdomain.ErrUnauthorized
	}

	return claims, nil
}

// Refresh Token generation (Secure 32-byte crypto random string + SHA-256 hash)
func (s *AuthService) GenerateRefreshToken() (rawToken string, tokenHash string, err error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", "", err
	}
	rawToken = hex.EncodeToString(bytes)
	tokenHash = s.HashRefreshToken(rawToken)
	return rawToken, tokenHash, nil
}

func (s *AuthService) HashRefreshToken(rawToken string) string {
	h := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(h[:])
}

// TOTP MFA Management
func (s *AuthService) GenerateMFASecret(email string) (secret string, qrCodeURL string, err error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      "CoreBankingLedger",
		AccountName: email,
	})
	if err != nil {
		return "", "", err
	}
	return key.Secret(), key.URL(), nil
}

func (s *AuthService) VerifyMFACode(secret, code string) bool {
	if secret == "" || code == "" {
		return false
	}
	return totp.Validate(code, secret)
}

func (s *AuthService) GenerateFamilyID() uuid.UUID {
	return uuid.New()
}
