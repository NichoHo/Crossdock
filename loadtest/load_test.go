//go:build load

// Load scenarios from spec 15, driven over HTTP against a running stack:
//
//	CROSSDOCK_API_URL, CROSSDOCK_SERVICE_TOKEN, CROSSDOCK_IDENTITY_SIGNING_KEY, CROSSDOCK_DATABASE_URL
//	go test -tags load -count=1 -timeout 30m -v ./loadtest
//
// Each scenario seeds its own warehouse, then checks the invariants (conservation,
// projection fidelity, task coupling) after the load stops. Start the API with
// CROSSDOCK_RATE_LIMIT_USER=0: the generator acts as one user and would hit the rate limit. Results go to
// loadtest/results/. Scale knobs: LOAD_OPERATORS, LOAD_SECONDS.
// The webhook fan-out scenario is cmd/webhook-load; count under load is
// internal/counting/load_test.go (see the README for why they are separate).
package loadtest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	mrand "math/rand/v2"
	"os"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/nicholasgasior/crossdock/internal/auth"
	"github.com/nicholasgasior/crossdock/internal/inventory"
	"github.com/nicholasgasior/crossdock/internal/synth"
	"github.com/nicholasgasior/crossdock/internal/wmstest"
)

type rig struct {
	t     *testing.T
	db    *sql.DB
	store *inventory.Store
	w     wmstest.World
	c     *synth.Client
}

func env(t *testing.T, k string) string {
	v := os.Getenv(k)
	if v == "" {
		t.Skipf("%s not set", k)
	}
	return v
}

func envInt(k string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(k)); err == nil && n > 0 {
		return n
	}
	return def
}

// newRig seeds a warehouse with nSKU SKUs, each in its own pick location holding qty units.
func newRig(t *testing.T, nSKU int, qty int64) *rig {
	db, err := sql.Open("postgres", env(t, "CROSSDOCK_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { db.Close() })
	store := inventory.NewStore(db)
	w := wmstest.Seed(t, db, nSKU)
	for i, sku := range w.SKUs {
		loc := inventory.LocationCode(fmt.Sprintf("A%02d-%02d-A-1", 2+i/20, 1+i%20))
		wmstest.AddPickLocation(t, db, w, loc, 100000)
		wmstest.Stock(t, db, store, w, sku, loc, qty)
	}
	c := synth.New(env(t, "CROSSDOCK_API_URL"), env(t, "CROSSDOCK_SERVICE_TOKEN"), env(t, "CROSSDOCK_IDENTITY_SIGNING_KEY"), string(w.WID))
	return &rig{t, db, store, w, c}
}

func (r *rig) sup(method, path string, in, out any) (time.Duration, error) {
	_, d, err := r.c.Do(context.Background(), "load-supervisor", auth.RoleSupervisor, method, path, in, out)
	return d, err
}

// parallel runs fn(i) for i in [0,n) with at most width at once.
func parallel(n, width int, fn func(i int)) {
	sem := make(chan struct{}, width)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		sem <- struct{}{}
		wg.Add(1)
		go func(i int) { defer func() { <-sem; wg.Done() }(); fn(i) }(i)
	}
	wg.Wait()
}

type lat struct {
	mu sync.Mutex
	ms []float64
}

func (l *lat) add(d time.Duration) {
	l.mu.Lock()
	l.ms = append(l.ms, float64(d.Microseconds())/1000)
	l.mu.Unlock()
}

func (l *lat) pct(p float64) float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.ms) == 0 {
		return 0
	}
	s := append([]float64(nil), l.ms...)
	sort.Float64s(s)
	return s[min(len(s)-1, int(float64(len(s))*p))]
}

func (r *rig) report(name string, v map[string]any) {
	v["scenario"], v["warehouse"] = name, string(r.w.WID)
	v["when"] = time.Now().UTC().Format(time.RFC3339)
	v["host"] = fmt.Sprintf("%s/%s, %d CPUs, Go %s", runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), runtime.Version())
	v["commit"] = os.Getenv("LOAD_COMMIT")
	b, _ := json.MarshalIndent(v, "", "  ")
	_ = os.MkdirAll("results", 0o755)
	_ = os.WriteFile("results/"+name+"-latest.json", b, 0o644)
	r.t.Logf("%s: %s", name, b)
}

// makeOrders creates n single-line orders of one unit, cycling through the SKUs, and
// allocates each. It returns the order ids.
func (r *rig) makeOrders(n int, alloc *lat) []string {
	ids := make([]string, n)
	var bad atomic.Int64
	// Seeded, so a run can be repeated. SKUs are shuffled so that the next tasks in the
	// queue sit on different positions, as they do when operators work different waves.
	rng := mrand.New(mrand.NewPCG(7, 11))
	pick := make([]int, n)
	for i := range pick {
		pick[i] = rng.IntN(len(r.w.SKUs))
	}
	parallel(n, 48, func(i int) {
		var o struct{ ID string }
		if _, err := r.sup("POST", "/orders", map[string]any{"ref": fmt.Sprintf("LD-%s-%d", r.w.WID, i), "lines": []map[string]any{{"sku": string(r.w.SKUs[pick[i]]), "qty": 1}}}, &o); err != nil {
			bad.Add(1)
			return
		}
		d, err := r.sup("POST", "/orders/"+o.ID+"/allocate", nil, nil)
		if err != nil {
			bad.Add(1)
			return
		}
		if alloc != nil {
			alloc.add(d)
		}
		ids[i] = o.ID
	})
	if bad.Load() > 0 {
		r.t.Fatalf("%d of %d orders failed to create or allocate", bad.Load(), n)
	}
	return ids
}

func (r *rig) wave(orderIDs []string) {
	const size = 20 // small waves: the queue interleaves their routes, so operators land on different positions
	for i := 0; i < len(orderIDs); i += size {
		if _, err := r.sup("POST", "/waves", map[string]any{"order_ids": orderIDs[i:min(i+size, len(orderIDs))]}, nil); err != nil {
			r.t.Fatal(err)
		}
	}
}

func (r *rig) invariants() { wmstest.CheckInvariants(r.t, r.db, r.store, r.w.WID) }

// Allocation burst: 1,000 orders in 10 seconds, contending for 50 SKUs.
func TestAllocationBurst(t *testing.T) {
	r := newRig(t, 50, 40) // 2,000 units of supply against about 2,000 units of demand, unevenly spread
	const orders = 1000
	var alloc lat
	var bad atomic.Int64
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < orders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Skew demand onto a few SKUs so the same positions are contended.
			sku := r.w.SKUs[(i*i)%len(r.w.SKUs)]
			var o struct{ ID string }
			if _, err := r.sup("POST", "/orders", map[string]any{"ref": fmt.Sprintf("BURST-%s-%d", r.w.WID, i), "lines": []map[string]any{{"sku": string(sku), "qty": 1 + i%3}}}, &o); err != nil {
				bad.Add(1)
				return
			}
			d, err := r.sup("POST", "/orders/"+o.ID+"/allocate", nil, nil)
			if err != nil {
				bad.Add(1)
				return
			}
			alloc.add(d)
		}(i)
		time.Sleep(10 * time.Millisecond) // 1,000 orders over 10 seconds
	}
	wg.Wait()
	elapsed := time.Since(start)

	states := map[string]int{}
	rows, err := r.db.Query(`SELECT state, count(*) FROM orders WHERE warehouse_id=$1 GROUP BY 1`, string(r.w.WID))
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		var n int
		_ = rows.Scan(&s, &n)
		states[s] = n
	}
	rows.Close()
	r.report("allocation-burst", map[string]any{"orders": orders, "seconds": elapsed.Seconds(), "errors": bad.Load(), "order_states": states,
		"allocate_p50_ms": alloc.pct(.5), "allocate_p99_ms": alloc.pct(.99)})

	if bad.Load() != 0 {
		t.Errorf("%d requests failed", bad.Load())
	}
	if states["new"] != 0 {
		t.Errorf("%d orders left unallocated (state new)", states["new"])
	}
	if elapsed > 40*time.Second {
		t.Errorf("burst took %v, want every order settled within 30s of the last submission", elapsed)
	}
	if p := alloc.pct(.99); p > 250 {
		t.Errorf("p99 reservation latency %.0f ms, want under 250", p)
	}
	r.invariants()
}

// Pick storm: LOAD_OPERATORS operators confirm one pick per second for LOAD_SECONDS across LOAD_SKUS
// SKUs. Picks of one SKU queue on its position row, so a catalogue much smaller than the floor
// (LOAD_SKUS=50) measures that hot-position limit instead of the platform's general capacity.
func TestPickStorm(t *testing.T) {
	ops, secs, skus := envInt("LOAD_OPERATORS", 100), envInt("LOAD_SECONDS", 60), envInt("LOAD_SKUS", 500)
	r := newRig(t, skus, int64(ops*secs/skus*2+100))
	r.wave(r.makeOrders(ops*secs*12/10, nil))

	var confirm, claim lat
	var errs, idle, done atomic.Int64
	deadline := time.Now().Add(time.Duration(secs) * time.Second)
	var wg sync.WaitGroup
	for i := 0; i < ops; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			user := fmt.Sprintf("storm-%03d", i)
			tick := time.NewTicker(time.Second)
			defer tick.Stop()
			time.Sleep(time.Duration(i) * time.Second / time.Duration(ops)) // spread the first second
			for time.Now().Before(deadline) {
				tk, d, err := r.c.Claim(context.Background(), user, "pick")
				claim.add(d)
				if err != nil {
					errs.Add(1)
				} else if tk == nil {
					idle.Add(1)
				} else {
					res, d, err := r.c.Commands(context.Background(), user, []synth.Command{{Key: "storm-" + tk.ID, Type: "pick", Payload: map[string]any{"task_id": tk.ID}}})
					confirm.add(d)
					if err != nil || len(res) != 1 || res[0].Status != "applied" {
						errs.Add(1)
						t.Logf("confirm %s: %v %+v", tk.ID, err, res)
					} else {
						done.Add(1)
					}
				}
				<-tick.C
			}
		}(i)
	}
	wg.Wait()
	r.report(fmt.Sprintf("pick-storm-%dskus", skus), map[string]any{"operators": ops, "skus": skus, "seconds": secs, "picks_confirmed": done.Load(), "errors": errs.Load(), "idle_claims": idle.Load(),
		"confirm_p50_ms": confirm.pct(.5), "confirm_p99_ms": confirm.pct(.99), "claim_p99_ms": claim.pct(.99)})

	if errs.Load() != 0 {
		t.Errorf("%d errors during the storm (reservation conflicts must not surface)", errs.Load())
	}
	if want := int64(ops*secs) * 9 / 10; done.Load() < want {
		t.Errorf("only %d picks confirmed, want at least %d (one per operator per second)", done.Load(), want)
	}
	if p := confirm.pct(.99); p > 200 {
		t.Errorf("p99 confirm latency %.0f ms, want under 200", p)
	}
	r.invariants()
}

// Offline sync wave: 50 scanners each sync 100 queued commands at once, plus two they must not be allowed to run.
func TestOfflineSyncWave(t *testing.T) {
	const scanners, queued = 50, 100
	r := newRig(t, 50, scanners*queued/50*2+100)
	r.wave(r.makeOrders(scanners*queued, nil))

	// Each scanner picked up its tasks on the floor and is now offline with a queue.
	tasks := make([][]*synth.Task, scanners)
	parallel(scanners, scanners, func(s int) {
		for len(tasks[s]) < queued {
			tk, _, err := r.c.Claim(context.Background(), fmt.Sprintf("scan-%02d", s), "pick")
			if err != nil || tk == nil {
				t.Errorf("scanner %d could not claim task %d: %v", s, len(tasks[s]), err)
				return
			}
			tasks[s] = append(tasks[s], tk)
		}
	})
	if t.Failed() {
		t.FailNow()
	}

	var applied, dup, rejected, noReason atomic.Int64
	var calls lat
	start := time.Now()
	parallel(scanners, scanners, func(s int) {
		user := fmt.Sprintf("scan-%02d", s)
		var batch []synth.Command
		for _, tk := range tasks[s] {
			batch = append(batch, synth.Command{Key: "off-" + tk.ID, Type: "pick", Payload: map[string]any{"task_id": tk.ID}})
		}
		// A task that belongs to the next scanner (a conflict) and a command sent twice (a retry).
		other := tasks[(s+1)%scanners][0]
		batch = append(batch, synth.Command{Key: "off-bad-" + user, Type: "pick", Payload: map[string]any{"task_id": other.ID}}, batch[0])
		res, d, err := r.c.Commands(context.Background(), user, batch)
		calls.add(d)
		if err != nil {
			t.Errorf("%s: %v", user, err)
			return
		}
		for _, x := range res {
			switch x.Status {
			case "applied":
				applied.Add(1)
			case "duplicate":
				dup.Add(1)
			case "rejected":
				rejected.Add(1)
				if x.Reason == "" {
					noReason.Add(1)
				}
			}
		}
	})
	elapsed := time.Since(start)
	r.report("offline-sync-wave", map[string]any{"scanners": scanners, "queued_each": queued, "seconds": elapsed.Seconds(), "applied": applied.Load(),
		"duplicates": dup.Load(), "rejected": rejected.Load(), "batch_p99_ms": calls.pct(.99)})

	if applied.Load() != scanners*queued {
		t.Errorf("applied %d, want %d", applied.Load(), scanners*queued)
	}
	if rejected.Load() != scanners || noReason.Load() != 0 {
		t.Errorf("rejected %d (want %d, one conflict per scanner), %d without a reason", rejected.Load(), scanners, noReason.Load())
	}
	if dup.Load() != scanners {
		t.Errorf("duplicates %d, want %d", dup.Load(), scanners)
	}
	if elapsed > 60*time.Second {
		t.Errorf("took %v, want under 60s", elapsed)
	}
	r.invariants()
}
