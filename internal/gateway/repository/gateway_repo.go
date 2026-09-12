package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	gwdomain "core-banking-ledger/internal/gateway/domain"
	"core-banking-ledger/internal/repository"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

type GatewayRepository struct {
	db *sql.DB
}

func NewGatewayRepository(db *sql.DB) *GatewayRepository {
	return &GatewayRepository{db: db}
}

func (r *GatewayRepository) DB() *sql.DB {
	return r.db
}

// User methods

func (r *GatewayRepository) CreateUser(ctx context.Context, exec repository.Executable, user *gwdomain.User) error {
	query := `
		INSERT INTO users (user_id, email, password_hash, role, mfa_secret, mfa_enabled, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`
	mfaSecret := sql.NullString{String: user.MFASecret, Valid: user.MFASecret != ""}
	_, err := exec.ExecContext(ctx, query,
		user.UserID,
		strings.ToLower(strings.TrimSpace(user.Email)),
		user.PasswordHash,
		string(user.Role),
		mfaSecret,
		user.MFAEnabled,
		user.CreatedAt,
		user.UpdatedAt,
	)
	if err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "23505" { // unique_violation
			return gwdomain.ErrUserAlreadyExists
		}
		return err
	}
	return nil
}

func (r *GatewayRepository) GetUserByEmail(ctx context.Context, exec repository.Executable, email string) (*gwdomain.User, error) {
	query := `
		SELECT user_id, email, password_hash, role, COALESCE(mfa_secret, ''), mfa_enabled,
		       failed_login_attempts, locked_until, COALESCE(pin_hash, ''), failed_pin_attempts, pin_locked_until,
		       created_at, updated_at
		FROM users
		WHERE LOWER(email) = LOWER($1)
	`
	user := &gwdomain.User{}
	var roleStr string
	var lockedUntil, pinLockedUntil sql.NullTime
	err := exec.QueryRowContext(ctx, query, strings.TrimSpace(email)).Scan(
		&user.UserID, &user.Email, &user.PasswordHash, &roleStr, &user.MFASecret, &user.MFAEnabled,
		&user.FailedLoginAttempts, &lockedUntil, &user.PINHash, &user.FailedPINAttempts, &pinLockedUntil,
		&user.CreatedAt, &user.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, gwdomain.ErrUserNotFound
		}
		return nil, err
	}
	user.Role = gwdomain.UserRole(roleStr)
	if lockedUntil.Valid {
		user.LockedUntil = &lockedUntil.Time
	}
	if pinLockedUntil.Valid {
		user.PINLockedUntil = &pinLockedUntil.Time
	}
	return user, nil
}

func (r *GatewayRepository) GetUserByID(ctx context.Context, exec repository.Executable, userID uuid.UUID) (*gwdomain.User, error) {
	query := `
		SELECT user_id, email, password_hash, role, COALESCE(mfa_secret, ''), mfa_enabled,
		       failed_login_attempts, locked_until, COALESCE(pin_hash, ''), failed_pin_attempts, pin_locked_until,
		       created_at, updated_at
		FROM users
		WHERE user_id = $1
	`
	user := &gwdomain.User{}
	var roleStr string
	var lockedUntil, pinLockedUntil sql.NullTime
	err := exec.QueryRowContext(ctx, query, userID).Scan(
		&user.UserID, &user.Email, &user.PasswordHash, &roleStr, &user.MFASecret, &user.MFAEnabled,
		&user.FailedLoginAttempts, &lockedUntil, &user.PINHash, &user.FailedPINAttempts, &pinLockedUntil,
		&user.CreatedAt, &user.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, gwdomain.ErrUserNotFound
		}
		return nil, err
	}
	user.Role = gwdomain.UserRole(roleStr)
	if lockedUntil.Valid {
		user.LockedUntil = &lockedUntil.Time
	}
	if pinLockedUntil.Valid {
		user.PINLockedUntil = &pinLockedUntil.Time
	}
	return user, nil
}

func (r *GatewayRepository) UpdateUserMFA(ctx context.Context, exec repository.Executable, userID uuid.UUID, secret string, enabled bool) error {
	query := `
		UPDATE users
		SET mfa_secret = $1, mfa_enabled = $2, updated_at = $3
		WHERE user_id = $4
	`
	mfaSecret := sql.NullString{String: secret, Valid: secret != ""}
	_, err := exec.ExecContext(ctx, query, mfaSecret, enabled, time.Now().UTC(), userID)
	return err
}

// User-Account Ownership methods

func (r *GatewayRepository) LinkUserAccount(ctx context.Context, exec repository.Executable, userID, accountID uuid.UUID, permission string) error {
	query := `
		INSERT INTO user_accounts (user_id, account_id, permission, created_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, account_id) DO NOTHING
	`
	_, err := exec.ExecContext(ctx, query, userID, accountID, permission, time.Now().UTC())
	return err
}

func (r *GatewayRepository) VerifyAccountOwnership(ctx context.Context, exec repository.Executable, userID, accountID uuid.UUID) (bool, error) {
	query := `
		SELECT EXISTS (
			SELECT 1 FROM user_accounts
			WHERE user_id = $1 AND account_id = $2
		)
	`
	var exists bool
	err := exec.QueryRowContext(ctx, query, userID, accountID).Scan(&exists)
	return exists, err
}

// Refresh Token methods

func (r *GatewayRepository) SaveRefreshToken(ctx context.Context, exec repository.Executable, tokenRecord *gwdomain.RefreshTokenRecord) error {
	query := `
		INSERT INTO refresh_tokens (token_id, user_id, token_hash, family_id, is_revoked, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`
	_, err := exec.ExecContext(ctx, query,
		tokenRecord.TokenID, tokenRecord.UserID, tokenRecord.TokenHash, tokenRecord.FamilyID, tokenRecord.IsRevoked, tokenRecord.ExpiresAt, tokenRecord.CreatedAt,
	)
	return err
}

func (r *GatewayRepository) GetRefreshTokenByHash(ctx context.Context, exec repository.Executable, tokenHash string) (*gwdomain.RefreshTokenRecord, error) {
	query := `
		SELECT token_id, user_id, token_hash, family_id, is_revoked, expires_at, created_at
		FROM refresh_tokens
		WHERE token_hash = $1
	`
	rec := &gwdomain.RefreshTokenRecord{}
	err := exec.QueryRowContext(ctx, query, tokenHash).Scan(
		&rec.TokenID, &rec.UserID, &rec.TokenHash, &rec.FamilyID, &rec.IsRevoked, &rec.ExpiresAt, &rec.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, gwdomain.ErrUnauthorized
		}
		return nil, err
	}
	return rec, nil
}

func (r *GatewayRepository) RevokeTokenFamily(ctx context.Context, exec repository.Executable, familyID uuid.UUID) error {
	query := `
		UPDATE refresh_tokens
		SET is_revoked = TRUE
		WHERE family_id = $1
	`
	_, err := exec.ExecContext(ctx, query, familyID)
	return err
}

func (r *GatewayRepository) RevokeRefreshToken(ctx context.Context, exec repository.Executable, tokenID uuid.UUID) error {
	query := `
		UPDATE refresh_tokens
		SET is_revoked = TRUE
		WHERE token_id = $1
	`
	_, err := exec.ExecContext(ctx, query, tokenID)
	return err
}

// Admin Account Status modification

func (r *GatewayRepository) UpdateAccountStatus(ctx context.Context, exec repository.Executable, accountID uuid.UUID, status string) error {
	query := `
		UPDATE accounts
		SET status = $1
		WHERE account_id = $2
	`
	res, err := exec.ExecContext(ctx, query, status, accountID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return gwdomain.ErrUnauthorized
	}
	return nil
}

// API Audit Log methods

func (r *GatewayRepository) CreateAPIAuditLog(ctx context.Context, exec repository.Executable, log *gwdomain.APIAuditRecord) error {
	metaJSON, err := json.Marshal(log.Metadata)
	if err != nil {
		metaJSON = []byte("{}")
	}

	query := `
		INSERT INTO api_audit_logs (user_id, endpoint, method, account_id, ip_address, status_code, outcome, metadata, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`
	_, err = exec.ExecContext(ctx, query,
		log.UserID, log.Endpoint, log.Method, log.AccountID, log.IPAddress, log.StatusCode, log.Outcome, string(metaJSON), log.CreatedAt,
	)
	return err
}

// System Invariant & Reconciliation methods

func (r *GatewayRepository) GetSystemWideTotals(ctx context.Context, exec repository.Executable) (int64, int64, error) {
	query := `
		SELECT 
			COALESCE(SUM(CASE WHEN direction = 'debit' THEN amount_cents ELSE 0 END), 0) AS total_debits,
			COALESCE(SUM(CASE WHEN direction = 'credit' THEN amount_cents ELSE 0 END), 0) AS total_credits
		FROM ledger_entries
	`
	var debits, credits int64
	err := exec.QueryRowContext(ctx, query).Scan(&debits, &credits)
	return debits, credits, err
}

type ReconciliationRunRecord struct {
	RunID           uuid.UUID `json:"run_id"`
	TotalAccounts   int       `json:"total_accounts"`
	MismatchesCount int       `json:"mismatches_count"`
	SystemBalanced  bool      `json:"system_balanced"`
	Discrepancies   string    `json:"discrepancies"`
	TriggeredBy     string    `json:"triggered_by"`
	CreatedAt       time.Time `json:"created_at"`
}

func (r *GatewayRepository) SaveReconciliationRun(ctx context.Context, exec repository.Executable, run *ReconciliationRunRecord) error {
	query := `
		INSERT INTO reconciliation_runs (run_id, total_accounts, mismatches_count, system_balanced, discrepancies, triggered_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`
	_, err := exec.ExecContext(ctx, query, run.RunID, run.TotalAccounts, run.MismatchesCount, run.SystemBalanced, run.Discrepancies, run.TriggeredBy, run.CreatedAt)
	return err
}

func (r *GatewayRepository) GetRecentReconciliationRuns(ctx context.Context, exec repository.Executable, limit int) ([]ReconciliationRunRecord, error) {
	if limit <= 0 {
		limit = 20
	}
	query := `
		SELECT run_id, total_accounts, mismatches_count, system_balanced, COALESCE(discrepancies::text, '[]'), triggered_by, created_at
		FROM reconciliation_runs
		ORDER BY created_at DESC
		LIMIT $1
	`
	rows, err := exec.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []ReconciliationRunRecord
	for rows.Next() {
		var rec ReconciliationRunRecord
		if err := rows.Scan(&rec.RunID, &rec.TotalAccounts, &rec.MismatchesCount, &rec.SystemBalanced, &rec.Discrepancies, &rec.TriggeredBy, &rec.CreatedAt); err != nil {
			return nil, err
		}
		runs = append(runs, rec)
	}
	return runs, rows.Err()
}

// Flagged Transactions methods

type FlaggedTransactionRecord struct {
	FlagID        uuid.UUID  `json:"flag_id"`
	TransactionID uuid.UUID  `json:"transaction_id"`
	AmountCents   int64      `json:"amount_cents"`
	RuleTriggered string     `json:"rule_triggered"`
	Status        string     `json:"status"`
	ReviewedBy    *uuid.UUID `json:"reviewed_by,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

func (r *GatewayRepository) FlagTransaction(ctx context.Context, exec repository.Executable, txID uuid.UUID, amountCents int64, rule string) error {
	query := `
		INSERT INTO flagged_transactions (transaction_id, amount_cents, rule_triggered, status, created_at)
		VALUES ($1, $2, $3, 'pending_review', $4)
		ON CONFLICT DO NOTHING
	`
	_, err := exec.ExecContext(ctx, query, txID, amountCents, rule, time.Now().UTC())
	return err
}

func (r *GatewayRepository) GetFlaggedTransactions(ctx context.Context, exec repository.Executable, status string) ([]FlaggedTransactionRecord, error) {
	query := `
		SELECT flag_id, transaction_id, amount_cents, rule_triggered, status, reviewed_by, created_at
		FROM flagged_transactions
		WHERE ($1 = '' OR status = $1)
		ORDER BY created_at DESC
	`
	rows, err := exec.QueryContext(ctx, query, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []FlaggedTransactionRecord
	for rows.Next() {
		var rec FlaggedTransactionRecord
		if err := rows.Scan(&rec.FlagID, &rec.TransactionID, &rec.AmountCents, &rec.RuleTriggered, &rec.Status, &rec.ReviewedBy, &rec.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, rec)
	}
	return list, rows.Err()
}

func (r *GatewayRepository) ReviewFlaggedTransaction(ctx context.Context, exec repository.Executable, flagID uuid.UUID, status string, reviewerID uuid.UUID) error {
	query := `
		UPDATE flagged_transactions
		SET status = $1, reviewed_by = $2
		WHERE flag_id = $3
	`
	_, err := exec.ExecContext(ctx, query, status, reviewerID, flagID)
	return err
}

// KYC Applications methods

type KYCApplicationRecord struct {
	KYCID           uuid.UUID  `json:"kyc_id"`
	UserID          uuid.UUID  `json:"user_id"`
	FullName        string     `json:"full_name"`
	DOB             string     `json:"dob"`
	IDNumber        string     `json:"id_number"`
	Status          string     `json:"status"`
	ReviewedBy      *uuid.UUID `json:"reviewed_by,omitempty"`
	RejectionReason string     `json:"rejection_reason,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (r *GatewayRepository) CreateKYCApplication(ctx context.Context, exec repository.Executable, app *KYCApplicationRecord) error {
	query := `
		INSERT INTO kyc_applications (kyc_id, user_id, full_name, dob, id_number, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`
	_, err := exec.ExecContext(ctx, query, app.KYCID, app.UserID, app.FullName, app.DOB, app.IDNumber, app.Status, app.CreatedAt, app.UpdatedAt)
	return err
}

func (r *GatewayRepository) GetKYCApplications(ctx context.Context, exec repository.Executable, status string) ([]KYCApplicationRecord, error) {
	query := `
		SELECT kyc_id, user_id, full_name, dob, id_number, status, reviewed_by, COALESCE(rejection_reason, ''), created_at, updated_at
		FROM kyc_applications
		WHERE ($1 = '' OR status = $1)
		ORDER BY created_at DESC
	`
	rows, err := exec.QueryContext(ctx, query, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []KYCApplicationRecord
	for rows.Next() {
		var rec KYCApplicationRecord
		if err := rows.Scan(&rec.KYCID, &rec.UserID, &rec.FullName, &rec.DOB, &rec.IDNumber, &rec.Status, &rec.ReviewedBy, &rec.RejectionReason, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
			return nil, err
		}
		list = append(list, rec)
	}
	return list, rows.Err()
}

func (r *GatewayRepository) ReviewKYCApplication(ctx context.Context, exec repository.Executable, kycID uuid.UUID, status, reason string, reviewerID uuid.UUID) error {
	query := `
		UPDATE kyc_applications
		SET status = $1, rejection_reason = $2, reviewed_by = $3, updated_at = $4
		WHERE kyc_id = $5
	`
	reasonVal := sql.NullString{String: reason, Valid: reason != ""}
	_, err := exec.ExecContext(ctx, query, status, reasonVal, reviewerID, time.Now().UTC(), kycID)
	return err
}

// API Audit Search

func (r *GatewayRepository) SearchAPIAuditLogs(ctx context.Context, exec repository.Executable, userIDStr, endpoint, outcome string, limit, offset int) ([]gwdomain.APIAuditRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	query := `
		SELECT log_id, user_id, endpoint, method, account_id, ip_address, status_code, outcome, created_at
		FROM api_audit_logs
		WHERE ($1 = '' OR user_id = $1::uuid)
		  AND ($2 = '' OR endpoint LIKE '%' || $2 || '%')
		  AND ($3 = '' OR outcome = $3)
		ORDER BY created_at DESC
		LIMIT $4 OFFSET $5
	`
	rows, err := exec.QueryContext(ctx, query, userIDStr, endpoint, outcome, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []gwdomain.APIAuditRecord
	for rows.Next() {
		var l gwdomain.APIAuditRecord
		if err := rows.Scan(&l.LogID, &l.UserID, &l.Endpoint, &l.Method, &l.AccountID, &l.IPAddress, &l.StatusCode, &l.Outcome, &l.CreatedAt); err != nil {
			return nil, err
		}
		logs = append(logs, l)
	}
	return logs, rows.Err()
}

// Auth Enhancements Repo Methods

func (r *GatewayRepository) IncrementFailedLoginAttempts(ctx context.Context, exec repository.Executable, userID uuid.UUID) (int, error) {
	query := `
		UPDATE users
		SET failed_login_attempts = failed_login_attempts + 1, updated_at = NOW()
		WHERE user_id = $1
		RETURNING failed_login_attempts
	`
	var count int
	err := exec.QueryRowContext(ctx, query, userID).Scan(&count)
	return count, err
}

func (r *GatewayRepository) LockAccount(ctx context.Context, exec repository.Executable, userID uuid.UUID, lockDuration time.Duration) error {
	query := `
		UPDATE users
		SET locked_until = $1, updated_at = NOW()
		WHERE user_id = $2
	`
	lockedUntil := time.Now().UTC().Add(lockDuration)
	_, err := exec.ExecContext(ctx, query, lockedUntil, userID)
	return err
}

func (r *GatewayRepository) ResetFailedLoginAttempts(ctx context.Context, exec repository.Executable, userID uuid.UUID) error {
	query := `
		UPDATE users
		SET failed_login_attempts = 0, locked_until = NULL, updated_at = NOW()
		WHERE user_id = $1
	`
	_, err := exec.ExecContext(ctx, query, userID)
	return err
}

func (r *GatewayRepository) UpdatePassword(ctx context.Context, exec repository.Executable, userID uuid.UUID, passwordHash string) error {
	query := `
		UPDATE users
		SET password_hash = $1, updated_at = NOW()
		WHERE user_id = $2
	`
	_, err := exec.ExecContext(ctx, query, passwordHash, userID)
	return err
}

func (r *GatewayRepository) SetTransactionPIN(ctx context.Context, exec repository.Executable, userID uuid.UUID, pinHash string) error {
	query := `
		UPDATE users
		SET pin_hash = $1, failed_pin_attempts = 0, pin_locked_until = NULL, updated_at = NOW()
		WHERE user_id = $2
	`
	_, err := exec.ExecContext(ctx, query, pinHash, userID)
	return err
}

func (r *GatewayRepository) IncrementFailedPINAttempts(ctx context.Context, exec repository.Executable, userID uuid.UUID) (int, error) {
	query := `
		UPDATE users
		SET failed_pin_attempts = failed_pin_attempts + 1, updated_at = NOW()
		WHERE user_id = $1
		RETURNING failed_pin_attempts
	`
	var count int
	err := exec.QueryRowContext(ctx, query, userID).Scan(&count)
	return count, err
}

func (r *GatewayRepository) LockTransactionPIN(ctx context.Context, exec repository.Executable, userID uuid.UUID, lockDuration time.Duration) error {
	query := `
		UPDATE users
		SET pin_locked_until = $1, updated_at = NOW()
		WHERE user_id = $2
	`
	lockedUntil := time.Now().UTC().Add(lockDuration)
	_, err := exec.ExecContext(ctx, query, lockedUntil, userID)
	return err
}

func (r *GatewayRepository) ResetFailedPINAttempts(ctx context.Context, exec repository.Executable, userID uuid.UUID) error {
	query := `
		UPDATE users
		SET failed_pin_attempts = 0, pin_locked_until = NULL, updated_at = NOW()
		WHERE user_id = $1
	`
	_, err := exec.ExecContext(ctx, query, userID)
	return err
}

func (r *GatewayRepository) RevokeUserRefreshTokens(ctx context.Context, exec repository.Executable, userID uuid.UUID) error {
	query := `
		UPDATE refresh_tokens
		SET is_revoked = TRUE
		WHERE user_id = $1
	`
	_, err := exec.ExecContext(ctx, query, userID)
	return err
}

func (r *GatewayRepository) LogAuthEvent(ctx context.Context, exec repository.Executable, event *gwdomain.AuthEventRecord) error {
	query := `
		INSERT INTO auth_events (user_id, email, event_type, ip_address, user_agent, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`
	ip := event.IPAddress
	if ip == "" {
		ip = "127.0.0.1"
	}
	_, err := exec.ExecContext(ctx, query, event.UserID, strings.ToLower(event.Email), event.EventType, ip, event.UserAgent, time.Now().UTC())
	return err
}
