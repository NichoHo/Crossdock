// webhook-load runs the "webhook fan-out" load scenario (spec 15) against a
// real PostgreSQL and a real Kafka-compatible broker (Redpanda), with in-process
// receivers standing in for customers.
//
//	100 endpoints: 10 fail 30% of requests, 1 is permanently down, 89 are healthy.
//	N events (default 100, so 10,000 deliveries), grouped into shipments.
//
// It checks, and exits non-zero if any fails:
//  1. per-shipment ordering at every endpoint,
//  2. every event is accepted exactly once by every endpoint that is not down,
//  3. the healthy endpoints finish inside their SLA while others fail,
//  4. dead-letter depth equals the number of events sent to the dead endpoint.
//
// Usage: CROSSDOCK_DATABASE_URL=... CROSSDOCK_BROKERS=127.0.0.1:19092 go run ./cmd/webhook-load -events 100
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	mrand "math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	_ "github.com/lib/pq"
	"github.com/nicholasgasior/crossdock/internal/delivery"
	"github.com/nicholasgasior/crossdock/internal/ids"
	"github.com/nicholasgasior/crossdock/internal/outbox"
)

type receiver struct {
	srv  *httptest.Server
	kind string // healthy | flaky | down
	mu   sync.Mutex
	got  []hit
	rng  *mrand.Rand
}

type hit struct {
	seq    int64
	key    string
	status int
	at     time.Time
}

var keepData bool

func main() {
	events := flag.Int("events", 100, "events to publish; each is delivered to every endpoint")
	shipments := flag.Int("shipments", 0, "distinct shipments (ordering keys); default events/4")
	seed := flag.Uint64("seed", 1, "random seed")
	sla := flag.Duration("healthy-sla", 30*time.Second, "time within which healthy endpoints must finish")
	timeout := flag.Duration("timeout", 5*time.Minute, "overall limit")
	out := flag.String("out", "", "write a JSON result file here")
	keep := flag.Bool("keep", false, "keep the generated warehouse and its rows after the run")
	flag.Parse()
	keepData = *keep
	if *shipments == 0 {
		*shipments = max(1, *events/4)
	}
	if err := run(*events, *shipments, *seed, *sla, *timeout, *out); err != nil {
		fmt.Fprintln(os.Stderr, "webhook-load: FAIL:", err)
		os.Exit(1)
	}
}

func run(nEvents, nShipments int, seed uint64, sla, timeout time.Duration, outPath string) error {
	dbURL, brokers := os.Getenv("CROSSDOCK_DATABASE_URL"), os.Getenv("CROSSDOCK_BROKERS")
	if dbURL == "" || brokers == "" {
		return fmt.Errorf("set CROSSDOCK_DATABASE_URL and CROSSDOCK_BROKERS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(40)
	db.SetMaxIdleConns(40) // default is 2: reconnecting per query exhausts ephemeral ports

	var k [32]byte
	_, _ = rand.Read(k[:])
	enc, _ := delivery.NewEncryptor(base64.StdEncoding.EncodeToString(k[:]))
	svc := delivery.NewService(db, enc, true)
	wid := ids.New("wh-load")
	if _, err := db.ExecContext(ctx, `INSERT INTO warehouses (id, name) VALUES ($1,'load')`, wid); err != nil {
		return err
	}
	if !keepData {
		// Leave no endpoints behind: a real server would try to deliver to them.
		defer func() {
			bg := context.Background()
			for _, q := range []string{`DELETE FROM events WHERE warehouse_id=$1`, `DELETE FROM endpoints WHERE warehouse_id=$1`,
				`DELETE FROM outbox WHERE warehouse_id=$1`, `DELETE FROM warehouses WHERE id=$1`} {
				// sqlok: each q is a constant from the list above
				_, _ = db.ExecContext(bg, q, wid)
			}
		}()
	}

	rng := mrand.New(mrand.NewPCG(seed, 99))
	var recvs []*receiver
	for i := 0; i < 100; i++ {
		kind := "healthy"
		switch {
		case i < 10:
			kind = "flaky"
		case i == 10:
			kind = "down"
		}
		r := &receiver{kind: kind, rng: mrand.New(mrand.NewPCG(seed, uint64(i)))}
		r.srv = httptest.NewServer(http.HandlerFunc(r.serve))
		defer r.srv.Close()
		recvs = append(recvs, r)
		if _, _, err := svc.Create(ctx, wid, r.srv.URL, []string{"*"}); err != nil {
			return err
		}
	}

	topic := fmt.Sprintf("crossdock.load.%d", time.Now().UnixNano())
	bus, err := delivery.NewKafkaBus(ctx, strings.Split(brokers, ","), topic, 6)
	if err != nil {
		return err
	}
	defer bus.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Publish events: shipment keys interleaved at random, each key in order.
	for i := 0; i < nEvents; i++ {
		key := fmt.Sprintf("shp-%d", rng.IntN(nShipments))
		if err := outbox.Emit(ctx, db, wid, "shipment.dispatched", key, map[string]any{"key": key, "n": i}); err != nil {
			return err
		}
	}

	start := time.Now()
	go delivery.Relay(ctx, db, bus, 50*time.Millisecond, log)
	go func() { _ = delivery.RunFanout(ctx, db, bus) }()
	cfg := delivery.WorkerConfig{
		Workers: 32, BackoffBase: 20 * time.Millisecond, BackoffCap: 500 * time.Millisecond, Budget: 12,
		BreakerThreshold: 1 << 30, // the dead endpoint must reach its budget, not trip the breaker
		Poll:             10 * time.Millisecond, AllowPrivate: true,
	}
	go delivery.NewWorker(db, svc, cfg, log).Run(ctx)

	// A new consumer group takes a while to join the broker. Time the work from the
	// first fanned-out event, and report the join separately.
	for {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE warehouse_id=$1`, wid).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("no event reached the fan-out consumer")
		case <-time.After(50 * time.Millisecond):
		}
	}
	busStartup := time.Since(start)
	start = time.Now()

	want := nEvents * len(recvs)
	var settled int
	for {
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM deliveries WHERE warehouse_id=$1 AND state IN ('delivered','dead')`, wid).Scan(&settled); err != nil {
			return err
		}
		if settled >= want {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out: %d of %d deliveries settled", settled, want)
		case <-time.After(200 * time.Millisecond):
		}
	}
	total := time.Since(start)

	// ---- checks ----
	var failures []string
	fail := func(f string, a ...any) { failures = append(failures, fmt.Sprintf(f, a...)) }

	var healthyDone, flakyDone time.Duration
	for i, r := range recvs {
		r.mu.Lock()
		last := map[string]int64{}
		accepted := map[int64]int{}
		var lastAt time.Time
		for _, h := range r.got {
			if h.status/100 != 2 {
				continue
			}
			accepted[h.seq]++
			if h.seq <= last[h.key] {
				fail("endpoint %d (%s): shipment %s event %d arrived after %d", i, r.kind, h.key, h.seq, last[h.key])
			}
			last[h.key] = h.seq
			if h.at.After(lastAt) {
				lastAt = h.at
			}
		}
		r.mu.Unlock()
		switch r.kind {
		case "down":
			if len(accepted) != 0 {
				fail("down endpoint accepted %d events", len(accepted))
			}
		default:
			if len(accepted) != nEvents {
				fail("endpoint %d (%s): accepted %d distinct events, want %d", i, r.kind, len(accepted), nEvents)
			}
			for seq, n := range accepted {
				if n != 1 {
					fail("endpoint %d: event %d accepted %d times", i, seq, n)
				}
			}
		}
		if d := lastAt.Sub(start); r.kind == "healthy" && d > healthyDone {
			healthyDone = d
		} else if r.kind == "flaky" && d > flakyDone {
			flakyDone = d
		}
	}
	if healthyDone > sla {
		fail("healthy endpoints finished after %v, SLA %v", healthyDone, sla)
	}
	var dead, deadElsewhere int
	_ = db.QueryRowContext(ctx, `SELECT count(*) FROM deliveries WHERE warehouse_id=$1 AND state='dead'`, wid).Scan(&dead)
	_ = db.QueryRowContext(ctx, `
		SELECT count(*) FROM deliveries d JOIN endpoints e ON e.id=d.endpoint_id AND e.warehouse_id=d.warehouse_id
		WHERE d.warehouse_id=$1 AND d.state='dead' AND e.url <> $2`, wid, recvs[10].srv.URL).Scan(&deadElsewhere)
	if dead != nEvents || deadElsewhere != 0 {
		fail("dead letters = %d (%d outside the down endpoint), want exactly %d", dead, deadElsewhere, nEvents)
	}

	var reqs, errs int
	for _, r := range recvs {
		for _, h := range r.got {
			reqs++
			if h.status/100 != 2 {
				errs++
			}
		}
	}
	res := map[string]any{
		"scenario": "webhook fan-out", "date": time.Now().UTC().Format(time.RFC3339), "seed": seed,
		"host": map[string]any{"cpus": runtime.NumCPU(), "os": runtime.GOOS, "go": runtime.Version()},
		"endpoints": len(recvs), "events": nEvents, "shipments": nShipments, "deliveries": want,
		"http_requests": reqs, "http_failures": errs, "dead_letters": dead,
		"bus_startup_seconds": busStartup.Seconds(), "total_seconds": total.Seconds(), "healthy_done_seconds": healthyDone.Seconds(), "flaky_done_seconds": flakyDone.Seconds(),
		"deliveries_per_second": float64(want) / total.Seconds(), "passed": len(failures) == 0, "failures": failures,
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(b))
	if outPath != "" {
		if err := os.WriteFile(outPath, append(b, '\n'), 0o644); err != nil {
			return err
		}
	}
	if len(failures) > 0 {
		sort.Strings(failures)
		return fmt.Errorf("%d check(s) failed, first: %s", len(failures), failures[0])
	}
	return nil
}

func (r *receiver) serve(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	var env struct {
		Seq  int64 `json:"sequence"`
		Data struct {
			Key string `json:"key"`
		} `json:"data"`
	}
	_ = json.Unmarshal(body, &env)
	status := http.StatusOK
	r.mu.Lock()
	switch r.kind {
	case "down":
		status = http.StatusInternalServerError
	case "flaky":
		if r.rng.Float64() < 0.30 {
			status = http.StatusInternalServerError
		}
	}
	r.got = append(r.got, hit{seq: env.Seq, key: env.Data.Key, status: status, at: time.Now()})
	r.mu.Unlock()
	w.WriteHeader(status)
}
