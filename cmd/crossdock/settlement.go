package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"time"

	"github.com/nicholasgasior/crossdock/internal/inventory"
	"github.com/nicholasgasior/crossdock/internal/settlement"
)

// crossdock courier simulate -warehouse W   gives dispatched COD shipments a courier outcome
// crossdock remittance generate -warehouse W -carrier C -out file.csv   writes a remittance with known breaks
func cmdCourier(args []string) error {
	if len(args) == 0 || args[0] != "simulate" {
		return errors.New("usage: crossdock courier simulate -warehouse W [-seed N] [-rejected 0.1] [-lost 0.02]")
	}
	fs := flag.NewFlagSet("courier simulate", flag.ExitOnError)
	wid := fs.String("warehouse", os.Getenv("CROSSDOCK_WAREHOUSE_ID"), "warehouse id")
	seed := fs.Uint64("seed", 1, "random seed")
	rejected := fs.Float64("rejected", 0.10, "share of parcels the customer rejects")
	lost := fs.Float64("lost", 0.02, "share of parcels the courier loses")
	_ = fs.Parse(args[1:])
	if *wid == "" {
		return errors.New("-warehouse is required")
	}
	db, err := openDB(requireEnv("CROSSDOCK_DATABASE_URL"))
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	rows, err := db.QueryContext(ctx, `
		SELECT sh.id FROM shipments sh
		WHERE sh.warehouse_id=$1 AND sh.cod_amount_minor IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM delivery_outcomes o WHERE o.warehouse_id=sh.warehouse_id AND o.shipment_id=sh.id)
		ORDER BY sh.dispatched_at, sh.id`, *wid)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	svc := settlement.NewService(db)
	rng := rand.New(rand.NewPCG(*seed, 5))
	counts := map[string]int{}
	for _, id := range ids {
		outcome := settlement.DeliveredCollected
		switch x := rng.Float64(); {
		case x < *rejected:
			outcome = settlement.Rejected
		case x < *rejected+*lost:
			outcome = settlement.Lost
		}
		if _, err := svc.RecordOutcome(ctx, inventory.WarehouseID(*wid), settlement.ShipmentRef{ID: id}, outcome, time.Now(), "simulator"); err != nil {
			return err
		}
		counts[outcome]++
	}
	fmt.Printf("%d shipments given an outcome: %v\n", len(ids), counts)
	return nil
}

func cmdRemittance(args []string) error {
	if len(args) == 0 || args[0] != "generate" {
		return errors.New("usage: crossdock remittance generate -warehouse W -carrier C -out file.csv [-short N] [-duplicate N] [-unremitted N] [-rejected-paid N] [-unknown N]")
	}
	fs := flag.NewFlagSet("remittance generate", flag.ExitOnError)
	wid := fs.String("warehouse", os.Getenv("CROSSDOCK_WAREHOUSE_ID"), "warehouse id")
	carrier := fs.String("carrier", "", "courier code")
	out := fs.String("out", "remittance.csv", "file to write; the expected breaks go to <out>.expected.json")
	seed := fs.Uint64("seed", 1, "random seed")
	short := fs.Int("short", 3, "lines remitted short")
	dup := fs.Int("duplicate", 2, "shipments remitted twice")
	unrem := fs.Int("unremitted", 2, "delivered shipments left out of the file")
	rejPaid := fs.Int("rejected-paid", 2, "lines paying for parcels reported rejected")
	unknown := fs.Int("unknown", 2, "lines for tracking numbers nobody dispatched")
	_ = fs.Parse(args[1:])
	if *wid == "" || *carrier == "" {
		return errors.New("-warehouse and -carrier are required")
	}
	db, err := openDB(requireEnv("CROSSDOCK_DATABASE_URL"))
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	svc := settlement.NewService(db)
	terms := settlement.DefaultTerms
	all, err := svc.ListTerms(ctx, inventory.WarehouseID(*wid))
	if err != nil {
		return err
	}
	for _, t := range all {
		if t.Carrier == *carrier {
			terms = t.Terms
		}
	}
	rows, err := db.QueryContext(ctx, `
		SELECT sh.tracking, sh.cod_amount_minor,
		       COALESCE((SELECT o.outcome FROM delivery_outcomes o WHERE o.warehouse_id=sh.warehouse_id AND o.shipment_id=sh.id ORDER BY o.reported_at DESC LIMIT 1), '')
		FROM shipments sh WHERE sh.warehouse_id=$1 AND sh.carrier=$2 AND sh.cod_amount_minor IS NOT NULL ORDER BY sh.dispatched_at, sh.id`, *wid, *carrier)
	if err != nil {
		return err
	}
	var ships []settlement.GenShipment
	for rows.Next() {
		var g settlement.GenShipment
		if err := rows.Scan(&g.Tracking, &g.COD, &g.Outcome); err != nil {
			rows.Close()
			return err
		}
		ships = append(ships, g)
	}
	rows.Close()
	gen, err := settlement.GenerateRemittance(ships, settlement.GenConfig{Seed: *seed, Terms: terms,
		Short: *short, Duplicate: *dup, Unremitted: *unrem, RemittedForRejected: *rejPaid, Unknown: *unknown})
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, gen.CSV, 0o644); err != nil {
		return err
	}
	truth, _ := json.MarshalIndent(map[string]any{"expected_breaks": gen.Expected, "clean_lines": gen.Clean, "by_tracking": gen.ByTracking}, "", "  ")
	if err := os.WriteFile(*out+".expected.json", truth, 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s: %d clean lines, expected breaks %v\n", *out, gen.Clean, gen.Expected)
	return nil
}
