package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"core-banking-ledger/internal/domain"
	"core-banking-ledger/internal/service"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type Handler struct {
	ledgerService service.LedgerService
}

func NewHandler(ledgerService service.LedgerService) *Handler {
	return &Handler{ledgerService: ledgerService}
}

type ErrorResponse struct {
	Error string `json:"error"`
}

func respondJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if payload != nil {
		_ = json.NewEncoder(w).Encode(payload)
	}
}

func respondError(w http.ResponseWriter, status int, message string) {
	respondJSON(w, status, ErrorResponse{Error: message})
}

// CreateAccountHandler handles POST /api/v1/accounts
type CreateAccountRequest struct {
	Name        string             `json:"name"`
	AccountType domain.AccountType `json:"account_type"`
	Currency    string             `json:"currency"`
	Actor       string             `json:"actor,omitempty"`
}

func (h *Handler) CreateAccountHandler(w http.ResponseWriter, r *http.Request) {
	var req CreateAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}

	acc, err := h.ledgerService.CreateAccount(r.Context(), req.Name, req.AccountType, req.Currency, req.Actor)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidAccountType) {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respondJSON(w, http.StatusCreated, acc)
}

// PostTransactionHandler handles POST /api/v1/transactions
type PostTransactionRequest struct {
	IdempotencyKey string                  `json:"idempotency_key"`
	Description    string                  `json:"description"`
	Entries        []domain.PostEntryInput `json:"entries"`
	Actor          string                  `json:"actor,omitempty"`
}

func (h *Handler) PostTransactionHandler(w http.ResponseWriter, r *http.Request) {
	var req PostTransactionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}

	tx, err := h.ledgerService.PostTransaction(r.Context(), req.IdempotencyKey, req.Description, req.Entries, req.Actor)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrUnbalancedTransaction),
			errors.Is(err, domain.ErrInvalidAmount),
			errors.Is(err, domain.ErrInvalidIdempotencyKey),
			errors.Is(err, domain.ErrInvalidDirection),
			errors.Is(err, domain.ErrCurrencyMismatch),
			errors.Is(err, domain.ErrMinEntriesRequired),
			errors.Is(err, domain.ErrAccountInactive):
			respondError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, domain.ErrAccountNotFound):
			respondError(w, http.StatusNotFound, err.Error())
		default:
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	respondJSON(w, http.StatusOK, tx)
}

// GetBalanceHandler handles GET /api/v1/accounts/{id}/balance
func (h *Handler) GetBalanceHandler(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	accountID, err := uuid.Parse(idStr)
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid account_id UUID")
		return
	}

	bal, err := h.ledgerService.GetBalance(r.Context(), accountID)
	if err != nil {
		if errors.Is(err, domain.ErrAccountNotFound) {
			respondError(w, http.StatusNotFound, err.Error())
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, bal)
}

// GetTransactionHistoryHandler handles GET /api/v1/accounts/{id}/history
func (h *Handler) GetTransactionHistoryHandler(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	accountID, err := uuid.Parse(idStr)
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid account_id UUID")
		return
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	entries, err := h.ledgerService.GetTransactionHistory(r.Context(), accountID, limit, offset)
	if err != nil {
		if errors.Is(err, domain.ErrAccountNotFound) {
			respondError(w, http.StatusNotFound, err.Error())
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"account_id": accountID,
		"entries":    entries,
		"count":      len(entries),
	})
}

// ReverseTransactionHandler handles POST /api/v1/transactions/{id}/reverse
type ReverseTransactionRequest struct {
	Reason string `json:"reason"`
	Actor  string `json:"actor,omitempty"`
}

func (h *Handler) ReverseTransactionHandler(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	txID, err := uuid.Parse(idStr)
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid transaction_id UUID")
		return
	}

	var req ReverseTransactionRequest
	if r.Body != nil && r.ContentLength > 0 {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	tx, err := h.ledgerService.ReverseTransaction(r.Context(), txID, req.Reason, req.Actor)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrTransactionNotFound):
			respondError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, domain.ErrTransactionAlreadyReversed):
			respondError(w, http.StatusConflict, err.Error())
		default:
			respondError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	respondJSON(w, http.StatusOK, tx)
}

// ReconcileAllAccountsHandler handles POST /api/v1/reconcile
func (h *Handler) ReconcileAllAccountsHandler(w http.ResponseWriter, r *http.Request) {
	mismatches, err := h.ledgerService.ReconcileAllAccounts(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	status := "OK"
	if len(mismatches) > 0 {
		status = "MISMATCH_DETECTED"
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"status":          status,
		"mismatches":      mismatches,
		"mismatches_count": len(mismatches),
	})
}
