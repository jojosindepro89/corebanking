package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	gwdomain "core-banking-ledger/internal/gateway/domain"
	gwservice "core-banking-ledger/internal/gateway/service"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type GatewayHandler struct {
	svc *gwservice.GatewayService
}

func NewGatewayHandler(svc *gwservice.GatewayService) *GatewayHandler {
	return &GatewayHandler{svc: svc}
}

func respondJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if data != nil {
		_ = json.NewEncoder(w).Encode(data)
	}
}

func getUserIDFromContext(r *http.Request) (uuid.UUID, error) {
	val, ok := r.Context().Value(UserIDKey).(string)
	if !ok || val == "" {
		return uuid.Nil, gwdomain.ErrUnauthorized
	}
	return uuid.Parse(val)
}

func getUserRoleFromContext(r *http.Request) gwdomain.UserRole {
	val, ok := r.Context().Value(RoleKey).(string)
	if !ok || val == "" {
		return gwdomain.RoleUser
	}
	return gwdomain.UserRole(val)
}

func getMFACodeFromContext(r *http.Request) string {
	val, _ := r.Context().Value(MFACodeKey).(string)
	return strings.TrimSpace(val)
}

// 1. Authentication Handlers

func (h *GatewayHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req gwdomain.RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}

	user, err := h.svc.RegisterUser(r.Context(), req)
	if err != nil {
		if errors.Is(err, gwdomain.ErrUserAlreadyExists) {
			respondJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	respondJSON(w, http.StatusCreated, user)
}

func (h *GatewayHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req gwdomain.LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}

	resp, err := h.svc.LoginUser(r.Context(), req)
	if err != nil {
		if errors.Is(err, gwdomain.ErrMFARequired) {
			respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "MFA_REQUIRED", "message": "2FA TOTP code required"})
			return
		}
		if errors.Is(err, gwdomain.ErrInvalidCredentials) || errors.Is(err, gwdomain.ErrInvalidMFACode) {
			respondJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
			return
		}
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "login failed"})
		return
	}

	respondJSON(w, http.StatusOK, resp)
}

func (h *GatewayHandler) SetupMFA(w http.ResponseWriter, r *http.Request) {
	userID, err := getUserIDFromContext(r)
	if err != nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	resp, err := h.svc.SetupMFA(r.Context(), userID)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, resp)
}

func (h *GatewayHandler) VerifyMFA(w http.ResponseWriter, r *http.Request) {
	userID, err := getUserIDFromContext(r)
	if err != nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	var req gwdomain.MFAVerifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}

	if err := h.svc.VerifyMFASetup(r.Context(), userID, req.Code); err != nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "MFA enabled successfully"})
}

func (h *GatewayHandler) RefreshToken(w http.ResponseWriter, r *http.Request) {
	var req gwdomain.RefreshTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}

	resp, err := h.svc.RotateRefreshToken(r.Context(), req.RefreshToken)
	if err != nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, resp)
}

// 2. Account Handlers

func (h *GatewayHandler) CreateAccount(w http.ResponseWriter, r *http.Request) {
	userID, err := getUserIDFromContext(r)
	if err != nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	var req gwdomain.CreateGatewayAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}

	acc, err := h.svc.CreateAccount(r.Context(), userID, req)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	respondJSON(w, http.StatusCreated, acc)
}

func (h *GatewayHandler) GetBalance(w http.ResponseWriter, r *http.Request) {
	userID, err := getUserIDFromContext(r)
	if err != nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	accountIDStr := chi.URLParam(r, "id")
	accountID, err := uuid.Parse(accountIDStr)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid account ID"})
		return
	}

	role := getUserRoleFromContext(r)
	bal, err := h.svc.GetAccountBalance(r.Context(), userID, role, accountID)
	if err != nil {
		if errors.Is(err, gwdomain.ErrAccountOwnershipMismatch) {
			respondJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
			return
		}
		respondJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, bal)
}

func (h *GatewayHandler) GetTransactions(w http.ResponseWriter, r *http.Request) {
	userID, err := getUserIDFromContext(r)
	if err != nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	accountIDStr := chi.URLParam(r, "id")
	accountID, err := uuid.Parse(accountIDStr)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid account ID"})
		return
	}

	limit := 50
	offset := 0
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if parsed, err := strconv.Atoi(o); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	role := getUserRoleFromContext(r)
	entries, err := h.svc.GetAccountTransactions(r.Context(), userID, role, accountID, limit, offset)
	if err != nil {
		if errors.Is(err, gwdomain.ErrAccountOwnershipMismatch) {
			respondJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
			return
		}
		respondJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"account_id": accountID,
		"limit":      limit,
		"offset":     offset,
		"count":      len(entries),
		"entries":    entries,
	})
}

// 3. Money Transfer Handler

func (h *GatewayHandler) Transfer(w http.ResponseWriter, r *http.Request) {
	userID, err := getUserIDFromContext(r)
	if err != nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	var req gwdomain.TransferRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}

	// Read idempotency key from header if not in body
	if req.IdempotencyKey == "" {
		req.IdempotencyKey = r.Header.Get("X-Idempotency-Key")
	}

	mfaCode := getMFACodeFromContext(r)
	tx, err := h.svc.Transfer(r.Context(), userID, req, mfaCode)
	if err != nil {
		if errors.Is(err, gwdomain.ErrAccountOwnershipMismatch) || errors.Is(err, gwdomain.ErrForbidden) {
			respondJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
			return
		}
		if errors.Is(err, gwdomain.ErrMFARequired) || errors.Is(err, gwdomain.ErrInvalidMFACode) {
			respondJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
			return
		}
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, tx)
}

// 4. Admin Handlers

func (h *GatewayHandler) AdminGetAccount(w http.ResponseWriter, r *http.Request) {
	accountIDStr := chi.URLParam(r, "id")
	accountID, err := uuid.Parse(accountIDStr)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid account ID"})
		return
	}

	bal, err := h.svc.AdminGetAccount(r.Context(), accountID)
	if err != nil {
		respondJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, bal)
}

func (h *GatewayHandler) AdminFreezeAccount(w http.ResponseWriter, r *http.Request) {
	accountIDStr := chi.URLParam(r, "id")
	accountID, err := uuid.Parse(accountIDStr)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid account ID"})
		return
	}

	var req gwdomain.FreezeAccountRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	if err := h.svc.AdminFreezeAccount(r.Context(), accountID, req.Reason); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "account frozen successfully", "account_id": accountID.String()})
}

func (h *GatewayHandler) AdminReverseTransaction(w http.ResponseWriter, r *http.Request) {
	adminID, err := getUserIDFromContext(r)
	if err != nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	txIDStr := chi.URLParam(r, "id")
	txID, err := uuid.Parse(txIDStr)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid transaction ID"})
		return
	}

	var req gwdomain.ReverseTransactionRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	tx, err := h.svc.AdminReverseTransaction(r.Context(), txID, req.Reason, adminID)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, tx)
}
