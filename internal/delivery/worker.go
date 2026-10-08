package delivery

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/nicholasgasior/crossdock/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// WorkerConfig holds the delivery policy (spec 7.2). Defaults match the spec.
type WorkerConfig struct {
	Workers          int           // concurrent endpoints being worked
	BackoffBase      time.Duration // 1s
	BackoffCap       time.Duration // 1h
	Budget           int           // attempts before dead letter (20, about three days)
	BreakerThreshold int           // consecutive failures that open the breaker (10)
	BreakerCooldown  time.Duration // how long it stays open (5m)
	Timeout          time.Duration // per request
	Poll             time.Duration // idle sleep
	Lease            time.Duration // endpoint lease; renewed per delivery
	AllowPrivate     bool          // allow loopback/private webhook targets (dev, tests)
	Now              func() time.Time
}

func (c *WorkerConfig) defaults() {
	set := func(p *time.Duration, v time.Duration) {
		if *p == 0 {
			*p = v
		}
	}
	if c.Workers == 0 {
		c.Workers = 16
	}
	if c.Budget == 0 {
		c.Budget = 20
	}
	if c.BreakerThreshold == 0 {
		c.BreakerThreshold = 10
	}
	set(&c.BackoffBase, time.Second)
	set(&c.BackoffCap, time.Hour)
	set(&c.BreakerCooldown, 5*time.Minute)
	set(&c.Timeout, 10*time.Second)
	set(&c.Poll, 250*time.Millisecond)
	set(&c.Lease, 30*time.Second)
	if c.Now == nil {
		c.Now = time.Now
	}
}

// Worker delivers pending deliveries over HTTP.
type Worker struct {
	db   *sql.DB
	svc  *Service
	cfg  WorkerConfig
	http *http.Client
	log  *slog.Logger
}

func NewWorker(db *sql.DB, svc *Service, cfg WorkerConfig, log *slog.Logger) *Worker {
	cfg.defaults()
	return &Worker{db: db, svc: svc, cfg: cfg, log: log, http: &http.Client{
		Timeout:       cfg.Timeout,
		Transport:     &http.Transport{DialContext: guardedDialer(cfg.AllowPrivate).DialContext, MaxIdleConnsPerHost: 4, DisableKeepAlives: false},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, // a redirect is a failure, never followed
	}}
}

// Backoff is exponential with full jitter: a random delay in [0, min(cap, base*2^attempt)).
func Backoff(base, cap time.Duration, attempt int, rnd func() float64) time.Duration {
	d := base
	for i := 0; i < attempt && d < cap; i++ {
		d *= 2
	}
	if d > cap {
		d = cap
	}
	return time.Duration(rnd() * float64(d))
}

// Run starts the worker pool and blocks until ctx ends.
func (w *Worker) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for i := 0; i < w.cfg.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				worked, err := w.RunOnce(ctx)
				if err != nil && ctx.Err() == nil {
					w.log.Error("delivery worker", "err", err)
				}
				if !worked {
					select {
					case <-ctx.Done():
					case <-time.After(w.cfg.Poll):
					}
				}
			}
		}()
	}
	wg.Wait()
}

type leased struct {
	wid, id, url, breaker string
	secrets               []string
	secretsAt             time.Time
}

// eligible is the shared "may this delivery go now" predicate: due, pending,
// and (for ordered events) no earlier undelivered original with the same key.
func eligible(now string) string {
	return `
	d.state = 'pending' AND d.next_attempt_at <= ` + now + `
	AND (d.replay_of <> '' OR d.ordering_key = '' OR NOT EXISTS (
	      SELECT 1 FROM deliveries p
	      WHERE p.warehouse_id = d.warehouse_id AND p.endpoint_id = d.endpoint_id
	        AND p.ordering_key = d.ordering_key AND p.state = 'pending' AND p.replay_of = '' AND p.event_id < d.event_id))`
}

// RunOnce takes one endpoint lease and drains what is due for it. It reports
// whether it found work. Tests call it directly for deterministic stepping.
func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	now := w.cfg.Now()
	ep, err := w.lease(ctx, now)
	if err != nil || ep == nil {
		return false, err
	}
	defer w.release(context.WithoutCancel(ctx), ep)

	// A breaker whose cooldown ended lets exactly one probe through.
	probe := ep.breaker != "closed"
	for ctx.Err() == nil {
		now = w.cfg.Now()
		limit := 20
		if probe {
			limit = 1
		}
		batch, err := w.due(ctx, ep, now, limit)
		if err != nil || len(batch) == 0 {
			return true, err
		}
		for _, d := range batch {
			ok, err := w.deliver(ctx, ep, d)
			if err != nil {
				return true, err
			}
			if probe {
				return true, nil
			}
			if !ok {
				if w.breakerOpen(ctx, ep) {
					return true, nil
				}
			}
		}
	}
	return true, nil
}

func (w *Worker) lease(ctx context.Context, now time.Time) (*leased, error) {
	var l leased
	// sqlok: eligible() inserts only a placeholder name
	err := w.db.QueryRowContext(ctx, `
		UPDATE endpoints SET lease_until = $2,
		       breaker = CASE WHEN breaker = 'open' THEN 'half_open' ELSE breaker END
		WHERE (warehouse_id, id) = (
		  SELECT e.warehouse_id, e.id FROM endpoints e
		  WHERE e.state = 'active'
		    AND (e.lease_until IS NULL OR e.lease_until < $1)
		    AND (e.breaker <> 'open' OR e.open_until <= $1)
		    AND EXISTS (SELECT 1 FROM deliveries d
		                WHERE d.warehouse_id = e.warehouse_id AND d.endpoint_id = e.id
		                  AND `+eligible("$1")+`)
		  ORDER BY random() LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING warehouse_id, id, url, breaker`, now, now.Add(w.cfg.Lease)).Scan(&l.wid, &l.id, &l.url, &l.breaker)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &l, err
}

func (w *Worker) release(ctx context.Context, ep *leased) {
	_, _ = w.db.ExecContext(ctx, `UPDATE endpoints SET lease_until = NULL WHERE warehouse_id=$1 AND id=$2`, ep.wid, ep.id)
}

type due struct {
	id, replayOf, topic, key, trace, wid string
	eventID                              int64
	attempts                             int
	payload                              json.RawMessage
	occurred                             time.Time
}

func (w *Worker) due(ctx context.Context, ep *leased, now time.Time, limit int) ([]due, error) {
	// sqlok: eligible() inserts only a placeholder name
	rows, err := w.db.QueryContext(ctx, `
		SELECT d.id, d.event_id, d.attempts, d.replay_of, ev.topic, ev.key, ev.payload, ev.occurred_at, ev.traceparent, ev.warehouse_id
		FROM deliveries d JOIN events ev ON ev.id = d.event_id
		WHERE d.warehouse_id = $1 AND d.endpoint_id = $2 AND `+eligible("$3")+`
		ORDER BY d.event_id, d.created_at LIMIT $4`, ep.wid, ep.id, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.eventID, &d.attempts, &d.replayOf, &d.topic, &d.key, &d.payload, &d.occurred, &d.trace, &d.wid); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// deliver makes one HTTP attempt and records the result. It returns whether
// the endpoint accepted the event.
func (w *Worker) deliver(ctx context.Context, ep *leased, d due) (bool, error) {
	now := w.cfg.Now()
	// Secrets are read once per few seconds per lease, not per delivery. If they
	// cannot be read or decrypted, the attempt fails like any other (backoff,
	// budget, dead letter) instead of spinning on the same delivery.
	var secretErr error
	if ep.secrets == nil || now.Sub(ep.secretsAt) > 5*time.Second {
		ep.secrets, secretErr = w.svc.signingSecrets(ctx, ep.wid, ep.id, now)
		if secretErr == nil && len(ep.secrets) == 0 {
			secretErr = errors.New("no valid signing secret")
		}
		ep.secretsAt = now
	}
	secrets := ep.secrets
	body, _ := json.Marshal(map[string]any{
		"id": "evt_" + strconv.FormatInt(d.eventID, 10), "type": d.topic, "occurred_at": d.occurred,
		"sequence": d.eventID, "warehouse_id": d.wid, "idempotency_key": "evt_" + strconv.FormatInt(d.eventID, 10),
		"data": d.payload,
	})

	spanCtx, span := telemetry.Tracer().Start(telemetry.WithTraceparent(ctx, d.trace), "webhook.deliver",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("endpoint.id", ep.id), attribute.String("event.topic", d.topic),
			attribute.Int64("event.sequence", d.eventID), attribute.Int("delivery.attempt", d.attempts+1)))
	defer span.End()

	start := time.Now()
	var (
		status      int
		respSnippet string
		callErr     = secretErr
	)
	if secretErr != nil {
		callErr = fmt.Errorf("signing secret unavailable: %w", secretErr)
	} else {
		status, respSnippet, callErr = w.post(spanCtx, ep.url, d, secrets, now, body)
	}
	elapsed := time.Since(start)
	ok := callErr == nil && status >= 200 && status < 300
	errText := ""
	if callErr != nil {
		errText = callErr.Error()
	} else if !ok {
		errText = "endpoint returned " + strconv.Itoa(status)
	}
	span.SetAttributes(attribute.Int("http.status_code", status))
	if !ok {
		span.SetStatus(codes.Error, errText)
	}

	// The attempt, the delivery state, the breaker and the lease renewal change in
	// one statement (one round trip, atomic).
	n := d.attempts + 1
	state, next := "delivered", now
	if !ok {
		state, next = "pending", now.Add(Backoff(w.cfg.BackoffBase, w.cfg.BackoffCap, n-1, rand.Float64))
		if n >= w.cfg.Budget {
			state = "dead"
		}
	}
	_, err := w.db.ExecContext(context.WithoutCancel(ctx), `
		WITH att AS (
		  INSERT INTO delivery_attempts (delivery_id, n, at, status, error, response, duration_ms)
		  VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING
		), upd AS (
		  UPDATE deliveries SET state=$8, attempts=$2, last_status=$4, last_error=$5, next_attempt_at=$9,
		         delivered_at = CASE WHEN $8 = 'delivered' THEN $3 ELSE delivered_at END
		  WHERE id=$1
		)
		UPDATE endpoints SET
		    lease_until = $13,
		    consecutive_failures = CASE WHEN $10 THEN 0 ELSE consecutive_failures + 1 END,
		    breaker = CASE WHEN $10 THEN 'closed'
		                   WHEN breaker = 'half_open' OR consecutive_failures + 1 >= $11 THEN 'open' ELSE breaker END,
		    open_until = CASE WHEN $10 THEN NULL
		                      WHEN breaker = 'half_open' OR consecutive_failures + 1 >= $11 THEN $12 ELSE open_until END
		WHERE warehouse_id=$14 AND id=$15`,
		d.id, n, now, status, errText, respSnippet, elapsed.Milliseconds(), state, next, ok,
		w.cfg.BreakerThreshold, w.cfg.Now().Add(w.cfg.BreakerCooldown), w.cfg.Now().Add(w.cfg.Lease), ep.wid, ep.id)
	return ok, err
}

func (w *Worker) post(ctx context.Context, url string, d due, secrets []string, ts time.Time, body []byte) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Crossdock-Webhooks/1")
	req.Header.Set("X-Crossdock-Event-Id", "evt_"+strconv.FormatInt(d.eventID, 10))
	req.Header.Set("X-Crossdock-Timestamp", strconv.FormatInt(ts.Unix(), 10))
	req.Header.Set("X-Crossdock-Signature", SignatureHeader(secrets, ts, body))
	if d.replayOf != "" {
		req.Header.Set("X-Crossdock-Replay-Of", "evt_"+strconv.FormatInt(d.eventID, 10))
	}
	telemetry.Inject(ctx, req.Header.Set)
	resp, err := w.http.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return resp.StatusCode, string(snippet), nil
}

func (w *Worker) breakerOpen(ctx context.Context, ep *leased) bool {
	var b string
	if err := w.db.QueryRowContext(ctx, `SELECT breaker FROM endpoints WHERE warehouse_id=$1 AND id=$2`, ep.wid, ep.id).Scan(&b); err != nil {
		return true
	}
	return b == "open"
}
