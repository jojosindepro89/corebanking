package api

import (
	"encoding/json"
	"net/http"

	nipSvc "core-banking-ledger/internal/gateway/service/nip"
)

type NIPHandler struct {
	svc *nipSvc.NIPService
}

func NewNIPHandler(svc *nipSvc.NIPService) *NIPHandler {
	return &NIPHandler{svc: svc}
}

type NameEnquiryRequest struct {
	BankCode      string `json:"bank_code"`
	AccountNumber string `json:"account_number"`
}

func (h *NIPHandler) NameEnquiry(w http.ResponseWriter, r *http.Request) {
	userID, err := getUserIDFromContext(r)
	if err != nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	var req NameEnquiryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}

	accName, sessionID, err := h.svc.NameEnquiry(r.Context(), userID, req.BankCode, req.AccountNumber)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"account_name":   accName,
		"session_id":     sessionID,
		"bank_code":      req.BankCode,
		"account_number": req.AccountNumber,
	})
}

func (h *NIPHandler) OutboundTransfer(w http.ResponseWriter, r *http.Request) {
	userID, err := getUserIDFromContext(r)
	if err != nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	var req nipSvc.NIPOutboundTransferRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}

	ip := getClientIP(r)
	deviceInfo := r.Header.Get("User-Agent")

	stepUpCode := r.Header.Get("X-StepUp-Code")
	if stepUpCode == "" {
		stepUpCode = req.StepUpCode
	}

	isStepUpValidated := (len(stepUpCode) == 6)

	tx, status, err := h.svc.OutboundTransfer(r.Context(), userID, req, ip, deviceInfo, isStepUpValidated)
	if status == "STEP_UP_AUTH_REQUIRED" {
		respondJSON(w, http.StatusForbidden, map[string]string{
			"error":   "STEP_UP_AUTH_REQUIRED",
			"message": "Medium risk transfer detected. Re-enter 6-digit TOTP code in X-StepUp-Code header.",
		})
		return
	}

	if status == "HELD_FOR_REVIEW" {
		respondJSON(w, http.StatusAccepted, map[string]string{
			"status":  "HELD_FOR_REVIEW",
			"message": "High risk transfer detected. Held in compliance review queue.",
		})
		return
	}

	if err != nil && status != "FAILED" && status != "PENDING_RECONCILIATION" {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	txID := ""
	idempotencyKey := ""
	if tx != nil {
		txID = tx.TransactionID.String()
		idempotencyKey = tx.IdempotencyKey
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"status":          status,
		"session_id":      req.SessionID,
		"transaction_id":  txID,
		"idempotency_key": idempotencyKey,
	})
}

func (h *NIPHandler) InboundWebhook(w http.ResponseWriter, r *http.Request) {
	rawBody := drainBody(r)
	sig := r.Header.Get("X-NIBSS-Signature")

	var payload nipSvc.NIPInboundWebhookPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid webhook payload"})
		return
	}

	if err := h.svc.InboundWebhook(r.Context(), rawBody, sig, payload); err != nil {
		if err.Error() == "invalid webhook signature" {
			respondJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
			return
		}
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "SUCCESS", "message": "Inbound transfer processed"})
}
