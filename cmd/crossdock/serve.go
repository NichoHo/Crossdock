package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	apispec "github.com/nicholasgasior/crossdock/api"
	"github.com/nicholasgasior/crossdock/internal/api"
	"github.com/nicholasgasior/crossdock/internal/auth"
	"github.com/nicholasgasior/crossdock/internal/counting"
	"github.com/nicholasgasior/crossdock/internal/delivery"
	"github.com/nicholasgasior/crossdock/internal/inventory"
	"github.com/nicholasgasior/crossdock/internal/outbox"
	"github.com/nicholasgasior/crossdock/internal/reconcile"
	"github.com/nicholasgasior/crossdock/internal/settlement"
	"github.com/nicholasgasior/crossdock/internal/slotting"
	"github.com/nicholasgasior/crossdock/internal/telemetry"
)

// config is read from the environment and validated once at startup (spec 13.3).
type config struct {
	DatabaseURL  string
	Listen       string
	ServiceToken string
	SigningKey   string

	// Webhook delivery (Phase 2). Both Brokers and EncryptionKey enable it.
	Brokers         []string
	EncryptionKey   string
	AllowPrivateWH  bool
	DeliveryWorkers int

	// Limits are requests per second per principal; 0 turns a limit off.
	Limits api.Limits

	// BackgroundEvery is how often counts are scheduled and inbound receipts reconciled.
	BackgroundEvery time.Duration
}

// loadConfig reports every missing or malformed value at once.
func loadConfig() (config, error) {
	c := config{
		DatabaseURL:     os.Getenv("CROSSDOCK_DATABASE_URL"),
		Listen:          os.Getenv("CROSSDOCK_LISTEN"),
		ServiceToken:    os.Getenv("CROSSDOCK_SERVICE_TOKEN"),
		SigningKey:      os.Getenv("CROSSDOCK_IDENTITY_SIGNING_KEY"),
		EncryptionKey:   os.Getenv("CROSSDOCK_SECRET_ENCRYPTION_KEY"),
		AllowPrivateWH:  os.Getenv("CROSSDOCK_WEBHOOK_ALLOW_PRIVATE") == "true",
		DeliveryWorkers: 16,
		Limits:          api.DefaultLimits(),
		BackgroundEvery: time.Minute,
	}
	for _, b := range strings.Split(os.Getenv("CROSSDOCK_BROKERS"), ",") {
		if b = strings.TrimSpace(b); b != "" {
			c.Brokers = append(c.Brokers, b)
		}
	}
	if c.Listen == "" {
		c.Listen = ":8080"
	}
	var problems []string
	if c.DatabaseURL == "" {
		problems = append(problems, "CROSSDOCK_DATABASE_URL is required")
	}
	if len(c.ServiceToken) < 32 {
		problems = append(problems, "CROSSDOCK_SERVICE_TOKEN is required and must be at least 32 characters")
	}
	if len(c.SigningKey) < 32 {
		problems = append(problems, "CROSSDOCK_IDENTITY_SIGNING_KEY is required and must be at least 32 characters")
	}
	if (len(c.Brokers) > 0) != (c.EncryptionKey != "") {
		problems = append(problems, "CROSSDOCK_BROKERS and CROSSDOCK_SECRET_ENCRYPTION_KEY must be set together to enable webhooks")
	}
	if c.EncryptionKey != "" {
		if _, err := delivery.NewEncryptor(c.EncryptionKey); err != nil {
			problems = append(problems, "CROSSDOCK_SECRET_ENCRYPTION_KEY: "+err.Error())
		}
	}
	if v := os.Getenv("CROSSDOCK_DELIVERY_WORKERS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 512 {
			problems = append(problems, "CROSSDOCK_DELIVERY_WORKERS must be between 1 and 512")
		}
		c.DeliveryWorkers = n
	}
	if v := os.Getenv("CROSSDOCK_BACKGROUND_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Second {
			problems = append(problems, "CROSSDOCK_BACKGROUND_INTERVAL must be a duration of at least 1s, such as 1m")
		}
		c.BackgroundEvery = d
	}
	rate := func(name string, r *api.RateLimit) {
		v := os.Getenv(name)
		if v == "" {
			return
		}
		n, err := strconv.ParseFloat(v, 64)
		if err != nil || n < 0 {
			problems = append(problems, name+" must be a number of requests per second, 0 or more (0 turns the limit off)")
			return
		}
		r.PerSecond, r.Burst = n, int(n*2)+1
	}
	rate("CROSSDOCK_RATE_LIMIT_USER", &c.Limits.User)
	rate("CROSSDOCK_RATE_LIMIT_SYSTEM", &c.Limits.System)
	if len(problems) > 0 {
		return c, errors.New("invalid configuration:\n  - " + strings.Join(problems, "\n  - "))
	}
	return c, nil
}

func cmdServe() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{ReplaceAttr: telemetry.RedactAttr}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownTrace, err := telemetry.Setup(ctx, "crossdock")
	if err != nil {
		return fmt.Errorf("telemetry: %w", err)
	}
	defer shutdownTrace(context.WithoutCancel(ctx)) //nolint:errcheck

	db, err := openDB(cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	a, err := auth.New(db, cfg.ServiceToken, cfg.SigningKey)
	if err != nil {
		return err
	}

	var opts []api.Option
	if len(cfg.Brokers) > 0 {
		enc, _ := delivery.NewEncryptor(cfg.EncryptionKey) // validated in loadConfig
		bus, err := delivery.NewKafkaBus(ctx, cfg.Brokers, "crossdock.events", 6)
		if err != nil {
			return err
		}
		defer bus.Close()
		svc := delivery.NewService(db, enc, cfg.AllowPrivateWH)
		opts = append(opts, api.WithDelivery(svc))

		go delivery.Relay(ctx, db, bus, 200*time.Millisecond, log)
		go func() {
			if err := delivery.RunFanout(ctx, db, bus); err != nil {
				log.Error("fanout stopped", "err", err)
			}
		}()
		go delivery.NewWorker(db, svc, delivery.WorkerConfig{Workers: cfg.DeliveryWorkers, AllowPrivate: cfg.AllowPrivateWH}, log).Run(ctx)
		log.Info("webhook delivery enabled", "brokers", cfg.Brokers, "workers", cfg.DeliveryWorkers)
	} else {
		// Phase 1 behaviour: outbox rows go to a log line.
		go outbox.Run(ctx, db, 500*time.Millisecond,
			func(e outbox.Event) error {
				log.Info("event", "id", e.ID, "topic", e.Topic, "key", e.Key)
				return nil
			},
			func(err error) { log.Error("outbox relay", "err", err) })
		log.Warn("webhook delivery disabled: set CROSSDOCK_BROKERS and CROSSDOCK_SECRET_ENCRYPTION_KEY")
	}

	go background(ctx, db, log, cfg.BackgroundEvery)

	srv := &http.Server{
		Addr: cfg.Listen, Handler: api.New(db, a, log, apispec.Spec, append(opts, api.WithLimits(cfg.Limits))...).Handler(),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("listening", "addr", cfg.Listen)
	select {
	case err := <-errc:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

// background schedules cycle counts and reconciles inbound receipts for every
// warehouse on a timer. Both are idempotent, so overlapping or repeated runs
// (or several API instances) do no harm.
func background(ctx context.Context, db *sql.DB, log *slog.Logger, every time.Duration) {
	store := inventory.NewStore(db)
	cs, rs, ss, sl := counting.NewService(db, store), reconcile.NewService(db), settlement.NewService(db), slotting.NewService(db, store)
	tick := func() {
		rows, err := db.QueryContext(ctx, `SELECT id FROM warehouses ORDER BY id`)
		if err != nil {
			log.Error("background: list warehouses", "err", err)
			return
		}
		var ws []inventory.WarehouseID
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				ws = append(ws, inventory.WarehouseID(id))
			}
		}
		rows.Close()
		for _, w := range ws {
			if n, err := cs.ScheduleCounts(ctx, w); err != nil {
				log.Error("background: schedule counts", "warehouse", w, "err", err)
			} else if n > 0 {
				log.Info("count tasks scheduled", "warehouse", w, "created", n)
			}
			if o, c, err := rs.Run(ctx, w); err != nil {
				log.Error("background: reconcile", "warehouse", w, "err", err)
			} else if o+c > 0 {
				log.Info("inbound reconciliation", "warehouse", w, "opened", o, "cleared", c)
			}
			if n, err := sl.ReplayPending(ctx, w); err != nil {
				log.Error("background: replay slotting proposals", "warehouse", w, "err", err)
			} else if n > 0 {
				log.Info("slotting proposals replayed", "warehouse", w, "count", n)
			}
			if n, err := ss.FindUnremitted(ctx, w); err != nil {
				log.Error("background: settlement scan", "warehouse", w, "err", err)
			} else if n > 0 {
				log.Info("unremitted COD cases opened", "warehouse", w, "opened", n)
			}
		}
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}
