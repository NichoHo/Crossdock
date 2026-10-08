package delivery

import (
	"context"
	"time"

	"github.com/nicholasgasior/crossdock/internal/ids"
)

// Replays are ordinary deliveries with replay_of set (spec 7.2). They bypass
// per-key ordering and carry an X-Crossdock-Replay-Of header; receivers dedupe
// on the event's idempotency key.

const insertReplay = `
	INSERT INTO deliveries (id, event_id, warehouse_id, endpoint_id, ordering_key, replay_of)
	SELECT $1, ev.id, ev.warehouse_id, $3, '',
	       COALESCE((SELECT o.id FROM deliveries o WHERE o.event_id = ev.id AND o.endpoint_id = $3 AND o.replay_of = '' LIMIT 1), 'adhoc')
	FROM events ev WHERE ev.id = $2 AND ev.warehouse_id = $4`

// ReplayEvent redelivers one event to one endpoint.
func (s *Service) ReplayEvent(ctx context.Context, wid, endpointID string, eventID int64) error {
	if _, err := s.Get(ctx, wid, endpointID); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, insertReplay, ids.New("dlv"), eventID, endpointID, wid)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ReplayDead redelivers every dead letter of the endpoint that has not been
// replayed yet. It returns how many were queued.
func (s *Service) ReplayDead(ctx context.Context, wid, endpointID string) (int, error) {
	if _, err := s.Get(ctx, wid, endpointID); err != nil {
		return 0, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.event_id FROM deliveries d
		WHERE d.warehouse_id=$1 AND d.endpoint_id=$2 AND d.state='dead' AND d.replay_of=''
		  AND NOT EXISTS (SELECT 1 FROM deliveries r WHERE r.replay_of = d.id)
		ORDER BY d.event_id`, wid, endpointID)
	if err != nil {
		return 0, err
	}
	return s.replayAll(ctx, rows, wid, endpointID)
}

// ReplayRange redelivers every event that occurred in [from, to) to the endpoint.
func (s *Service) ReplayRange(ctx context.Context, wid, endpointID string, from, to time.Time) (int, error) {
	if _, err := s.Get(ctx, wid, endpointID); err != nil {
		return 0, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id FROM events WHERE warehouse_id=$1 AND occurred_at >= $2 AND occurred_at < $3 ORDER BY id`, wid, from, to)
	if err != nil {
		return 0, err
	}
	return s.replayAll(ctx, rows, wid, endpointID)
}

func (s *Service) replayAll(ctx context.Context, rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}, wid, endpointID string) (int, error) {
	var evs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, err
		}
		evs = append(evs, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck
	for _, id := range evs {
		if _, err := tx.ExecContext(ctx, insertReplay, ids.New("dlv"), id, endpointID, wid); err != nil {
			return 0, err
		}
	}
	return len(evs), tx.Commit()
}
