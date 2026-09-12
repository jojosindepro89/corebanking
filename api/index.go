package handler

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	coreRepo "core-banking-ledger/pkg/repository"
	coreService "core-banking-ledger/pkg/service"
	"core-banking-ledger/migrations"

	gwapi "core-banking-ledger/pkg/gateway/api"
	gwdomain "core-banking-ledger/pkg/gateway/domain"
	gwrepo "core-banking-ledger/pkg/gateway/repository"
	gwservice "core-banking-ledger/pkg/gateway/service"
	fraudSvc "core-banking-ledger/pkg/gateway/service/fraud"
	nipSvc "core-banking-ledger/pkg/gateway/service/nip"

	_ "github.com/lib/pq"
)

var (
	once   sync.Once
	router http.Handler
)

func initGateway() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgresql://neondb_owner:npg_lYSdb0g6fGjM@ep-winter-unit-aii55k6e-pooler.c-4.us-east-1.aws.neon.tech/neondb?sslmode=require"
	}

	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		jwtSecret = "super-secret-core-banking-jwt-key-2026-secure"
	}

	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		log.Printf("Failed to open DB: %v", err)
		return
	}

	runMigrations(db)

	cRepo := coreRepo.NewRepository(db)
	cService := coreService.NewService(cRepo)

	gRepo := gwrepo.NewGatewayRepository(db)
	authSvc := gwservice.NewAuthService(jwtSecret, 15*time.Minute)
	gService := gwservice.NewGatewayService(gRepo, cService, authSvc, 7*24*time.Hour)

	dashSvc := gwservice.NewAdminDashboardService(gRepo, cService, nil)
	dashHandler := gwapi.NewAdminDashboardHandler(dashSvc)

	fraudScorer := fraudSvc.NewRulesRiskScorer(db)
	fraudService := fraudSvc.NewFraudService(db, fraudScorer, nil)
	fraudHandler := gwapi.NewFraudHandler(fraudService)

	nipProvider := nipSvc.NewMockNIBSSProvider()
	nipService := nipSvc.NewNIPService(gRepo, cService, nipProvider, fraudService)
	nipHandler := gwapi.NewNIPHandler(nipService)

	cardService := gwservice.NewCardService(gRepo, cService)
	cardHandler := gwapi.NewCardHandler(cardService)

	fxService := gwservice.NewFXService(gRepo, cService)
	fxHandler := gwapi.NewFXHandler(fxService)

	handler := gwapi.NewGatewayHandler(gService)

	// Seed default Admin & Demo user
	ctx := context.Background()
	_, _ = gService.RegisterUser(ctx, gwdomain.RegisterRequest{
		Email:    "admin@bank.com",
		Password: "AdminPass123!",
		IsAdmin:  true,
	})
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
			_, _ = cardService.IssueCard(ctx, gwservice.IssueCardRequest{
				UserID:          demoUser.UserID,
				AccountID:       acc.AccountID,
				CardBrand:       "VISA",
				CardType:        "VIRTUAL",
				CardholderName:  "Demo Customer",
				DailyLimitCents: 5000000,
			})
		}
	}

	router = gwapi.NewGatewayRouter(handler, dashHandler, nipHandler, fraudHandler, cardHandler, fxHandler, authSvc, gRepo, []string{"*"})
}

func Handler(w http.ResponseWriter, r *http.Request) {
	once.Do(initGateway)
	if router != nil {
		router.ServeHTTP(w, r)
	} else {
		http.Error(w, "Core Banking Engine initialization error", http.StatusInternalServerError)
	}
}

func runMigrations(db *sql.DB) {
	files := []string{
		"001_init_schema.sql",
		"002_api_gateway.sql",
		"003_admin_dashboard.sql",
		"004_nip_integration.sql",
		"005_fraud_monitoring.sql",
		"006_core_banking_expansion.sql",
		"007_card_issuance.sql",
		"008_auth_enhancements.sql",
		"009_fx_engine.sql",
	}

	for _, file := range files {
		content, err := migrations.MigrationFiles.ReadFile(file)
		if err != nil {
			log.Printf("Migration file read error %s: %v", file, err)
			continue
		}
		_, err = db.Exec(string(content))
		if err != nil {
			log.Printf("Migration exec notice %s: %v", file, err)
		}
	}
}

