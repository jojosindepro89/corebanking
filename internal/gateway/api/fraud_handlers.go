package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"core-banking-ledger/internal/gateway/service/fraud"
)

type FraudHandler struct {
	fraudService *fraud.FraudService
}

func NewFraudHandler(fraudService *fraud.FraudService) *FraudHandler {
	return &FraudHandler{fraudService: fraudService}
}

func (h *FraudHandler) GetHeldTransactions(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	list, err := h.fraudService.GetHeldTransactions(r.Context(), status)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "FAILED_TO_GET_HELD_TRANSACTIONS", "message": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"held_transactions": list,
	})
}

type ReviewActionRequest struct {
	Reason string `json:"reason,omitempty"`
}

func (h *FraudHandler) ApproveHeldTransaction(w http.ResponseWriter, r *http.Request) {
	holdIDStr := chi.URLParam(r, "id")
	holdID, err := uuid.Parse(holdIDStr)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "INVALID_HOLD_ID", "message": "Invalid UUID"})
		return
	}

	adminIDStr, _ := r.Context().Value(UserIDKey).(string)
	adminID, _ := uuid.Parse(adminIDStr)

	err = h.fraudService.ApproveHeldTransaction(r.Context(), holdID, adminID)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "APPROVAL_FAILED", "message": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "approved",
		"message": "Held transaction approved and released for processing",
	})
}

func (h *FraudHandler) RejectHeldTransaction(w http.ResponseWriter, r *http.Request) {
	holdIDStr := chi.URLParam(r, "id")
	holdID, err := uuid.Parse(holdIDStr)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "INVALID_HOLD_ID", "message": "Invalid UUID"})
		return
	}

	var req ReviewActionRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Reason == "" {
		req.Reason = "Transaction flagged as high-risk and rejected by compliance admin"
	}

	adminIDStr, _ := r.Context().Value(UserIDKey).(string)
	adminID, _ := uuid.Parse(adminIDStr)

	err = h.fraudService.RejectHeldTransaction(r.Context(), holdID, adminID, req.Reason)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "REJECTION_FAILED", "message": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "rejected",
		"message": "Held transaction rejected and marked for reversal",
	})
}

func (h *FraudHandler) GetAPPEvidenceByTxID(w http.ResponseWriter, r *http.Request) {
	txIDStr := chi.URLParam(r, "tx_id")
	txID, err := uuid.Parse(txIDStr)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "INVALID_TX_ID", "message": "Invalid Transaction UUID"})
		return
	}

	evidence, err := h.fraudService.GetAPPEvidenceByTxID(r.Context(), txID)
	if err != nil {
		respondJSON(w, http.StatusNotFound, map[string]string{"error": "EVIDENCE_NOT_FOUND", "message": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(evidence)
}

func (h *FraudHandler) GetRuleConfigs(w http.ResponseWriter, r *http.Request) {
	configs, err := h.fraudService.GetRuleConfigs(r.Context())
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "FAILED_TO_GET_CONFIGS", "message": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"rule_configs": configs,
	})
}

type UpdateRuleConfigRequest struct {
	RuleKey      string  `json:"rule_key"`
	ValueNumeric float64 `json:"value_numeric"`
	ValueString  string  `json:"value_string"`
	IsEnabled    bool    `json:"is_enabled"`
}

func (h *FraudHandler) UpdateRuleConfig(w http.ResponseWriter, r *http.Request) {
	var req UpdateRuleConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RuleKey == "" {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "INVALID_INPUT", "message": "rule_key is required"})
		return
	}

	err := h.fraudService.UpdateRuleConfig(r.Context(), req.RuleKey, req.ValueNumeric, req.ValueString, req.IsEnabled)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "UPDATE_FAILED", "message": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":   "updated",
		"rule_key": req.RuleKey,
	})
}
