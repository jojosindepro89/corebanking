package api

import (
	"net/http"
	"os"
	"path/filepath"
	"time"

	gwrepo "core-banking-ledger/internal/gateway/repository"
	gwservice "core-banking-ledger/internal/gateway/service"

	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"
)

func NewGatewayRouter(handler *GatewayHandler, dashHandler *AdminDashboardHandler, nipHandler *NIPHandler, fraudHandler *FraudHandler, cardHandler *CardHandler, fxHandler *FXHandler, authSvc *gwservice.AuthService, repo *gwrepo.GatewayRepository, allowedOrigins []string) http.Handler {
	r := chi.NewRouter()

	// Base middleware stack
	r.Use(chiMiddleware.RequestID)
	r.Use(chiMiddleware.RealIP)
	r.Use(chiMiddleware.Recoverer)
	r.Use(SecurityHeadersMiddleware())
	r.Use(CORSMiddleware(allowedOrigins))
	r.Use(APIAuditLoggingMiddleware(repo))

	// Health check
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		respondJSON(w, http.StatusOK, map[string]string{"status": "UP", "service": "api-gateway"})
	})

	// Public Card Endpoints (for Dashboard & Operations UI)
	if cardHandler != nil {
		r.Post("/cards/issue", cardHandler.IssueCard)
		r.Post("/api/v1/cards/issue", cardHandler.IssueCard)
		r.Get("/cards/all", cardHandler.GetAllCards)
		r.Get("/api/v1/cards/all", cardHandler.GetAllCards)
		r.Get("/cards/user/{user_id}", cardHandler.GetUserCards)
		r.Get("/api/v1/cards/user/{user_id}", cardHandler.GetUserCards)
		r.Post("/cards/{id}/freeze", cardHandler.FreezeCard)
		r.Post("/api/v1/cards/{id}/freeze", cardHandler.FreezeCard)
		r.Post("/cards/{id}/unfreeze", cardHandler.UnfreezeCard)
		r.Post("/api/v1/cards/{id}/unfreeze", cardHandler.UnfreezeCard)
		r.Post("/cards/simulate-pos", cardHandler.SimulatePOSCharge)
		r.Post("/api/v1/cards/simulate-pos", cardHandler.SimulatePOSCharge)
	}

	// Public NIBSS Inbound Webhook Endpoint
	if nipHandler != nil {
		r.Post("/nip/webhook", nipHandler.InboundWebhook)
		r.Post("/api/v1/nip/webhook", nipHandler.InboundWebhook)
	}

	// Serve Admin Dashboard HTML UI
	serveDashboardUI := func(w http.ResponseWriter, r *http.Request) {
		content, err := os.ReadFile("internal/gateway/web/admin_dashboard.html")
		if err != nil {
			content, err = os.ReadFile("../../internal/gateway/web/admin_dashboard.html")
		}
		if err != nil {
			http.Error(w, "Dashboard UI file not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content)
	}

	r.Get("/", serveDashboardUI)
	r.Get("/admin/ui", serveDashboardUI)
	r.Get("/admin-ui", serveDashboardUI)
	r.Get("/ui", serveDashboardUI)

	workDir, _ := os.Getwd()
	assetsDir := http.Dir(filepath.Join(workDir, "internal/gateway/web/assets"))
	r.Handle("/assets/*", http.StripPrefix("/assets/", http.FileServer(assetsDir)))

	// Public Auth Endpoints (Rate limited: 10 req/minute per IP)
	mountAuthRoutes := func(r chi.Router) {
		r.Use(httprate.LimitByIP(10, 1*time.Minute))

		r.Post("/register", handler.Register)
		r.Post("/login", handler.Login)
		r.Post("/refresh", handler.RefreshToken)
		r.Post("/logout", handler.Logout)
	}

	r.Route("/auth", mountAuthRoutes)
	r.Route("/api/v1/auth", mountAuthRoutes)

	// Authenticated Routes
	r.Group(func(r chi.Router) {
		r.Use(AuthMiddleware(authSvc))
		r.Use(MFAHeaderExtractor())

		// Authenticated MFA & Session Security Endpoints
		r.Post("/auth/mfa/setup", handler.SetupMFA)
		r.Post("/auth/mfa/verify", handler.VerifyMFA)
		r.Post("/auth/logout-all", handler.LogoutAll)
		r.Post("/auth/password/change", handler.ChangePassword)
		r.Post("/auth/pin/setup", handler.SetupPIN)
		r.Post("/auth/pin/verify", handler.VerifyPIN)

		r.Post("/api/v1/auth/mfa/setup", handler.SetupMFA)
		r.Post("/api/v1/auth/mfa/verify", handler.VerifyMFA)
		r.Post("/api/v1/auth/logout-all", handler.LogoutAll)
		r.Post("/api/v1/auth/password/change", handler.ChangePassword)
		r.Post("/api/v1/auth/pin/setup", handler.SetupPIN)
		r.Post("/api/v1/auth/pin/verify", handler.VerifyPIN)

		// Account Management (Rate limited: 60 req/minute per IP)
		mountAccountRoutes := func(r chi.Router) {
			r.Use(httprate.LimitByIP(60, 1*time.Minute))

			r.Post("/", handler.CreateAccount)
			r.Get("/{id}/balance", handler.GetBalance)
			r.Get("/{id}/transactions", handler.GetTransactions)
		}
		r.Route("/accounts", mountAccountRoutes)
		r.Route("/api/v1/accounts", mountAccountRoutes)

		// Transfers / Money Movement (Rate limited: 10 req/minute per IP)
		mountTransferRoutes := func(r chi.Router) {
			r.Use(httprate.LimitByIP(10, 1*time.Minute))

			r.Post("/", handler.Transfer)
		}
		r.Route("/transfers", mountTransferRoutes)
		r.Route("/api/v1/transfers", mountTransferRoutes)

		// NIP Interbank Transfers (Authenticated Users)
		if nipHandler != nil {
			mountNIPRoutes := func(r chi.Router) {
				r.Use(httprate.LimitByIP(15, 1*time.Minute))
				r.Post("/name-enquiry", nipHandler.NameEnquiry)
				r.Post("/transfer", nipHandler.OutboundTransfer)
			}
			r.Route("/nip", mountNIPRoutes)
			r.Route("/api/v1/nip", mountNIPRoutes)
		}

		// Multi-Currency FX Rate Lock & Swap Engine
		if fxHandler != nil {
			mountFXRoutes := func(r chi.Router) {
				r.Use(httprate.LimitByIP(30, 1*time.Minute))
				r.Get("/rates", fxHandler.GetRatesHandler)
				r.Post("/quote", fxHandler.CreateQuoteHandler)
				r.Post("/execute", fxHandler.ExecuteSwapHandler)
			}
			r.Route("/fx", mountFXRoutes)
			r.Route("/api/v1/fx", mountFXRoutes)
		}

		// KYC Submission
		r.Post("/kyc/submit", dashHandler.SubmitKYCApplication)
		r.Post("/api/v1/kyc/submit", dashHandler.SubmitKYCApplication)

		// Admin Endpoints (Elevated role + Admin middleware check)
		mountAdminRoutes := func(r chi.Router) {
			r.Use(AdminMiddleware())

			r.Get("/accounts/{id}", handler.AdminGetAccount)
			r.Post("/accounts/{id}/freeze", handler.AdminFreezeAccount)
			r.Post("/transactions/{id}/reverse", handler.AdminReverseTransaction)

			// Admin Dashboard API
			r.Get("/dashboard/health", dashHandler.GetSystemHealth)
			r.Post("/dashboard/reconcile/run", dashHandler.RunReconciliation)
			r.Get("/dashboard/reconcile/history", dashHandler.GetReconciliationHistory)
			r.Get("/dashboard/transactions/flagged", dashHandler.GetFlaggedTransactions)
			r.Post("/dashboard/transactions/flagged/{id}/review", dashHandler.ReviewFlaggedTransaction)
			r.Get("/dashboard/kyc/queue", dashHandler.GetKYCQueue)
			r.Post("/dashboard/kyc/{id}/review", dashHandler.ReviewKYCApplication)
			r.Post("/accounts/{id}/unfreeze", dashHandler.UnfreezeAccount)
			r.Get("/audit-logs", dashHandler.SearchAuditLogs)

			// Fraud Management & APP Dispute Evidence API
			if fraudHandler != nil {
				r.Get("/fraud/held", fraudHandler.GetHeldTransactions)
				r.Post("/fraud/held/{id}/approve", fraudHandler.ApproveHeldTransaction)
				r.Post("/fraud/held/{id}/reject", fraudHandler.RejectHeldTransaction)
				r.Get("/fraud/evidence/{tx_id}", fraudHandler.GetAPPEvidenceByTxID)
				r.Get("/fraud/configs", fraudHandler.GetRuleConfigs)
				r.Put("/fraud/configs", fraudHandler.UpdateRuleConfig)
			}
		}

		r.Route("/admin", mountAdminRoutes)
		r.Route("/api/v1/admin", mountAdminRoutes)
	})

	return r
}

