package counting_test

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicholasgasior/crossdock/internal/counting"
	"github.com/nicholasgasior/crossdock/internal/ids"
	"github.com/nicholasgasior/crossdock/internal/inventory"
	"github.com/nicholasgasior/crossdock/internal/outbound"
	"github.com/nicholasgasior/crossdock/internal/tasks"
	"github.com/nicholasgasior/crossdock/internal/testdb"
	"github.com/nicholasgasior/crossdock/internal/wmstest"
)

// loadWorld is 10 pick locations, each holding one SKU, and 600 picks of 2 units
// waiting. Picks are always 2 units, so a real loss of 1 unit can never look like
// a pick.
type loadWorld struct {
	db     *sql.DB
	w      wmstest.World
	store  *inventory.Store
	svc    *counting.Service
	locs   []inventory.LocationCode
	skus   []inventory.SKUID
	lots   []inventory.LotID
}

func newLoadWorld(t *testing.T) *loadWorld {
	t.Helper()
	db := testdb.Open(t)
	db.SetMaxOpenConns(70) // stay under the server's connection limit
	store := inventory.NewStore(db)
	lw := &loadWorld{db: db, store: store, svc: counting.NewService(db, store)}
	lw.w = wmstest.Seed(t, db, 10)
	lw.locs = append(lw.locs, lw.w.Pick)
	for i := 1; i < 10; i++ {
		code := inventory.LocationCode(fmt.Sprintf("A01-%02d-A-1", i+1))
		wmstest.AddPickLocation(t, db, lw.w, code, 1000)
		lw.locs = append(lw.locs, code)
	}
	for i := 0; i < 10; i++ {
		lw.skus = append(lw.skus, lw.w.SKUs[i])
		lw.lots = append(lw.lots, wmstest.Stock(t, db, store, lw.w, lw.skus[i], lw.locs[i], 300))
	}
	t.Cleanup(func() { wmstest.CheckInvariants(t, db, store, lw.w.WID) })
	return lw
}

// releasePicks creates, allocates and waves 600 orders of 2 units, 60 per SKU.
func (lw *loadWorld) releasePicks(t *testing.T) int {
	t.Helper()
	ctx := context.Background()
	out := outbound.NewService(lw.db, lw.store)
	var ids []string
	for i := 0; i < 600; i++ {
		o, err := out.CreateOrder(ctx, lw.w.WID, fmt.Sprintf("SO-%d", i), []outbound.OrderLine{{SKU: lw.skus[i%10], Qty: 2}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, o.ID)
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 20)
	for _, id := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if st, err := out.Allocate(ctx, lw.w.WID, id); err != nil || st != "allocated" {
				t.Errorf("allocate = %q, %v", st, err)
			}
		}()
	}
	wg.Wait()
	picks := 0
	for i := 0; i < len(ids); i += 100 {
		w, err := out.ReleaseWave(ctx, lw.w.WID, ids[i:i+100])
		if err != nil {
			t.Fatal(err)
		}
		picks += w.PickTasks
	}
	return picks
}

// physical reads what is on the shelf right now, as an operator looking at it would.
func (lw *loadWorld) physical(loc inventory.LocationCode) int64 {
	var n int64
	if err := lw.db.QueryRow(`
		SELECT COALESCE(SUM(available+reserved+quarantined+damaged),0) FROM positions WHERE warehouse_id=$1 AND location=$2`,
		string(lw.w.WID), string(loc)).Scan(&n); err != nil {
		panic(err)
	}
	return n
}

type tally struct {
	counts, matched int
	byClass         map[counting.Class]int
	recounts        int
}

// run drives 100 pickers and one counter per location until every pick is done.
// miscountBy is subtracted from what each counter reports (0 for honest counters).
func (lw *loadWorld) run(t *testing.T, miscountBy int64) tally {
	t.Helper()
	ctx := context.Background()
	var done atomic.Bool
	var picked atomic.Int64
	var pickers sync.WaitGroup
	out := outbound.NewService(lw.db, lw.store)
	for p := 0; p < 100; p++ {
		pickers.Add(1)
		go func() {
			defer pickers.Done()
			for {
				tk, err := tasks.Claim(ctx, lw.db, lw.w.WID, fmt.Sprintf("picker-%d", p), []inventory.TaskKind{inventory.TaskPick})
				if err != nil {
					t.Errorf("claim: %v", err)
					return
				}
				if tk == nil {
					return
				}
				if err := out.CompletePick(ctx, lw.w.WID, tk.ID); err != nil {
					t.Errorf("pick: %v", err)
					return
				}
				picked.Add(1)
			}
		}()
	}

	var mu sync.Mutex
	tl := tally{byClass: map[counting.Class]int{}}
	var counters sync.WaitGroup
	for i := range lw.locs {
		counters.Add(1)
		go func() {
			defer counters.Done()
			rng := rand.New(rand.NewPCG(uint64(i), 7))
			for n := 0; !done.Load() || n < 3; n++ {
				tk, _, err := tasks.Create(ctx, lw.db, tasks.Task{WarehouseID: lw.w.WID, Kind: inventory.TaskCount, FromLocation: lw.locs[i],
					Ref: ids.New("load"), Reason: "schedule"})
				if err != nil {
					t.Errorf("count task: %v", err)
					return
				}
				if err := lw.svc.Start(ctx, lw.w.WID, tk.ID, "counter"); err != nil {
					t.Errorf("start: %v", err)
					return
				}
				time.Sleep(time.Duration(2+rng.IntN(20)) * time.Millisecond) // walking the aisle, pickers still working
				seen := lw.physical(lw.locs[i]) - miscountBy
				time.Sleep(time.Duration(1+rng.IntN(8)) * time.Millisecond)
				res, err := lw.svc.Submit(ctx, lw.w.WID, tk.ID, "counter",
					[]counting.Line{{SKU: string(lw.skus[i]), Lot: string(lw.lots[i]), Counted: seen}})
				if err != nil {
					t.Errorf("submit: %v", err)
					return
				}
				mu.Lock()
				tl.counts++
				tl.matched += res.Matched
				if res.Recount != "" {
					tl.recounts++
				}
				mu.Unlock()
			}
		}()
	}
	pickers.Wait()
	done.Store(true)
	counters.Wait()

	rows, err := lw.db.Query(`SELECT proposed, count(*) FROM variances WHERE warehouse_id=$1 GROUP BY 1`, string(lw.w.WID))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		var n int
		_ = rows.Scan(&c, &n)
		tl.byClass[counting.Class(c)] = n
	}
	if picked.Load() == 0 {
		t.Fatal("no picks ran")
	}
	return tl
}

// TestCountUnderLoadHasZeroFalseShrinkage is the Phase 3 exit criterion
// (spec 14.2, 15): 100 pickers confirm picks across 10 locations while cycle
// counts run on those locations. Every count agrees with the system or is
// classified timing. No recount is asked for and no adjustment posts.
func TestCountUnderLoadHasZeroFalseShrinkage(t *testing.T) {
	if testing.Short() {
		t.Skip("load scenario")
	}
	lw := newLoadWorld(t)
	if n := lw.releasePicks(t); n != 600 {
		t.Fatalf("pick tasks = %d, want 600", n)
	}
	tl := lw.run(t, 0)

	t.Logf("%d counts: %d keys matched, classifications %v, recounts requested %d", tl.counts, tl.matched, tl.byClass, tl.recounts)
	if tl.counts < 20 {
		t.Fatalf("only %d counts ran; the test is not exercising anything", tl.counts)
	}
	for class, n := range tl.byClass {
		if class != counting.Timing {
			t.Errorf("%d variance(s) classified %q under honest counting; only timing is acceptable", n, class)
		}
	}
	if tl.recounts != 0 {
		t.Errorf("%d recounts requested; a timing difference must not need one", tl.recounts)
	}
	var adj int
	_ = lw.db.QueryRow(`SELECT count(*) FROM movements WHERE warehouse_id=$1 AND kind='count_adjust'`, string(lw.w.WID)).Scan(&adj)
	if adj != 0 {
		t.Errorf("%d adjustments posted, want none", adj)
	}
	if tl.byClass[counting.Timing] == 0 {
		t.Log("note: no count overlapped a pick this run, so the timing path was not exercised")
	}
}

// TestRealLossIsNotHiddenByLoad is the other side: a counter who really is one
// unit short, in the middle of the same activity, is not waved through as timing.
func TestRealLossIsNotHiddenByLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("load scenario")
	}
	lw := newLoadWorld(t)
	lw.releasePicks(t)
	tl := lw.run(t, 1)

	if tl.counts < 20 {
		t.Fatalf("only %d counts ran", tl.counts)
	}
	if tl.byClass[counting.Timing] != 0 {
		t.Errorf("%d variance(s) of a 1 unit loss were called timing; picks are 2 units, they cannot explain it", tl.byClass[counting.Timing])
	}
	if tl.recounts != tl.counts {
		t.Errorf("recounts requested for %d of %d counts, want every one", tl.recounts, tl.counts)
	}
	var adj int
	_ = lw.db.QueryRow(`SELECT count(*) FROM movements WHERE warehouse_id=$1 AND kind='count_adjust'`, string(lw.w.WID)).Scan(&adj)
	if adj != 0 {
		t.Errorf("%d adjustments posted on a single count, want none", adj)
	}
}
