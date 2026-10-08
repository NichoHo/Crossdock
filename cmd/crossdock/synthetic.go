package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nicholasgasior/crossdock/internal/synth"
)

// crossdock synthetic -warehouse W [-operators 4] [-every 20s]
//
// Runs scripted shifts against the API (CROSSDOCK_API_URL, CROSSDOCK_SERVICE_TOKEN,
// CROSSDOCK_IDENTITY_SIGNING_KEY) so a deployed platform is always doing something.
func cmdSynthetic(args []string) error {
	fs := flag.NewFlagSet("synthetic", flag.ExitOnError)
	wid := fs.String("warehouse", os.Getenv("CROSSDOCK_WAREHOUSE_ID"), "warehouse id")
	ops := fs.Int("operators", 4, "synthetic operators")
	every := fs.Duration("every", 20*time.Second, "producer tick (one delivery and a few orders)")
	_ = fs.Parse(args)
	if *wid == "" {
		return fmt.Errorf("-warehouse is required")
	}
	base := os.Getenv("CROSSDOCK_API_URL")
	if base == "" {
		base = "http://127.0.0.1:8080"
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	sh := &synth.Shift{C: synth.New(base, requireEnv("CROSSDOCK_SERVICE_TOKEN"), requireEnv("CROSSDOCK_IDENTITY_SIGNING_KEY"), *wid),
		Operators: *ops, Every: *every, Seed: uint64(time.Now().UnixNano()), Log: log}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		for range time.Tick(time.Minute) {
			log.Info("synthetic progress", "tasks_confirmed", sh.Done.Load(), "shipped", sh.Shipped.Load(), "errors", sh.Errors.Load())
		}
	}()
	return sh.Run(ctx)
}
