package api

import (
	"encoding/json"
	"net/http"

	gwservice "core-banking-ledger/internal/gateway/service"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type CardHandler struct {
	cardService *gwservice.CardService
}

func NewCardHandler(cardSvc *gwservice.CardService) *CardHandler {
	return &CardHandler{cardService: cardSvc}
}

func (h *CardHandler) IssueCard(w http.ResponseWriter, r *http.Request) {
	var req gwservice.IssueCardRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON payload"})
		return
	}

	card, err := h.cardService.IssueCard(r.Context(), req)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	respondJSON(w, http.StatusCreated, card)
}

func (h *CardHandler) GetUserCards(w http.ResponseWriter, r *http.Request) {
	userIDStr := chi.URLParam(r, "user_id")
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid user ID"})
		return
	}

	cards, err := h.cardService.GetUserCards(r.Context(), userID)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, cards)
}

func (h *CardHandler) GetAllCards(w http.ResponseWriter, r *http.Request) {
	cards, err := h.cardService.GetAllCards(r.Context(), 50)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, cards)
}

func (h *CardHandler) FreezeCard(w http.ResponseWriter, r *http.Request) {
	cardIDStr := chi.URLParam(r, "id")
	cardID, err := uuid.Parse(cardIDStr)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid card ID"})
		return
	}

	if err := h.cardService.FreezeCard(r.Context(), cardID); err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "FROZEN", "card_id": cardIDStr})
}

func (h *CardHandler) UnfreezeCard(w http.ResponseWriter, r *http.Request) {
	cardIDStr := chi.URLParam(r, "id")
	cardID, err := uuid.Parse(cardIDStr)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid card ID"})
		return
	}

	if err := h.cardService.UnfreezeCard(r.Context(), cardID); err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "ACTIVE", "card_id": cardIDStr})
}

func (h *CardHandler) SimulatePOSCharge(w http.ResponseWriter, r *http.Request) {
	var req gwservice.SimulatePOSChargeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON request"})
		return
	}

	tx, err := h.cardService.SimulatePOSCharge(r.Context(), req)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error":       err.Error(),
			"transaction": tx,
		})
		return
	}

	respondJSON(w, http.StatusOK, tx)
}
