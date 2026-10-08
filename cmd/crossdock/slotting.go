package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/nicholasgasior/crossdock/internal/generate"
	"github.com/nicholasgasior/crossdock/internal/inventory"
	"github.com/nicholasgasior/crossdock/internal/slotting"
)

// crossdock slotting run -warehouse W      run the Python job, then replay its proposal
// crossdock slotting replay <proposal-id>  measure a proposal with the platform's router
func cmdSlotting(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: crossdock slotting run|replay ...")
	}
	db, err := openDB(requireEnv("CROSSDOCK_DATABASE_URL"))
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	svc := slotting.NewService(db, inventory.NewStore(db))

	switch args[0] {
	case "replay":
		fs := flag.NewFlagSet("slotting replay", flag.ExitOnError)
		wid := fs.String("warehouse", os.Getenv("CROSSDOCK_WAREHOUSE_ID"), "warehouse id")
		_ = fs.Parse(args[1:])
		if fs.NArg() != 1 || *wid == "" {
			return errors.New("usage: crossdock slotting replay -warehouse W <proposal-id>")
		}
		r, err := svc.Replay(ctx, inventory.WarehouseID(*wid), fs.Arg(0))
		if err != nil {
			return err
		}
		fmt.Printf("replayed %d waves: %.0f m as laid out, %.0f m proposed, saving %.0f m (%.1f%%)\n",
			r.Waves, r.BeforeM, r.AfterM, r.Saving(), 100*r.Saving()/r.BeforeM)
		return nil

	case "run":
		fs := flag.NewFlagSet("slotting run", flag.ExitOnError)
		wid := fs.String("warehouse", os.Getenv("CROSSDOCK_WAREHOUSE_ID"), "warehouse id")
		maxMoves := fs.Int("max-moves", 20, "most SKUs one run may move")
		window := fs.Int("window-days", 90, "days of pick history to learn from")
		python := fs.String("python", os.Getenv("CROSSDOCK_PYTHON"), "python interpreter (default: cmd/slotting/.venv, else python3)")
		_ = fs.Parse(args[1:])
		if *wid == "" {
			return errors.New("-warehouse is required")
		}
		py := *python
		if py == "" {
			py = findPython()
		}
		self, err := os.Executable()
		if err != nil {
			return err
		}
		cmd := exec.CommandContext(ctx, py, filepath.Join("cmd", "slotting", "slotting.py"), "--warehouse", *wid,
			"--max-moves", fmt.Sprint(*maxMoves), "--window-days", fmt.Sprint(*window),
			"--replay-bin", self)
		cmd.Stdout, cmd.Stderr, cmd.Env = os.Stdout, os.Stderr, os.Environ()
		return cmd.Run()
	}
	return fmt.Errorf("unknown slotting command %q", args[0])
}

func findPython() string {
	venv := filepath.Join("cmd", "slotting", ".venv", "bin", "python")
	if runtime.GOOS == "windows" {
		venv = filepath.Join("cmd", "slotting", ".venv", "Scripts", "python.exe")
	}
	if _, err := os.Stat(venv); err == nil {
		return venv
	}
	return "python3"
}

// crossdock generate history -warehouse W [-waves N] ...
func cmdGenerateHistory(args []string) error {
	cfg := generate.DefaultHistoryConfig()
	fs := flag.NewFlagSet("generate history", flag.ExitOnError)
	wid := fs.String("warehouse", os.Getenv("CROSSDOCK_WAREHOUSE_ID"), "warehouse id (from crossdock generate)")
	fs.Uint64Var(&cfg.Seed, "seed", cfg.Seed, "random seed")
	fs.IntVar(&cfg.SKUs, "skus", cfg.SKUs, "SKUs to place and pick")
	fs.IntVar(&cfg.Waves, "waves", cfg.Waves, "waves of picking")
	fs.IntVar(&cfg.OrdersPerWave, "orders", cfg.OrdersPerWave, "orders per wave")
	fs.IntVar(&cfg.Days, "days", cfg.Days, "days of history")
	_ = fs.Parse(args)
	if *wid == "" {
		return errors.New("-warehouse is required")
	}
	db, err := openDB(requireEnv("CROSSDOCK_DATABASE_URL"))
	if err != nil {
		return err
	}
	defer db.Close()
	res, err := generate.GenerateHistory(context.Background(), db, inventory.NewStore(db), inventory.WarehouseID(*wid), cfg)
	if err != nil {
		return err
	}
	fmt.Printf("history written: %d SKUs, %d waves, %d picks, mean route %.0f m\n", res.SKUs, res.Waves, res.Picks, res.MeanRouteM)
	return nil
}
