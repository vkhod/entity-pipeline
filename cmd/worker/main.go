package main

import (
	"context"
	"flag"
	"log"
	"os/signal"
	"syscall"

	"github.com/vkhod/entity-pipeline/internal/config"
	"github.com/vkhod/entity-pipeline/internal/llm"
	"github.com/vkhod/entity-pipeline/internal/nlp"
	"github.com/vkhod/entity-pipeline/internal/store"
	"github.com/vkhod/entity-pipeline/internal/worker"
)

func main() {
	stage := flag.String("stage", "", "pipeline stage to run: extraction | classification")
	flag.Parse()

	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := store.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer st.Close()

	classifier, err := llm.NewClassifier(cfg.ClassifierMode, cfg.AnthropicAPIKey, cfg.AnthropicModel, cfg.DemoDelay)
	if err != nil {
		log.Fatalf("build classifier: %v", err)
	}

	// *store.Store satisfies queue.WorkQueue.
	w := worker.New(st, nlp.NewMockExtractor(), classifier, cfg.ClassifyBatch, cfg.PollInterval, cfg.Backoff)

	switch *stage {
	case "extraction":
		w.RunExtraction(ctx)
	case "classification":
		w.RunClassification(ctx)
	default:
		log.Fatalf("unknown --stage %q (want extraction|classification)", *stage)
	}
}
