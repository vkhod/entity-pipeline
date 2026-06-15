package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/vkhod/entity-pipeline/internal/api"
	"github.com/vkhod/entity-pipeline/internal/config"
	"github.com/vkhod/entity-pipeline/internal/store"
)

func main() {
	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := store.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer st.Close()

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.NewServer(st).Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("API listening on %s", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down API")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
