package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lanby-dev/lanby-relay/internal/relay"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := relay.LoadConfigFromEnv()
	if err != nil {
		log.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	client := relay.NewClient(cfg.PlatformURL)

	runner := relay.NewRunner(log, cfg, client)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := runner.Start(ctx); err != nil {
		log.Error("relay stopped with error", "error", err)
		os.Exit(1)
	}

	<-ctx.Done()
	time.Sleep(100 * time.Millisecond)
}
