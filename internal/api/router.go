package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func NewRouter(handler *Handler) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/accounts", handler.CreateAccountHandler)
		r.Get("/accounts/{id}/balance", handler.GetBalanceHandler)
		r.Get("/accounts/{id}/history", handler.GetTransactionHistoryHandler)

		r.Post("/transactions", handler.PostTransactionHandler)
		r.Post("/transactions/{id}/reverse", handler.ReverseTransactionHandler)

		r.Post("/reconcile", handler.ReconcileAllAccountsHandler)
	})

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"UP"}`))
	})

	return r
}
