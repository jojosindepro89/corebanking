package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	gwservice "core-banking-ledger/internal/gateway/service"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type AdminDashboardHandler struct {
	dashSvc *gwservice.AdminDashboardService
}

func NewAdminDashboardHandler(dashSvc *gwservice.AdminDashboardService) *AdminDashboardHandler {
	return &AdminDashboardHandler{dashSvc: dashSvc}
}

func (h *AdminDashboardHandler) GetSystemHealth(w http.ResponseWriter, r *http.Request) {
	overview, err := h.dashSvc.GetSystemHealth(r.Context())
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	respondJSON(w, http.StatusOK, overview)
}

func (h *AdminDashboardHandler) RunReconciliation(w http.ResponseWriter, r *http.Request) {
	adminID, err := getUserIDFromContext(r)
	if err != nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	res, err := h.dashSvc.RunOnDemandReconciliation(r.Context(), adminID)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, res)
}

func (h *AdminDashboardHandler) GetReconciliationHistory(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	runs, err := h.dashSvc.GetReconciliationHistory(r.Context(), limit)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"count": len(runs),
		"runs":  runs,
	})
}

func (h *AdminDashboardHandler) GetFlaggedTransactions(w http.ResponseWriter, r *http.Request) {
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	list, err := h.dashSvc.GetFlaggedTransactions(r.Context(), status)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"count": len(list),
		"items": list,
	})
}

type ReviewFlaggedRequest struct {
	Status string `json:"status"` // approved, dismissed
}

func (h *AdminDashboardHandler) ReviewFlaggedTransaction(w http.ResponseWriter, r *http.Request) {
	adminID, err := getUserIDFromContext(r)
	if err != nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	flagIDStr := chi.URLParam(r, "id")
	flagID, err := uuid.Parse(flagIDStr)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid flag ID"})
		return
	}

	var req ReviewFlaggedRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}

	if err := h.dashSvc.ReviewFlaggedTransaction(r.Context(), flagID, req.Status, adminID); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "flagged transaction updated successfully", "flag_id": flagID.String()})
}

func (h *AdminDashboardHandler) GetKYCQueue(w http.ResponseWriter, r *http.Request) {
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	list, err := h.dashSvc.GetKYCApplications(r.Context(), status)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{
		"count": len(list),
		"items": list,
	})
}

type SubmitKYCRequest struct {
	FullName string `json:"full_name"`
	DOB      string `json:"dob"`
	IDNumber string `json:"id_number"`
}

func (h *AdminDashboardHandler) SubmitKYCApplication(w http.ResponseWriter, r *http.Request) {
	userID, err := getUserIDFromContext(r)
	if err != nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	var req SubmitKYCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}

	app, err := h.dashSvc.SubmitKYCApplication(r.Context(), userID, req.FullName, req.DOB, req.IDNumber)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	respondJSON(w, http.StatusCreated, app)
}

type ReviewKYCRequest struct {
	Status string `json:"status"` // approved, rejected
	Reason string `json:"reason,omitempty"`
}

func (h *AdminDashboardHandler) ReviewKYCApplication(w http.ResponseWriter, r *http.Request) {
	adminID, err := getUserIDFromContext(r)
	if err != nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	kycIDStr := chi.URLParam(r, "id")
	kycID, err := uuid.Parse(kycIDStr)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid KYC application ID"})
		return
	}

	var req ReviewKYCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}

	if err := h.dashSvc.ReviewKYCApplication(r.Context(), kycID, req.Status, req.Reason, adminID); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "KYC application updated successfully", "kyc_id": kycID.String()})
}

type UnfreezeAccountRequest struct {
	Reason string `json:"reason"`
}

func (h *AdminDashboardHandler) UnfreezeAccount(w http.ResponseWriter, r *http.Request) {
	adminID, err := getUserIDFromContext(r)
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

	var req UnfreezeAccountRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	if err := h.dashSvc.UnfreezeAccount(r.Context(), accountID, req.Reason, adminID); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "account unfrozen successfully", "account_id": accountID.String()})
}

func (h *AdminDashboardHandler) SearchAuditLogs(w http.ResponseWriter, r *http.Request) {
	userIDStr := strings.TrimSpace(r.URL.Query().Get("user_id"))
	endpoint := strings.TrimSpace(r.URL.Query().Get("endpoint"))
	outcome := strings.TrimSpace(r.URL.Query().Get("outcome"))

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

	logs, err := h.dashSvc.SearchAuditLogs(r.Context(), userIDStr, endpoint, outcome, limit, offset)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"count":  len(logs),
		"limit":  limit,
		"offset": offset,
		"logs":   logs,
	})
}
