package synth

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync/atomic"
	"time"

	"github.com/nicholasgasior/crossdock/internal/auth"
)

// Shift is a scripted working day: a supervisor-side producer that receives
// stock, takes orders, releases waves, ships, and operators
// who claim tasks and confirm them as they would on the scanner (spec 13.5).
type Shift struct {
	C         *Client
	Operators int
	// Every is the producer's tick. Each tick receives one delivery and takes a few orders.
	Every   time.Duration
	Seed    uint64
	Log     *slog.Logger
	Done    atomic.Int64 // tasks confirmed
	Shipped atomic.Int64
	Errors  atomic.Int64
}

type listOf[T any] struct {
	Items []T `json:"items"`
}

// Run works until ctx ends. Errors are counted and logged, never fatal: a shift
// that stops on the first hiccup would not tell us how the platform copes.
func (s *Shift) Run(ctx context.Context) error {
	var cat struct {
		SKUs []struct {
			ID string `json:"id"`
		} `json:"skus"`
	}
	if _, _, err := s.C.Do(ctx, "synth-producer", auth.RoleSupervisor, "GET", "/catalogue", nil, &cat); err != nil {
		return fmt.Errorf("catalogue: %w", err)
	}
	if len(cat.SKUs) == 0 {
		return fmt.Errorf("warehouse %s has no SKUs: run crossdock generate first", s.C.WID)
	}
	skus := make([]string, len(cat.SKUs))
	for i, k := range cat.SKUs {
		skus[i] = k.ID
	}
	for i := 0; i < s.Operators; i++ {
		go s.operator(ctx, fmt.Sprintf("synth-op-%d", i+1), rand.New(rand.NewPCG(s.Seed, uint64(i))))
	}
	rng := rand.New(rand.NewPCG(s.Seed, 99))
	tick := time.NewTicker(s.Every)
	defer tick.Stop()
	for n := 0; ; n++ {
		s.produce(ctx, rng, skus, n)
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

func (s *Shift) fail(what string, err error) {
	if err != nil {
		s.Errors.Add(1)
		s.Log.Warn("synthetic step failed", "step", what, "err", err.Error())
	}
}

func (s *Shift) produce(ctx context.Context, rng *rand.Rand, skus []string, n int) {
	sup := func(method, path string, in, out any, hdr ...string) error {
		_, _, err := s.C.Do(ctx, "synth-producer", auth.RoleSupervisor, method, path, in, out, hdr...)
		return err
	}
	stamp := fmt.Sprintf("%d-%d", time.Now().UnixMilli(), n)

	// A delivery arrives and is received at the dock.
	sku := skus[rng.IntN(len(skus))]
	qty := 20 + rng.IntN(40)
	var asn struct {
		Lines []struct {
			ID string `json:"id"`
		} `json:"lines"`
	}
	if err := sup("POST", "/asns", map[string]any{"ref": "SYN-ASN-" + stamp, "lines": []map[string]any{{"sku": sku, "expected_qty": qty}}}, &asn); err != nil || len(asn.Lines) == 0 {
		s.fail("asn", err)
		return
	}
	s.fail("receive", sup("POST", "/receipts", map[string]any{"asn_line_id": asn.Lines[0].ID, "qty": qty, "location": "A00-00-A-1"}, nil, "Idempotency-Key", "syn-rcv-"+stamp))

	// Customers order. Stock may be short; the platform allocates what it can.
	for i := 0; i < 1+rng.IntN(3); i++ {
		o := skus[rng.IntN(len(skus))]
		s.fail("order", sup("POST", "/orders", map[string]any{"ref": fmt.Sprintf("SYN-SO-%s-%d", stamp, i), "lines": []map[string]any{{"sku": o, "qty": 1 + rng.IntN(3)}}}, nil))
	}
	s.fail("sweep", sup("POST", "/allocation-sweeps", nil, nil))

	var allocated listOf[struct {
		ID string `json:"id"`
	}]
	if err := sup("GET", "/orders?state=allocated&limit=20", nil, &allocated); err == nil && len(allocated.Items) > 0 {
		ids := make([]string, len(allocated.Items))
		for i, o := range allocated.Items {
			ids[i] = o.ID
		}
		s.fail("wave", sup("POST", "/waves", map[string]any{"order_ids": ids}, nil))
	}

	var packed listOf[struct {
		ID string `json:"id"`
	}]
	if err := sup("GET", "/orders?state=packed&limit=20", nil, &packed); err == nil {
		for _, o := range packed.Items {
			cod := 0
			if rng.IntN(3) == 0 {
				cod = 50000 + rng.IntN(300000)
			}
			if sup("POST", "/orders/"+o.ID+"/ship", map[string]any{"carrier": "JNT", "tracking": "SYN" + o.ID, "cod_amount_minor": cod}, nil) == nil {
				s.Shipped.Add(1)
			}
		}
	}
}

// operator claims and confirms tasks at a human pace.
func (s *Shift) operator(ctx context.Context, user string, rng *rand.Rand) {
	for ctx.Err() == nil {
		t, _, err := s.C.Claim(ctx, user, "putaway", "pick", "pack", "count")
		if err != nil || t == nil {
			s.fail("claim", err)
			sleep(ctx, time.Second+time.Duration(rng.IntN(2000))*time.Millisecond)
			continue
		}
		sleep(ctx, time.Duration(500+rng.IntN(1500))*time.Millisecond) // walk and scan
		if t.Kind == "count" {
			s.count(ctx, user, t, rng)
			continue
		}
		payload := map[string]any{"task_id": t.ID}
		if t.Kind == "putaway" {
			payload["destination"] = t.ToLocation
		}
		res, _, err := s.C.Commands(ctx, user, []Command{{Key: "syn-" + t.ID, Type: t.Kind, Payload: payload}})
		switch {
		case err != nil:
			s.fail("confirm", err)
		case len(res) == 1 && res[0].Status == "rejected":
			s.fail("confirm", fmt.Errorf("%s %s rejected: %s", t.Kind, t.ID, res[0].Reason))
		default:
			s.Done.Add(1)
		}
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// count walks to the location, reads what is on the shelf and reports it blind.
// Now and then the operator miscounts by one, which is how variances get made.
func (s *Shift) count(ctx context.Context, user string, t *Task, rng *rand.Rand) {
	var sheet listOf[struct{ SKU, Lot, LPN string }]
	var stock listOf[struct {
		SKU, Lot, LPN                                  string
		Available, Reserved, Quarantined, Damaged int64
	}]
	_, _, err1 := s.C.Do(ctx, user, auth.RoleOperator, "GET", "/tasks/"+t.ID+"/count-sheet", nil, &sheet)
	_, _, err2 := s.C.Do(ctx, user, auth.RoleOperator, "GET", "/stock?location="+t.FromLocation, nil, &stock)
	s.fail("count-read", firstErr(err1, err2))
	if err1 != nil || err2 != nil {
		return
	}
	if res, _, err := s.C.Commands(ctx, user, []Command{{Key: "syn-start-" + t.ID, Type: "count_start", Payload: map[string]any{"task_id": t.ID}}}); err != nil || res[0].Status == "rejected" {
		s.fail("count-start", firstErr(err, rejected(res)))
		return
	}
	sleep(ctx, time.Duration(1000+rng.IntN(2000))*time.Millisecond)
	lines := []map[string]any{}
	for _, l := range sheet.Items {
		var counted, damaged int64
		for _, p := range stock.Items {
			if p.SKU == l.SKU && p.Lot == l.Lot && p.LPN == l.LPN {
				counted, damaged = p.Available+p.Reserved+p.Quarantined, p.Damaged
			}
		}
		if rng.IntN(25) == 0 && counted > 0 {
			counted--
		}
		lines = append(lines, map[string]any{"sku": l.SKU, "lot": l.Lot, "lpn": l.LPN, "counted": counted, "damaged": damaged})
	}
	res, _, err := s.C.Commands(ctx, user, []Command{{Key: "syn-count-" + t.ID, Type: "count", Payload: map[string]any{"task_id": t.ID, "lines": lines}}})
	if firstErr(err, rejected(res)) != nil {
		s.fail("count", firstErr(err, rejected(res)))
		return
	}
	s.Done.Add(1)
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

func rejected(res []CommandResult) error {
	if len(res) == 1 && res[0].Status == "rejected" {
		return fmt.Errorf("rejected: %s", res[0].Reason)
	}
	return nil
}
