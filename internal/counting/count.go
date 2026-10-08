package counting

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/nicholasgasior/crossdock/internal/apperr"
	"github.com/nicholasgasior/crossdock/internal/ids"
	"github.com/nicholasgasior/crossdock/internal/inventory"
	"github.com/nicholasgasior/crossdock/internal/tasks"
)

var (
	ErrNotFound  = errors.New("counting: not found")
	ErrWrongTask = errors.New("counting: task is not a count task")
	ErrBadState  = errors.New("counting: variance is not in a state that allows this")
)

// RecountWindow is how long after the first count a recount may finish and still
// be trusted to classify it (spec 8.2).
const RecountWindow = time.Hour

// Service counts locations and classifies what it finds.
type Service struct {
	db    *sql.DB
	store *inventory.Store
	now   func() time.Time
}

func NewService(db *sql.DB, store *inventory.Store) *Service {
	return &Service{db: db, store: store, now: time.Now}
}

// SetClock replaces the clock. Tests use it to age counts.
func (s *Service) SetClock(now func() time.Time) { s.now = now }

type key struct{ sku, lot, lpn string }

// Line is one counted item as the operator reports it.
type Line struct {
	SKU     string
	Lot     string // optional; resolved from the system when empty
	LPN     string
	Counted int64
	Damaged int64 // units of Counted that are damaged
}

// shelf units: on the shelf, not picked. In-transit stock at a location has been
// picked and is not there to count.
const shelfStates = `('available','reserved','quarantined','damaged')`

const deltaExpr = `(CASE WHEN to_location=$2 AND to_state IN ` + shelfStates + ` THEN qty ELSE 0 END)
	- (CASE WHEN from_location=$2 AND from_state IN ` + shelfStates + ` THEN qty ELSE 0 END)`

// Start opens a count: it records which movements have committed so far. Doing
// this at start, with a database snapshot, is what makes timing classification
// exact even though sequence numbers are assigned before commit. Repeating it is
// a no-op.
func (s *Service) Start(ctx context.Context, wid inventory.WarehouseID, taskID, operator string) error {
	return tasks.Do(ctx, s.db, wid, taskID, func(tx *sql.Tx, t tasks.Task) error {
		if t.Kind != inventory.TaskCount {
			return ErrWrongTask
		}
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM counts WHERE warehouse_id=$1 AND task_id=$2)`, string(wid), taskID).Scan(&exists); err != nil || exists {
			return err
		}
		return s.openCount(ctx, tx, t, operator)
	})
}

func (s *Service) openCount(ctx context.Context, tx *sql.Tx, t tasks.Task, operator string) error {
	reason, recountOf := t.Reason, ""
	if reason == "" {
		reason = "schedule"
	}
	if reason == "recount" {
		recountOf = strings.TrimPrefix(t.Ref, "recount:")
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO counts (id, warehouse_id, task_id, location, reason, recount_of, operator, start_snapshot, start_seq, started_at)
		SELECT $1::text, $2::text, $3::text, $4::text, $5::text, $6::text, $7::text, pg_current_snapshot()::text,
		       COALESCE((SELECT max(seq) FROM movements WHERE warehouse_id=$2::text), 0), $8::timestamptz`,
		ids.New("cnt"), string(t.WarehouseID), t.ID, string(t.FromLocation), reason, recountOf, operator, s.now()); err != nil {
		return err
	}
	if t.State == inventory.TaskAssigned {
		return tasks.Transition(ctx, tx, t, inventory.TaskInProgress, operator, "")
	}
	return nil
}

// SheetLine is something the operator should look for. Quantities are hidden:
// a blind count is not biased by what the system expects.
type SheetLine struct{ SKU, Lot, LPN string }

// Sheet lists what the system says is at the task's location, without quantities.
func (s *Service) Sheet(ctx context.Context, wid inventory.WarehouseID, taskID string) ([]SheetLine, error) {
	t, err := tasks.Get(ctx, s.db, wid, taskID)
	if err != nil {
		return nil, err
	}
	if t.Kind != inventory.TaskCount {
		return nil, ErrWrongTask
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT sku_id, lot_id, lpn_id FROM positions
		WHERE warehouse_id=$1 AND location=$2 AND available+reserved+quarantined+damaged > 0
		ORDER BY sku_id, lot_id, lpn_id`, string(wid), string(t.FromLocation))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SheetLine{}
	for rows.Next() {
		var l SheetLine
		if err := rows.Scan(&l.SKU, &l.Lot, &l.LPN); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Result summarises a submitted count.
type Result struct {
	CountID   string
	Matched   int      // keys where the count equals the system
	Variances []string // variance ids opened or updated
	Recount   string   // recount task id, when one was needed
}

type moveInfo struct {
	Seq   int64  `json:"seq"`
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Delta int64  `json:"delta"`
}

// Submit records what the operator counted, classifies each difference and
// completes the task. Repeating it for a completed task is a no-op.
func (s *Service) Submit(ctx context.Context, wid inventory.WarehouseID, taskID, operator string, lines []Line) (Result, error) {
	for _, l := range lines {
		if l.SKU == "" || l.Counted < 0 || l.Damaged < 0 || l.Damaged > l.Counted {
			return Result{}, apperr.Invalid("each line needs a sku, a quantity of 0 or more, and damaged no greater than counted")
		}
	}
	// Repeatable read: every query below sees one committed state, so "ended
	// when" is a single well defined point.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback() //nolint:errcheck

	t, err := tasks.Lock(ctx, tx, wid, taskID)
	if err != nil {
		return Result{}, err
	}
	if t.Kind != inventory.TaskCount {
		return Result{}, ErrWrongTask
	}
	var res Result
	if t.State == inventory.TaskCompleted {
		err := tx.QueryRowContext(ctx, `SELECT id FROM counts WHERE warehouse_id=$1 AND task_id=$2`, string(wid), taskID).Scan(&res.CountID)
		return res, err
	}
	loc := string(t.FromLocation)

	var (
		countID, reason, startSnap, recountOf string
	)
	err = tx.QueryRowContext(ctx, `SELECT id, reason, start_snapshot, recount_of FROM counts WHERE warehouse_id=$1 AND task_id=$2`,
		string(wid), taskID).Scan(&countID, &reason, &startSnap, &recountOf)
	if errors.Is(err, sql.ErrNoRows) {
		// Never started (the start was lost): the window is empty, which is the strict choice.
		if err := s.openCount(ctx, tx, t, operator); err != nil {
			return Result{}, err
		}
		err = tx.QueryRowContext(ctx, `SELECT id, reason, start_snapshot, recount_of FROM counts WHERE warehouse_id=$1 AND task_id=$2`,
			string(wid), taskID).Scan(&countID, &reason, &startSnap, &recountOf)
	}
	if err != nil {
		return Result{}, err
	}
	res.CountID = countID

	var endSnap string
	var endSeq int64
	if err := tx.QueryRowContext(ctx, `SELECT pg_current_snapshot()::text, COALESCE((SELECT max(seq) FROM movements WHERE warehouse_id=$1),0)`, string(wid)).Scan(&endSnap, &endSeq); err != nil {
		return Result{}, err
	}

	base, err := s.sums(ctx, tx, wid, loc, startSnap)
	if err != nil {
		return Result{}, err
	}
	end, err := s.sums(ctx, tx, wid, loc, "")
	if err != nil {
		return Result{}, err
	}
	window, err := s.window(ctx, tx, wid, loc, startSnap)
	if err != nil {
		return Result{}, err
	}

	counted, err := resolveLines(lines, end, window, base)
	if err != nil {
		return Result{}, err
	}

	// Union of everything either side knows about.
	keys := map[key]struct{}{}
	for k := range end {
		keys[k] = struct{}{}
	}
	for k := range base {
		keys[k] = struct{}{}
	}
	for k := range window {
		keys[k] = struct{}{}
	}
	for k := range counted {
		keys[k] = struct{}{}
	}
	state := func(k key) KeyState {
		ds := []int64{}
		for _, m := range window[k] {
			ds = append(ds, m.Delta)
		}
		return KeyState{Base: base[k], End: end[k], Deltas: ds}
	}

	// The full record of the count, including zeros for items nobody reported.
	for _, k := range sortedKeyList(keys) {
		if end[k] == 0 && counted[k].Counted == 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO count_lines (warehouse_id, count_id, sku_id, lot_id, lpn_id, counted_qty, damaged_qty)
			VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`,
			string(wid), countID, k.sku, k.lot, k.lpn, counted[k].Counted, counted[k].Damaged); err != nil {
			return Result{}, err
		}
	}

	needRecount := false

	// Variances waiting on this recount, by key.
	waiting := map[key]string{}
	var firstCountedAt = map[string]time.Time{}
	var firstQty = map[string]int64{}
	if reason == "recount" {
		rows, err := tx.QueryContext(ctx, `
			SELECT id, sku_id, lot_id, lpn_id, counted_qty, first_counted_at FROM variances
			WHERE warehouse_id=$1 AND recount_task_id=$2 AND state='awaiting_recount'`, string(wid), taskID)
		if err != nil {
			return Result{}, err
		}
		for rows.Next() {
			var id string
			var k key
			var c int64
			var at time.Time
			if err := rows.Scan(&id, &k.sku, &k.lot, &k.lpn, &c, &at); err != nil {
				rows.Close()
				return Result{}, err
			}
			waiting[k], firstQty[id], firstCountedAt[id] = id, c, at
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return Result{}, err
		}
	}

	for _, k := range sortedKeyList(keys) {
		ks, c := state(k), counted[k]
		if reason == "recount" {
			if vid, ok := waiting[k]; ok {
				// The recount decides the waiting variance. Lateness is measured from the first count.
				late := s.now().Sub(firstCountedAt[vid]) > RecountWindow
				cls := Recount(firstQty[vid], c.Counted, ks, late)
				if err := s.settle(ctx, tx, wid, vid, countID, cls, ks.End, c.Counted, window[k]); err != nil {
					return Result{}, err
				}
				res.Variances = append(res.Variances, vid)
				delete(waiting, k)
				if c.Damaged > 0 {
					if err := s.damagedVariance(ctx, tx, wid, countID, loc, k, ks.End, c, &res); err != nil {
						return Result{}, err
					}
				}
				continue
			}
		}
		if c.Damaged > 0 {
			if err := s.damagedVariance(ctx, tx, wid, countID, loc, k, ks.End, c, &res); err != nil {
				return Result{}, err
			}
		}
		cls, needs, differs := FirstCount(c.Counted, ks)
		if !differs {
			if end[k] != 0 || c.Counted != 0 {
				res.Matched++
			}
			continue
		}
		// A recount sees a key the first count did not flag: judge it now, there is nothing to recount against.
		if reason == "recount" {
			needs = false
		}
		vid, err := s.openVariance(ctx, tx, wid, countID, loc, k, ks, c, cls, needs, window[k])
		if err != nil {
			return Result{}, err
		}
		res.Variances = append(res.Variances, vid)
		needRecount = needRecount || needs
	}
	// A waiting variance whose key vanished from the recount (nothing there, nothing expected).
	for k, vid := range waiting {
		ks := state(k)
		if err := s.settle(ctx, tx, wid, vid, countID, Recount(firstQty[vid], 0, ks, s.now().Sub(firstCountedAt[vid]) > RecountWindow), ks.End, 0, window[k]); err != nil {
			return Result{}, err
		}
		res.Variances = append(res.Variances, vid)
	}

	if needRecount {
		rt, _, err := tasks.Create(ctx, tx, tasks.Task{
			WarehouseID: wid, Kind: inventory.TaskCount, FromLocation: t.FromLocation,
			Ref: "recount:" + countID, Reason: "recount",
		})
		if err != nil {
			return Result{}, err
		}
		res.Recount = rt.ID
		if _, err := tx.ExecContext(ctx, `UPDATE variances SET recount_task_id=$3 WHERE warehouse_id=$1 AND count_id=$2 AND state='awaiting_recount'`,
			string(wid), countID, rt.ID); err != nil {
			return Result{}, err
		}
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE counts SET state='counted', end_snapshot=$3, end_seq=$4, completed_at=$5, operator=CASE WHEN operator='' THEN $6 ELSE operator END
		WHERE warehouse_id=$1 AND id=$2`, string(wid), countID, endSnap, endSeq, s.now(), operator); err != nil {
		return Result{}, err
	}
	if err := tasks.Transition(ctx, tx, t, inventory.TaskCompleted, "", ""); err != nil {
		return Result{}, err
	}
	return res, tx.Commit()
}

func (s *Service) sums(ctx context.Context, tx *sql.Tx, wid inventory.WarehouseID, loc, snapshot string) (map[key]int64, error) {
	q := `SELECT sku_id, lot_id, lpn_id, COALESCE(SUM(` + deltaExpr + `),0) FROM movements
		WHERE warehouse_id=$1 AND (from_location=$2 OR to_location=$2)`
	args := []any{string(wid), loc}
	if snapshot != "" {
		q += ` AND pg_visible_in_snapshot(xid, $3::pg_snapshot)`
		args = append(args, snapshot)
	}
	rows, err := tx.QueryContext(ctx, q+` GROUP BY sku_id, lot_id, lpn_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[key]int64{}
	for rows.Next() {
		var k key
		var n int64
		if err := rows.Scan(&k.sku, &k.lot, &k.lpn, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

// window returns the movements that committed after the count started, per key.
func (s *Service) window(ctx context.Context, tx *sql.Tx, wid inventory.WarehouseID, loc, startSnap string) (map[key][]moveInfo, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT sku_id, lot_id, lpn_id, seq, id, kind, `+deltaExpr+`
		FROM movements
		WHERE warehouse_id=$1 AND (from_location=$2 OR to_location=$2) AND NOT pg_visible_in_snapshot(xid, $3::pg_snapshot)
		ORDER BY seq`, string(wid), loc, startSnap)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[key][]moveInfo{}
	for rows.Next() {
		var k key
		var m moveInfo
		if err := rows.Scan(&k.sku, &k.lot, &k.lpn, &m.Seq, &m.ID, &m.Kind, &m.Delta); err != nil {
			return nil, err
		}
		if m.Delta != 0 { // reserve, pack and similar do not change what is on the shelf
			out[k] = append(out[k], m)
		}
	}
	return out, rows.Err()
}

type counted struct{ Counted, Damaged int64 }

// resolveLines maps the operator's lines to keys. A line without a lot takes the
// one lot the system holds for that sku and LPN, else the newest lot, else a
// placeholder lot for stock the system has never seen.
func resolveLines(lines []Line, end map[key]int64, window map[key][]moveInfo, base map[key]int64) (map[key]counted, error) {
	known := map[[2]string][]string{} // (sku, lpn) -> lots seen
	add := func(k key) {
		p := [2]string{k.sku, k.lpn}
		for _, l := range known[p] {
			if l == k.lot {
				return
			}
		}
		known[p] = append(known[p], k.lot)
	}
	for k := range end {
		add(k)
	}
	for k := range base {
		add(k)
	}
	for k := range window {
		add(k)
	}
	out := map[key]counted{}
	for _, l := range lines {
		lot := l.Lot
		if lot == "" {
			lots := known[[2]string{l.SKU, l.LPN}]
			switch len(lots) {
			case 0:
				lot = "LOT-COUNT-" + l.SKU
			default:
				sort.Strings(lots)
				lot = lots[len(lots)-1]
			}
		}
		k := key{l.SKU, lot, l.LPN}
		c := out[k]
		c.Counted += l.Counted
		c.Damaged += l.Damaged
		out[k] = c
	}
	return out, nil
}

func (s *Service) openVariance(ctx context.Context, tx *sql.Tx, wid inventory.WarehouseID, countID, loc string, k key, ks KeyState, c counted, cls Class, needsRecount bool, mv []moveInfo) (string, error) {
	id := ids.New("var")
	st := "open"
	if needsRecount {
		st = "awaiting_recount"
	}
	if err := ensureLot(ctx, tx, wid, k); err != nil {
		return "", err
	}
	inflight, _ := json.Marshal(nonNil(mv))
	_, err := tx.ExecContext(ctx, `
		INSERT INTO variances (id, warehouse_id, count_id, location, sku_id, lot_id, lpn_id, system_qty, counted_qty, damaged_qty,
		                       variance, in_flight, proposed, state, opened_at, first_counted_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$15)`,
		id, string(wid), countID, loc, k.sku, k.lot, k.lpn, ks.End, c.Counted, c.Damaged, ks.End-c.Counted, inflight, string(cls), st, s.now())
	return id, err
}

// damagedVariance records units the operator marked damaged. The action is a
// quarantine movement, not an adjustment; the supervisor confirms it.
func (s *Service) damagedVariance(ctx context.Context, tx *sql.Tx, wid inventory.WarehouseID, countID, loc string, k key, system int64, c counted, res *Result) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM variances WHERE warehouse_id=$1 AND location=$2 AND sku_id=$3 AND lot_id=$4 AND lpn_id=$5
		              AND proposed='damaged' AND state<>'closed')`, string(wid), loc, k.sku, k.lot, k.lpn).Scan(&exists); err != nil || exists {
		return err
	}
	id := ids.New("var")
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO variances (id, warehouse_id, count_id, location, sku_id, lot_id, lpn_id, system_qty, counted_qty, damaged_qty,
		                       variance, proposed, state, opened_at, first_counted_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,0,'damaged','open',$11,$11)`,
		id, string(wid), countID, loc, k.sku, k.lot, k.lpn, system, c.Counted, c.Damaged, s.now()); err != nil {
		return err
	}
	res.Variances = append(res.Variances, id)
	return nil
}

// settle applies a recount's verdict to the variance that was waiting for it.
func (s *Service) settle(ctx context.Context, tx *sql.Tx, wid inventory.WarehouseID, vid, countID string, cls Class, system, counted int64, mv []moveInfo) error {
	inflight, _ := json.Marshal(nonNil(mv))
	_, err := tx.ExecContext(ctx, `
		UPDATE variances SET proposed=$3, state='open', system_qty=$4::bigint, counted_qty=$5::bigint, variance=$4::bigint-$5::bigint,
		       in_flight=$6, flagged=$7, resolved_count_id=$8
		WHERE warehouse_id=$1 AND id=$2`,
		string(wid), vid, string(cls), system, counted, inflight, cls == Shrinkage, countID)
	return err
}

// ensureLot makes sure a lot row exists for stock the system has never seen, so
// a later "found" adjustment can satisfy the movement's foreign key.
func ensureLot(ctx context.Context, tx *sql.Tx, wid inventory.WarehouseID, k key) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO lots (id, sku_id, warehouse_id, received_at)
		SELECT $1, $2, $3, NOW() WHERE EXISTS (SELECT 1 FROM skus WHERE warehouse_id=$3 AND id=$2)
		ON CONFLICT (warehouse_id, id) DO NOTHING`, k.lot, k.sku, string(wid))
	return err
}

func nonNil(m []moveInfo) []moveInfo {
	if m == nil {
		return []moveInfo{}
	}
	return m
}

func sortedKeyList(m map[key]struct{}) []key {
	out := make([]key, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.sku != b.sku {
			return a.sku < b.sku
		}
		if a.lot != b.lot {
			return a.lot < b.lot
		}
		return a.lpn < b.lpn
	})
	return out
}

