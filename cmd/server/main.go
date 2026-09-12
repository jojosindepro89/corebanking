package main

import (
	"database/sql"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"core-banking-ledger/pkg/api"
	"core-banking-ledger/pkg/repository"
	"core-banking-ledger/pkg/service"

	_ "github.com/lib/pq"
)

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://postgres:postgrespassword@localhost:5432/ledger_db?sslmode=disable"
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Connecting to PostgreSQL database at %s...", dbURL)
	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		log.Fatalf("Failed to open database connection: %v", err)
	}
	defer db.Close()

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		log.Printf("Warning: Database ping failed: %v", err)
	}

	// Apply migrations
	schemaBytes, err := os.ReadFile("migrations/001_init_schema.sql")
	if err != nil {
		log.Printf("Warning: Could not read migrations/001_init_schema.sql: %v", err)
	} else {
		log.Println("Executing database migrations...")
		if _, err := db.Exec(string(schemaBytes)); err != nil {
			log.Fatalf("Failed to execute database migrations: %v", err)
		}
		log.Println("Database migrations applied successfully.")
	}

	repo := repository.NewRepository(db)
	ledgerSvc := service.NewService(repo)
	handler := api.NewHandler(ledgerSvc)
	router := api.NewRouter(handler)

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      router,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("Core Banking Ledger Service running on http://localhost:%s", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down Core Banking Ledger Service...")
}
