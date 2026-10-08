package counting

import (
	"context"
	"time"

	"github.com/nicholasgasior/crossdock/internal/inventory"
	"github.com/nicholasgasior/crossdock/internal/tasks"
)

// Interval is how often a location holding a velocity class is counted (spec 8.1).
var Interval = map[string]time.Duration{
	"A": 7 * 24 * time.Hour,
	"B": 30 * 24 * time.Hour,
	"C": 90 * 24 * time.Hour,
}

// ScheduleCounts creates a count task for every location that is due, plus every
// location touched by an exception since it was last counted. It is safe to call
// on a timer: a location with an unfinished count task, or already scheduled
// today, gets no second one. It returns how many tasks it created.
func (s *Service) ScheduleCounts(ctx context.Context, wid inventory.WarehouseID) (int, error) {
	now := s.now()
	created := 0

	// Locations holding stock, with the fastest velocity class stored there and
	// the time of their last finished count.
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.location,
		       MIN(CASE sk.velocity_class WHEN 'A' THEN 1 WHEN 'B' THEN 2 ELSE 3 END),
		       (SELECT max(c.completed_at) FROM counts c WHERE c.warehouse_id=p.warehouse_id AND c.location=p.location AND c.state='counted'),
		       EXISTS (SELECT 1 FROM tasks t WHERE t.warehouse_id=p.warehouse_id AND t.kind='count' AND t.from_location=p.location
		               AND t.state IN ('open','assigned','in_progress','exception'))
		FROM positions p
		JOIN skus sk ON sk.warehouse_id=p.warehouse_id AND sk.id=p.sku_id
		JOIN locations lo ON lo.warehouse_id=p.warehouse_id AND lo.code=p.location
		JOIN zones z ON z.id=lo.zone_id AND z.purpose IN ('pick_face','bulk')
		WHERE p.warehouse_id=$1 AND p.available+p.reserved+p.quarantined+p.damaged > 0
		GROUP BY p.warehouse_id, p.location`, string(wid))
	if err != nil {
		return 0, err
	}
	type loc struct {
		code    string
		class   int
		last    *time.Time
		pending bool
	}
	var locs []loc
	for rows.Next() {
		var l loc
		if err := rows.Scan(&l.code, &l.class, &l.last, &l.pending); err != nil {
			rows.Close()
			return 0, err
		}
		locs = append(locs, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	classOf := map[int]string{1: "A", 2: "B", 3: "C"}
	day := now.UTC().Format("20060102")
	for _, l := range locs {
		if l.pending {
			continue
		}
		if l.last != nil && now.Sub(*l.last) < Interval[classOf[l.class]] {
			continue
		}
		_, ok, err := tasks.Create(ctx, s.db, tasks.Task{
			WarehouseID: wid, Kind: inventory.TaskCount, FromLocation: inventory.LocationCode(l.code),
			Ref: "count:" + l.code + ":" + day, Reason: "schedule",
		})
		if err != nil {
			return created, err
		}
		if ok {
			created++
		}
	}

	// Exceptions: a short pick leaves doubt at its source, a putaway to a full slot at its destination.
	erows, err := s.db.QueryContext(ctx, `
		SELECT t.id, CASE t.kind WHEN 'pick' THEN t.from_location ELSE t.to_location END AS loc
		FROM tasks t
		WHERE t.warehouse_id=$1 AND t.state='exception' AND t.kind IN ('pick','putaway')
		  AND CASE t.kind WHEN 'pick' THEN t.from_location ELSE t.to_location END <> ''
		  AND NOT EXISTS (SELECT 1 FROM counts c WHERE c.warehouse_id=t.warehouse_id
		                  AND c.location = CASE t.kind WHEN 'pick' THEN t.from_location ELSE t.to_location END
		                  AND c.completed_at > t.updated_at)
		  AND NOT EXISTS (SELECT 1 FROM tasks k WHERE k.warehouse_id=t.warehouse_id AND k.kind='count'
		                  AND k.from_location = CASE t.kind WHEN 'pick' THEN t.from_location ELSE t.to_location END
		                  AND k.state IN ('open','assigned','in_progress','exception'))`, string(wid))
	if err != nil {
		return created, err
	}
	type exc struct{ task, loc string }
	var excs []exc
	for erows.Next() {
		var e exc
		if err := erows.Scan(&e.task, &e.loc); err != nil {
			erows.Close()
			return created, err
		}
		excs = append(excs, e)
	}
	erows.Close()
	if err := erows.Err(); err != nil {
		return created, err
	}
	for _, e := range excs {
		_, ok, err := tasks.Create(ctx, s.db, tasks.Task{
			WarehouseID: wid, Kind: inventory.TaskCount, FromLocation: inventory.LocationCode(e.loc),
			Ref: "count-exc:" + e.task, Reason: "exception",
		})
		if err != nil {
			return created, err
		}
		if ok {
			created++
		}
	}
	return created, nil
}
