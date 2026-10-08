// Package tasks is the operator task queue shared by all domains (spec 3.8).
// Domain packages create tasks and complete them in the same transaction as
// the movement the task causes; this package owns the state machine.
package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
	"github.com/nicholasgasior/crossdock/internal/ids"
	"github.com/nicholasgasior/crossdock/internal/inventory"
)

var (
	ErrNotFound      = errors.New("tasks: task not found")
	ErrBadTransition = errors.New("tasks: invalid state transition")
	ErrNotAssignee   = errors.New("tasks: task is assigned to someone else")
)

// Task is one unit of operator work.
type Task struct {
	ID              string
	WarehouseID     inventory.WarehouseID
	Kind            inventory.TaskKind
	State           inventory.TaskState
	AssignedTo      string
	SKU             inventory.SKUID
	Lot             inventory.LotID
	LPN             inventory.LPNID
	FromLocation    inventory.LocationCode
	ToLocation      inventory.LocationCode
	Qty             int64
	OrderID         string
	WaveID          string
	ReceiptID       string
	Ref             string
	RouteSeq        int
	ExceptionReason string
	// Reason says why the task exists (count: schedule, exception, recount).
	Reason      string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	CompletedAt     *time.Time
}

// Allowed state transitions. completed and cancelled are terminal.
var allowed = map[inventory.TaskState][]inventory.TaskState{
	inventory.TaskOpen:       {inventory.TaskAssigned, inventory.TaskCompleted, inventory.TaskCancelled, inventory.TaskException},
	inventory.TaskAssigned:   {inventory.TaskOpen, inventory.TaskInProgress, inventory.TaskCompleted, inventory.TaskCancelled, inventory.TaskException},
	inventory.TaskInProgress: {inventory.TaskCompleted, inventory.TaskCancelled, inventory.TaskException},
	inventory.TaskException:  {inventory.TaskOpen, inventory.TaskCompleted, inventory.TaskCancelled},
}

// CanTransition reports whether from -> to is permitted.
func CanTransition(from, to inventory.TaskState) bool {
	for _, s := range allowed[from] {
		if s == to {
			return true
		}
	}
	return false
}

const cols = `id, warehouse_id, kind, state, assigned_to, sku_id, lot_id, lpn_id,
	from_location, to_location, qty, order_id, wave_id, receipt_id, ref, route_seq,
	exception_reason, reason, created_at, updated_at, completed_at`

type scanner interface{ Scan(dest ...any) error }

func scan(r scanner) (Task, error) {
	var t Task
	err := r.Scan(&t.ID, (*string)(&t.WarehouseID), (*string)(&t.Kind), (*string)(&t.State),
		&t.AssignedTo, (*string)(&t.SKU), (*string)(&t.Lot), (*string)(&t.LPN),
		(*string)(&t.FromLocation), (*string)(&t.ToLocation), &t.Qty, &t.OrderID, &t.WaveID,
		&t.ReceiptID, &t.Ref, &t.RouteSeq, &t.ExceptionReason, &t.Reason, &t.CreatedAt, &t.UpdatedAt, &t.CompletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

// Querier is satisfied by *sql.DB and *sql.Tx.
type Querier interface {
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
}

// Create inserts a task in state open. If t.Ref is set and a task with the same
// (kind, ref) exists, the existing task is returned and created is false.
func Create(ctx context.Context, q Querier, t Task) (out Task, created bool, err error) {
	if t.ID == "" {
		t.ID = ids.New("tsk")
	}
	row := q.QueryRowContext(ctx, `
		INSERT INTO tasks (id, warehouse_id, kind, sku_id, lot_id, lpn_id, from_location, to_location,
			qty, order_id, wave_id, receipt_id, ref, route_seq, reason)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		ON CONFLICT (warehouse_id, kind, ref) WHERE ref <> '' DO NOTHING
		RETURNING `+cols,
		t.ID, string(t.WarehouseID), string(t.Kind), string(t.SKU), string(t.Lot), string(t.LPN),
		string(t.FromLocation), string(t.ToLocation), t.Qty, t.OrderID, t.WaveID, t.ReceiptID, t.Ref, t.RouteSeq, t.Reason)
	out, err = scan(row)
	if errors.Is(err, ErrNotFound) { // conflict: fetch the existing one
		out, err = scan(q.QueryRowContext(ctx,
			`SELECT `+cols+` FROM tasks WHERE warehouse_id=$1 AND kind=$2 AND ref=$3`,
			string(t.WarehouseID), string(t.Kind), t.Ref))
		return out, false, err
	}
	return out, err == nil, err
}

// Get reads a task without locking.
func Get(ctx context.Context, q Querier, wid inventory.WarehouseID, id string) (Task, error) {
	return scan(q.QueryRowContext(ctx, `SELECT `+cols+` FROM tasks WHERE warehouse_id=$1 AND id=$2`, string(wid), id))
}

// Lock reads a task with SELECT ... FOR UPDATE. Use inside a transaction.
func Lock(ctx context.Context, tx *sql.Tx, wid inventory.WarehouseID, id string) (Task, error) {
	return scan(tx.QueryRowContext(ctx, `SELECT `+cols+` FROM tasks WHERE warehouse_id=$1 AND id=$2 FOR UPDATE`, string(wid), id))
}

// Transition moves a locked task to a new state.
func Transition(ctx context.Context, tx *sql.Tx, t Task, to inventory.TaskState, actor, reason string) error {
	if !CanTransition(t.State, to) {
		return fmt.Errorf("%w: %s -> %s", ErrBadTransition, t.State, to)
	}
	assignee := t.AssignedTo
	switch to {
	case inventory.TaskOpen:
		assignee = ""
	case inventory.TaskAssigned:
		assignee = actor
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE tasks SET state=$3, assigned_to=$4, exception_reason=$5, updated_at=NOW(),
			completed_at = CASE WHEN $3='completed' THEN NOW() ELSE completed_at END
		WHERE warehouse_id=$1 AND id=$2`, string(t.WarehouseID), t.ID, string(to), assignee, reason)
	return err
}

// Do runs fn in a transaction that has the task locked, and commits.
func Do(ctx context.Context, db *sql.DB, wid inventory.WarehouseID, id string, fn func(tx *sql.Tx, t Task) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	t, err := Lock(ctx, tx, wid, id)
	if err != nil {
		return err
	}
	if err := fn(tx, t); err != nil {
		return err
	}
	return tx.Commit()
}

// Assign assigns an open task to an operator.
func Assign(ctx context.Context, db *sql.DB, wid inventory.WarehouseID, id, operator string) error {
	return Do(ctx, db, wid, id, func(tx *sql.Tx, t Task) error {
		if t.State == inventory.TaskAssigned && t.AssignedTo == operator {
			return nil // retry
		}
		return Transition(ctx, tx, t, inventory.TaskAssigned, operator, "")
	})
}

// Start moves an assigned task to in_progress. Only the assignee may start it.
func Start(ctx context.Context, db *sql.DB, wid inventory.WarehouseID, id, operator string) error {
	return Do(ctx, db, wid, id, func(tx *sql.Tx, t Task) error {
		if t.State == inventory.TaskInProgress && t.AssignedTo == operator {
			return nil
		}
		if t.AssignedTo != operator {
			return ErrNotAssignee
		}
		return Transition(ctx, tx, t, inventory.TaskInProgress, operator, "")
	})
}

// Raise marks a task as an exception with a reason (short pick, full slot...).
func Raise(ctx context.Context, db *sql.DB, wid inventory.WarehouseID, id, reason string) error {
	return Do(ctx, db, wid, id, func(tx *sql.Tx, t Task) error {
		if t.State == inventory.TaskException {
			return nil
		}
		return Transition(ctx, tx, t, inventory.TaskException, "", reason)
	})
}

// Claim assigns the next open task (lowest route_seq, then oldest) of one of
// the given kinds to the operator. It returns nil, nil when the queue is empty.
// SKIP LOCKED means concurrent claimers never block or receive the same task.
func Claim(ctx context.Context, db *sql.DB, wid inventory.WarehouseID, operator string, kinds []inventory.TaskKind) (*Task, error) {
	ks := make([]string, len(kinds))
	for i, k := range kinds {
		ks[i] = string(k)
	}
	// One kind lets Postgres read tasks_queue_idx in order and stop at the first row. A list
	// of kinds (or none) cannot, so it sorts every open task of the warehouse.
	// ponytail: O(open tasks) for several kinds; per-kind lookups would fix it if queues grow past ~10k.
	kindCond := `(cardinality($3::text[])=0 OR kind = ANY($3))`
	if len(ks) == 1 {
		kindCond = `kind = ($3::text[])[1]`
	}
	// sqlok: kindCond is one of two fixed fragments
	row := db.QueryRowContext(ctx, `
		UPDATE tasks SET state='assigned', assigned_to=$2, updated_at=NOW()
		WHERE (warehouse_id, id) = (
			SELECT warehouse_id, id FROM tasks
			WHERE warehouse_id=$1 AND state='open' AND `+kindCond+`
			ORDER BY route_seq, created_at, id LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING `+cols, string(wid), operator, pq.Array(ks))
	t, err := scan(row)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return &t, err
}

// Filter narrows List.
type Filter struct {
	State      inventory.TaskState
	Kind       inventory.TaskKind
	AssignedTo string
}

// List returns tasks ordered by (created_at, id) after the cursor (created_at|id
// encoded by the caller as afterCreated/afterID). limit is capped at 200.
func List(ctx context.Context, q Querier, wid inventory.WarehouseID, f Filter, afterCreated time.Time, afterID string, limit int) ([]Task, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var where []string
	args := []any{string(wid)}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args))))
	}
	if f.State != "" {
		add("state = ANY(string_to_array(?, ','))", string(f.State)) // comma separated list allowed
	}
	if f.Kind != "" {
		add("kind = ?", string(f.Kind))
	}
	if f.AssignedTo != "" {
		add("assigned_to = ?", f.AssignedTo)
	}
	if afterID != "" {
		args = append(args, afterCreated, afterID)
		where = append(where, fmt.Sprintf("(created_at, id) > ($%d, $%d)", len(args)-1, len(args)))
	}
	sqlq := `SELECT ` + cols + ` FROM tasks WHERE warehouse_id=$1`
	for _, w := range where {
		sqlq += " AND " + w
	}
	args = append(args, limit)
	sqlq += fmt.Sprintf(" ORDER BY created_at, id LIMIT $%d", len(args))
	// sqlok: fixed conditions with numbered placeholders; values are parameters
	rows, err := q.QueryContext(ctx, sqlq, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
