package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	coreRepo "core-banking-ledger/internal/repository"
	coreService "core-banking-ledger/internal/service"

	gwapi "core-banking-ledger/internal/gateway/api"
	gwdomain "core-banking-ledger/internal/gateway/domain"
	gwrepo "core-banking-ledger/internal/gateway/repository"
	gwservice "core-banking-ledger/internal/gateway/service"
	fraudSvc "core-banking-ledger/internal/gateway/service/fraud"
	nipSvc "core-banking-ledger/internal/gateway/service/nip"

	_ "github.com/lib/pq"
)

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://ledger_user:ledger_password@localhost:5432/ledger_db?sslmode=disable"
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}

	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		jwtSecret = "super-secret-core-banking-jwt-key-2026-secure"
	}

	log.Printf("Connecting to PostgreSQL database...")
	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		log.Fatalf("Failed to open DB connection: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		log.Fatalf("Failed to ping PostgreSQL: %v", err)
	}

	log.Printf("Running schema migrations...")
	runMigrations(db)

	// Core ledger initialization
	cRepo := coreRepo.NewRepository(db)
	cService := coreService.NewService(cRepo)

	// Gateway initialization
	gRepo := gwrepo.NewGatewayRepository(db)
	authSvc := gwservice.NewAuthService(jwtSecret, 15*time.Minute)
	gService := gwservice.NewGatewayService(gRepo, cService, authSvc, 7*24*time.Hour)

	// Admin Dashboard & Reconciliation initialization
	reconciler := gwservice.NewReconciliationRunner(gRepo, cService)
	reconciler.StartCron(context.Background(), 15*time.Minute)
	log.Printf("Automated Reconciliation Cron scheduled (every 15 minutes).")

	dashSvc := gwservice.NewAdminDashboardService(gRepo, cService, reconciler)
	dashHandler := gwapi.NewAdminDashboardHandler(dashSvc)

	// Fraud & APP Evidence Layer
	fraudScorer := fraudSvc.NewRulesRiskScorer(db)
	fraudService := fraudSvc.NewFraudService(db, fraudScorer, nil)
	fraudHandler := gwapi.NewFraudHandler(fraudService)

	// NIP Interbank Integration Layer
	nipProvider := nipSvc.NewMockNIBSSProvider()
	nipService := nipSvc.NewNIPService(gRepo, cService, nipProvider, fraudService)
	nipService.StartReconciliationCron(context.Background(), 5*time.Minute)
	nipHandler := gwapi.NewNIPHandler(nipService)

	// Card Issuance & POS Engine
	cardService := gwservice.NewCardService(gRepo, cService)
	cardHandler := gwapi.NewCardHandler(cardService)

	handler := gwapi.NewGatewayHandler(gService)

	// Seed default Admin User for Admin Dashboard UI
	ctx := context.Background()
	_, _ = gService.RegisterUser(ctx, gwdomain.RegisterRequest{
		Email:    "admin@bank.com",
		Password: "AdminPass123!",
		IsAdmin:  true,
	})
	log.Printf("Default Admin User registered/verified (Email: admin@bank.com, Password: AdminPass123!)")

	// Seed default Demo Customer User for Portal UI
	demoUser, err := gService.RegisterUser(ctx, gwdomain.RegisterRequest{
		Email:    "demo@user.com",
		Password: "UserPass123!",
		IsAdmin:  false,
	})
	if err == nil && demoUser != nil {
		acc, _ := gService.CreateAccount(ctx, demoUser.UserID, gwdomain.CreateGatewayAccountRequest{
			Name:        "Primary Checking Account",
			AccountType: "asset",
			Currency:    "NGN",
		})
		if acc != nil {
			// Seed a default Virtual Visa Card for demo user
			_, _ = cardService.IssueCard(ctx, gwservice.IssueCardRequest{
				UserID:         demoUser.UserID,
				AccountID:      acc.AccountID,
				CardBrand:      "VISA",
				CardType:       "VIRTUAL",
				CardholderName: "Demo Customer",
				DailyLimitCents: 5000000,
			})
		}
	}
	log.Printf("Default Demo User registered/verified (Email: demo@user.com, Password: UserPass123!)")

	router := gwapi.NewGatewayRouter(handler, dashHandler, nipHandler, fraudHandler, cardHandler, authSvc, gRepo, []string{"*"})

	addr := fmt.Sprintf(":%s", port)
	log.Printf("API Gateway Service running on http://localhost%s", addr)
	if err := http.ListenAndServe(addr, router); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func runMigrations(db *sql.DB) {
	files := []string{
		"migrations/001_init_schema.sql",
		"migrations/002_api_gateway.sql",
		"migrations/003_admin_dashboard.sql",
		"migrations/004_nip_integration.sql",
		"migrations/005_fraud_monitoring.sql",
		"migrations/006_core_banking_expansion.sql",
		"migrations/007_card_issuance.sql",
		"migrations/008_auth_enhancements.sql",
	}

	for _, file := range files {
		content, err := os.ReadFile(file)
		if err != nil {
			log.Fatalf("Failed to read migration file %s: %v", file, err)
		}
		_, err = db.Exec(string(content))
		if err != nil {
			log.Fatalf("Failed to execute migration %s: %v", file, err)
		}
	}
	log.Printf("All database migrations applied successfully.")
}
