package api

import (
	"encoding/json"
	"net/http"

	"core-banking-ledger/internal/domain"
	"core-banking-ledger/internal/service"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Request payloads
type OriginateLoanRequest struct {
	BorrowerAccountID string  `json:"borrower_account_id"`
	VaultAccountID    string  `json:"vault_account_id"`
	PrincipalCents    int64   `json:"principal_cents"`
	InterestRate      float64 `json:"interest_rate_percent"`
	TermMonths        int     `json:"term_months"`
}

type LoanRepaymentRequest struct {
	BorrowerAccountID  string `json:"borrower_account_id"`
	LoanAssetAccountID string `json:"loan_asset_account_id"`
	AmountCents        int64  `json:"amount_cents"`
}

type CreateStandingOrderRequest struct {
	SourceAccountID      string `json:"source_account_id"`
	DestinationAccountID string `json:"destination_account_id"`
	AmountCents          int64  `json:"amount_cents"`
	Description          string `json:"description"`
	Frequency            string `json:"frequency"` // daily, weekly, monthly
}

type SubmitBulkPayoutRequest struct {
	SourceAccountID string `json:"source_account_id"`
	Title           string `json:"title"`
	Items           []struct {
		DestinationAccountID string `json:"destination_account_id"`
		AmountCents          int64  `json:"amount_cents"`
	} `json:"items"`
}

type ExpansionHandler struct {
	loanEngine     *service.LoanEngine
	standingEngine *service.StandingOrderEngine
	interestEngine *service.InterestEngine
}

func NewExpansionHandler(loanEngine *service.LoanEngine, standingEngine *service.StandingOrderEngine, interestEngine *service.InterestEngine) *ExpansionHandler {
	return &ExpansionHandler{
		loanEngine:     loanEngine,
		standingEngine: standingEngine,
		interestEngine: interestEngine,
	}
}

func (h *ExpansionHandler) OriginateLoanHandler(w http.ResponseWriter, r *http.Request) {
	userID, err := getUserIDFromContext(r)
	if err != nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized user context"})
		return
	}

	var req OriginateLoanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
		return
	}

	borrowerID, err := uuid.Parse(req.BorrowerAccountID)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid borrower_account_id"})
		return
	}

	vaultID, err := uuid.Parse(req.VaultAccountID)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid vault_account_id"})
		return
	}

	loan, err := h.loanEngine.OriginateLoan(r.Context(), userID, borrowerID, vaultID, req.PrincipalCents, req.InterestRate, req.TermMonths, userID.String())
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	respondJSON(w, http.StatusCreated, loan)
}

func (h *ExpansionHandler) RepayLoanHandler(w http.ResponseWriter, r *http.Request) {
	userID, err := getUserIDFromContext(r)
	if err != nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized user context"})
		return
	}

	loanIDStr := chi.URLParam(r, "id")
	loanID, err := uuid.Parse(loanIDStr)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid loan ID"})
		return
	}

	var req LoanRepaymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
		return
	}

	borrowerID, err := uuid.Parse(req.BorrowerAccountID)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid borrower_account_id"})
		return
	}

	loanAssetID, err := uuid.Parse(req.LoanAssetAccountID)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid loan_asset_account_id"})
		return
	}

	loan, err := h.loanEngine.ProcessLoanRepayment(r.Context(), loanID, borrowerID, loanAssetID, req.AmountCents, userID.String())
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, loan)
}

func (h *ExpansionHandler) CreateStandingOrderHandler(w http.ResponseWriter, r *http.Request) {
	userID, err := getUserIDFromContext(r)
	if err != nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized user context"})
		return
	}

	var req CreateStandingOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
		return
	}

	srcID, err := uuid.Parse(req.SourceAccountID)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid source_account_id"})
		return
	}

	destID, err := uuid.Parse(req.DestinationAccountID)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid destination_account_id"})
		return
	}

	order := &domain.StandingOrder{
		UserID:               userID,
		SourceAccountID:      srcID,
		DestinationAccountID: destID,
		AmountCents:          req.AmountCents,
		Description:          req.Description,
		Frequency:            req.Frequency,
	}

	if err := h.standingEngine.CreateStandingOrder(r.Context(), order); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	respondJSON(w, http.StatusCreated, order)
}

func (h *ExpansionHandler) SubmitBulkPayoutHandler(w http.ResponseWriter, r *http.Request) {
	userID, err := getUserIDFromContext(r)
	if err != nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized user context"})
		return
	}

	var req SubmitBulkPayoutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
		return
	}

	srcID, err := uuid.Parse(req.SourceAccountID)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid source_account_id"})
		return
	}

	items := make([]domain.BulkPayoutItem, len(req.Items))
	for i, item := range req.Items {
		destID, err := uuid.Parse(item.DestinationAccountID)
		if err != nil {
			respondJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid destination_account_id in items"})
			return
		}
		items[i] = domain.BulkPayoutItem{
			DestinationAccountID: destID,
			AmountCents:          item.AmountCents,
		}
	}

	batch := &domain.BulkPayoutBatch{
		UserID:          userID,
		SourceAccountID: srcID,
		Title:           req.Title,
		Items:           items,
	}

	if err := h.standingEngine.SubmitBulkPayout(r.Context(), batch); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	// Trigger async processing
	go func() {
		_ = h.standingEngine.ProcessBulkPayoutBatch(r.Context(), batch.BatchID, userID.String())
	}()

	respondJSON(w, http.StatusAccepted, batch)
}
