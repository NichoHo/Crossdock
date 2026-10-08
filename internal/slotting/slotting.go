// Package slotting reads the proposals the Python slotting job writes, measures
// them, and turns accepted ones into relocate tasks (spec 10).
//
// The job's solver reports its own objective, which is a proxy. This package
// replays the historical waves through the same router that builds real pick
// routes (spec 6.3), once against the layout as it is and once against the
// proposed layout. The saving shown to supervisors is that replayed figure.
package slotting

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nicholasgasior/crossdock/internal/apperr"
	"github.com/nicholasgasior/crossdock/internal/ids"
	"github.com/nicholasgasior/crossdock/internal/inventory"
	"github.com/nicholasgasior/crossdock/internal/outbound"
	"github.com/nicholasgasior/crossdock/internal/tasks"
)

var (
	ErrNotFound  = errors.New("slotting: proposal not found")
	ErrWrongTask = errors.New("slotting: not a relocate task")
)

type Service struct {
	db    *sql.DB
	store *inventory.Store
	now   func() time.Time
}

func NewService(db *sql.DB, store *inventory.Store) *Service {
	return &Service{db: db, store: store, now: time.Now}
}

func (s *Service) SetClock(now func() time.Time) { s.now = now }

// Proposal is one run of the job.
type Proposal struct {
	ID, State, Solver, SolverStatus, AcceptedBy, Note string
	CreatedAt                                          time.Time
	WindowDays, MaxMoves                               int
	ObjectiveBefore, ObjectiveAfter                    float64
	// Replay* are nil until the platform has measured the proposal.
	ReplayWaves              *int
	ReplayBeforeM, ReplayAfterM *float64
	ReplayedAt, AcceptedAt   *time.Time
	Moves                    []Move
}

// SavingM is the replayed saving in metres, or nil if not yet replayed.
func (p Proposal) SavingM() *float64 {
	if p.ReplayBeforeM == nil || p.ReplayAfterM == nil {
		return nil
	}
	v := *p.ReplayBeforeM - *p.ReplayAfterM
	return &v
}

type Move struct {
	Seq                          int
	SKU, From, To, Reason        string
}

const proposalCols = `id, state, solver, solver_status, accepted_by, note, created_at, window_days, max_moves,
	objective_before, objective_after, replay_waves, replay_before_m, replay_after_m, replayed_at, accepted_at`

func scanProposal(r interface{ Scan(...any) error }) (Proposal, error) {
	var p Proposal
	err := r.Scan(&p.ID, &p.State, &p.Solver, &p.SolverStatus, &p.AcceptedBy, &p.Note, &p.CreatedAt, &p.WindowDays, &p.MaxMoves,
		&p.ObjectiveBefore, &p.ObjectiveAfter, &p.ReplayWaves, &p.ReplayBeforeM, &p.ReplayAfterM, &p.ReplayedAt, &p.AcceptedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

func (s *Service) Get(ctx context.Context, wid inventory.WarehouseID, id string) (Proposal, error) {
	p, err := scanProposal(s.db.QueryRowContext(ctx, `SELECT `+proposalCols+` FROM slotting_proposals WHERE warehouse_id=$1 AND id=$2`, string(wid), id))
	if err != nil {
		return p, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT seq, sku_id, from_location, to_location, reason FROM slotting_moves WHERE warehouse_id=$1 AND proposal_id=$2 ORDER BY seq`, string(wid), id)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var m Move
		if err := rows.Scan(&m.Seq, &m.SKU, &m.From, &m.To, &m.Reason); err != nil {
			return p, err
		}
		p.Moves = append(p.Moves, m)
	}
	return p, rows.Err()
}

// List returns proposals newest first.
func (s *Service) List(ctx context.Context, wid inventory.WarehouseID, limit int) ([]Proposal, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+proposalCols+` FROM slotting_proposals WHERE warehouse_id=$1 ORDER BY created_at DESC, id DESC LIMIT $2`, string(wid), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Proposal
	for rows.Next() {
		p, err := scanProposal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Replay (spec 10.2)
// ---------------------------------------------------------------------------

// ReplayResult is the platform's own measurement of a proposal.
type ReplayResult struct {
	Waves            int
	BeforeM, AfterM  float64
}

// Saving is metres saved over the replayed waves.
func (r ReplayResult) Saving() float64 { return r.BeforeM - r.AfterM }

// Replay runs the historical waves of the proposal's window through the pick
// router twice, against the layout as it was when the proposal was made and
// against the proposed layout, and stores the totals. SKUs the job did not
// consider are picked from where they were picked historically in both runs.
func (s *Service) Replay(ctx context.Context, wid inventory.WarehouseID, id string) (ReplayResult, error) {
	p, err := s.Get(ctx, wid, id)
	if err != nil {
		return ReplayResult{}, err
	}
	if p.State != "proposed" && p.State != "replayed" {
		return ReplayResult{}, apperr.Invalid("a %s proposal can no longer be replayed", p.State)
	}
	cur, prop := map[string]string{}, map[string]string{}
	rows, err := s.db.QueryContext(ctx, `SELECT sku_id, current_location, proposed_location FROM slotting_layout WHERE warehouse_id=$1 AND proposal_id=$2`, string(wid), id)
	if err != nil {
		return ReplayResult{}, err
	}
	for rows.Next() {
		var sku, a, b string
		if err := rows.Scan(&sku, &a, &b); err != nil {
			rows.Close()
			return ReplayResult{}, err
		}
		cur[sku], prop[sku] = a, b
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return ReplayResult{}, err
	}

	// Waves are the pick tasks that shared a wave_id.
	pickRows, err := s.db.QueryContext(ctx, `
		SELECT wave_id, sku_id, from_location FROM tasks
		WHERE warehouse_id=$1 AND kind='pick' AND state='completed' AND wave_id <> ''
		  AND completed_at >= $2::timestamptz - make_interval(days => $3) AND completed_at <= $2
		ORDER BY wave_id, id`, string(wid), p.CreatedAt, p.WindowDays)
	if err != nil {
		return ReplayResult{}, err
	}
	type pick struct{ sku, loc string }
	waves := map[string][]pick{}
	var order []string
	for pickRows.Next() {
		var wv string
		var pk pick
		if err := pickRows.Scan(&wv, &pk.sku, &pk.loc); err != nil {
			pickRows.Close()
			return ReplayResult{}, err
		}
		if _, ok := waves[wv]; !ok {
			order = append(order, wv)
		}
		waves[wv] = append(waves[wv], pk)
	}
	pickRows.Close()
	if err := pickRows.Err(); err != nil {
		return ReplayResult{}, err
	}
	if len(waves) == 0 {
		return ReplayResult{}, apperr.Invalid("no completed pick waves in the proposal's %d day window to replay", p.WindowDays)
	}

	var res ReplayResult
	for _, wv := range order {
		var before, after []string
		for _, pk := range waves[wv] {
			a, b := pk.loc, pk.loc
			if c, ok := cur[pk.sku]; ok {
				a, b = c, prop[pk.sku]
			}
			before, after = append(before, a), append(after, b)
		}
		_, mb, err := outbound.Route(before)
		if err != nil {
			return ReplayResult{}, err
		}
		_, ma, err := outbound.Route(after)
		if err != nil {
			return ReplayResult{}, err
		}
		res.Waves++
		res.BeforeM += mb
		res.AfterM += ma
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE slotting_proposals SET replay_waves=$3, replay_before_m=$4, replay_after_m=$5, replayed_at=$6,
		       state = CASE WHEN state='proposed' THEN 'replayed' ELSE state END
		WHERE warehouse_id=$1 AND id=$2`, string(wid), id, res.Waves, res.BeforeM, res.AfterM, s.now()); err != nil {
		return ReplayResult{}, err
	}
	return res, nil
}

// ReplayPending measures every proposal the job has written but nobody has
// replayed yet. Safe to run on a timer.
func (s *Service) ReplayPending(ctx context.Context, wid inventory.WarehouseID) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM slotting_proposals WHERE warehouse_id=$1 AND state='proposed' ORDER BY created_at`, string(wid))
	if err != nil {
		return 0, err
	}
	var pending []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		pending = append(pending, id)
	}
	rows.Close()
	done := 0
	for _, id := range pending {
		if _, err := s.Replay(ctx, wid, id); err != nil {
			if errors.Is(err, apperr.ErrInvalid) {
				continue // nothing to replay yet; try again later
			}
			return done, err
		}
		done++
	}
	return done, nil
}

// ---------------------------------------------------------------------------
// Accepting and doing the moves
// ---------------------------------------------------------------------------

// Accept turns a replayed proposal into relocate tasks, one per position that
// holds available stock of a moving SKU. Only a proposal whose replayed saving
// is positive can be accepted: the solver's word is not enough.
func (s *Service) Accept(ctx context.Context, wid inventory.WarehouseID, id, actor string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck
	p, err := scanProposal(tx.QueryRowContext(ctx, `SELECT `+proposalCols+` FROM slotting_proposals WHERE warehouse_id=$1 AND id=$2 FOR UPDATE`, string(wid), id))
	if err != nil {
		return 0, err
	}
	if p.State == "accepted" || p.State == "completed" {
		var n int
		err := tx.QueryRowContext(ctx, `SELECT count(*) FROM tasks WHERE warehouse_id=$1 AND kind='relocate' AND ref LIKE $2`, string(wid), "move:"+id+":%").Scan(&n)
		return n, err // repeating an accept is a no-op
	}
	if p.State != "replayed" {
		return 0, apperr.Invalid("a proposal must be replayed before it can be accepted (it is %s)", p.State)
	}
	if sv := p.SavingM(); sv == nil || *sv <= 0 {
		return 0, apperr.Invalid("the replayed saving is not positive, so there is nothing to gain by accepting it")
	}
	mrows, err := tx.QueryContext(ctx, `SELECT seq, sku_id, from_location, to_location FROM slotting_moves WHERE warehouse_id=$1 AND proposal_id=$2 ORDER BY seq`, string(wid), id)
	if err != nil {
		return 0, err
	}
	var moves []Move
	for mrows.Next() {
		var m Move
		if err := mrows.Scan(&m.Seq, &m.SKU, &m.From, &m.To); err != nil {
			mrows.Close()
			return 0, err
		}
		moves = append(moves, m)
	}
	mrows.Close()
	if err := mrows.Err(); err != nil {
		return 0, err
	}
	created := 0
	for _, m := range moves {
		prows, err := tx.QueryContext(ctx, `
			SELECT lot_id, lpn_id, available FROM positions
			WHERE warehouse_id=$1 AND location=$2 AND sku_id=$3 AND available > 0 ORDER BY lot_id, lpn_id`, string(wid), m.From, m.SKU)
		if err != nil {
			return 0, err
		}
		type pos struct {
			lot, lpn string
			qty      int64
		}
		var ps []pos
		for prows.Next() {
			var x pos
			if err := prows.Scan(&x.lot, &x.lpn, &x.qty); err != nil {
				prows.Close()
				return 0, err
			}
			ps = append(ps, x)
		}
		prows.Close()
		for _, x := range ps {
			_, ok, err := tasks.Create(ctx, tx, tasks.Task{
				WarehouseID: wid, Kind: inventory.TaskRelocate, SKU: inventory.SKUID(m.SKU), Lot: inventory.LotID(x.lot), LPN: inventory.LPNID(x.lpn),
				FromLocation: inventory.LocationCode(m.From), ToLocation: inventory.LocationCode(m.To), Qty: x.qty,
				Ref: fmt.Sprintf("move:%s:%d:%s:%s", id, m.Seq, x.lot, x.lpn), RouteSeq: m.Seq, Reason: "slotting",
			})
			if err != nil {
				return 0, err
			}
			if ok {
				created++
			}
		}
	}
	state := "accepted"
	if created == 0 {
		state = "completed" // nothing was on the shelf to move
	}
	if _, err := tx.ExecContext(ctx, `UPDATE slotting_proposals SET state=$3, accepted_by=$4, accepted_at=$5 WHERE warehouse_id=$1 AND id=$2`,
		string(wid), id, state, actor, s.now()); err != nil {
		return 0, err
	}
	return created, tx.Commit()
}

// Reject closes a proposal nobody wants.
func (s *Service) Reject(ctx context.Context, wid inventory.WarehouseID, id, note string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE slotting_proposals SET state='rejected', note=$3 WHERE warehouse_id=$1 AND id=$2 AND state IN ('proposed','replayed')`,
		string(wid), id, note)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.Get(ctx, wid, id); err != nil {
			return err
		}
		return apperr.Invalid("only a proposal nobody has accepted can be rejected")
	}
	return nil
}

// CompleteRelocate moves the stock of one relocate task: a relocate movement and
// the task commit together. Repeating it is a no-op. When the last task of a
// proposal finishes, the proposal is completed.
func (s *Service) CompleteRelocate(ctx context.Context, wid inventory.WarehouseID, taskID string) error {
	return tasks.Do(ctx, s.db, wid, taskID, func(tx *sql.Tx, t tasks.Task) error {
		if t.Kind != inventory.TaskRelocate {
			return ErrWrongTask
		}
		if t.State == inventory.TaskCompleted {
			return nil
		}
		if err := s.store.AppendMovementTx(ctx, tx, inventory.Movement{
			ID: ids.New("mvt"), WarehouseID: wid, Kind: inventory.KindRelocate,
			FromLocation: t.FromLocation, FromState: inventory.StateAvailable,
			ToLocation: t.ToLocation, ToState: inventory.StateAvailable,
			SKUID: t.SKU, LotID: t.Lot, LPNID: t.LPN, Qty: t.Qty, OccurredAt: s.now(),
			IdempotencyKey: "task-" + t.ID, TaskID: t.ID,
		}); err != nil {
			return err
		}
		if err := tasks.Transition(ctx, tx, t, inventory.TaskCompleted, "", ""); err != nil {
			return err
		}
		parts := strings.SplitN(t.Ref, ":", 3)
		if len(parts) < 3 || parts[0] != "move" {
			return nil
		}
		var open int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) FROM tasks WHERE warehouse_id=$1 AND kind='relocate' AND ref LIKE $2 AND state NOT IN ('completed','cancelled')`,
			string(wid), "move:"+parts[1]+":%").Scan(&open); err != nil {
			return err
		}
		if open == 0 {
			_, err := tx.ExecContext(ctx, `UPDATE slotting_proposals SET state='completed' WHERE warehouse_id=$1 AND id=$2 AND state='accepted'`, string(wid), parts[1])
			return err
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// Did it work? (spec 10.2, last paragraph)
// ---------------------------------------------------------------------------

// Realised compares real wave routes before and after an accepted proposal was
// carried out, so the prediction can be checked against what the floor did.
type Realised struct {
	WavesBefore, WavesAfter int
	AvgBeforeM, AvgAfterM   float64
	// Since is when the last move finished.
	Since *time.Time
}

func (s *Service) Realised(ctx context.Context, wid inventory.WarehouseID, p Proposal) (Realised, error) {
	var r Realised
	if p.AcceptedAt == nil {
		return r, nil
	}
	var done *time.Time
	if err := s.db.QueryRowContext(ctx, `
		SELECT max(completed_at) FROM tasks WHERE warehouse_id=$1 AND kind='relocate' AND ref LIKE $2 AND state='completed'`,
		string(wid), "move:"+p.ID+":%").Scan(&done); err != nil {
		return r, err
	}
	if done == nil {
		return r, nil
	}
	r.Since = done
	var b, a sql.NullFloat64
	err := s.db.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE created_at < $3), avg(route_distance) FILTER (WHERE created_at < $3),
		       count(*) FILTER (WHERE created_at >= $4), avg(route_distance) FILTER (WHERE created_at >= $4)
		FROM waves WHERE warehouse_id=$1 AND created_at >= $3::timestamptz - make_interval(days => $2)`,
		string(wid), p.WindowDays, *p.AcceptedAt, *done).Scan(&r.WavesBefore, &b, &r.WavesAfter, &a)
	r.AvgBeforeM, r.AvgAfterM = b.Float64, a.Float64
	return r, err
}

// ---------------------------------------------------------------------------
// Floor map
// ---------------------------------------------------------------------------

// MapCell is one location on the floor plan with how often it is picked.
type MapCell struct {
	Code, Zone string
	Picks      int
	Units      int64
}

// FloorMap returns every location with its pick count over the last days days.
func (s *Service) FloorMap(ctx context.Context, wid inventory.WarehouseID, days int) ([]MapCell, error) {
	if days <= 0 || days > 3650 {
		days = 90
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.code, z.purpose,
		       COALESCE((SELECT count(*) FROM tasks t WHERE t.warehouse_id=l.warehouse_id AND t.kind='pick' AND t.state='completed'
		                 AND t.from_location=l.code AND t.completed_at >= $3::timestamptz - make_interval(days => $2)), 0),
		       COALESCE((SELECT SUM(p.available+p.reserved+p.quarantined+p.damaged) FROM positions p WHERE p.warehouse_id=l.warehouse_id AND p.location=l.code), 0)
		FROM locations l JOIN zones z ON z.id=l.zone_id
		WHERE l.warehouse_id=$1 ORDER BY l.code`, string(wid), days, s.now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MapCell
	for rows.Next() {
		var c MapCell
		if err := rows.Scan(&c.Code, &c.Zone, &c.Picks, &c.Units); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
