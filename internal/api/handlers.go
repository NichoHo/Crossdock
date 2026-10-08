package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/lib/pq"
	"github.com/nicholasgasior/crossdock/internal/apperr"
	"github.com/nicholasgasior/crossdock/internal/auth"
	"github.com/nicholasgasior/crossdock/internal/inbound"
	"github.com/nicholasgasior/crossdock/internal/inventory"
	"github.com/nicholasgasior/crossdock/internal/outbound"
	"github.com/nicholasgasior/crossdock/internal/tasks"
)

type handler = func(s *Server, w http.ResponseWriter, r *http.Request, p auth.Principal)

// Routes is the single table of endpoints. api/openapi.yaml must describe
// exactly these (TestOpenAPIMatchesRoutes enforces it).
func Routes() []Route {
	const W = "/v1/warehouses/{wid}"
	op, sup, adm := auth.RoleOperator, auth.RoleSupervisor, auth.RoleAdmin
	return []Route{
		{Method: "GET", Pattern: "/v1/me", MinRole: op, Scope: "", Handler: (*Server).me},
		{Method: "GET", Pattern: "/v1/warehouses", MinRole: op, Scope: "", Handler: (*Server).listWarehouses},
		{Method: "GET", Pattern: W + "/catalogue", MinRole: op, Scope: "", Handler: (*Server).catalogue},
		{Method: "GET", Pattern: W + "/stock", MinRole: op, Scope: "", Handler: (*Server).stock},
		{Method: "GET", Pattern: W + "/movements", MinRole: op, Scope: "", Handler: (*Server).movements},

		{Method: "POST", Pattern: W + "/asns", MinRole: sup, Scope: "asn:write", Handler: (*Server).createASN},
		{Method: "GET", Pattern: W + "/asns", MinRole: op, Scope: "", Handler: (*Server).listASNs},
		{Method: "GET", Pattern: W + "/asns/{id}", MinRole: op, Scope: "", Handler: (*Server).getASN},
		{Method: "POST", Pattern: W + "/asn-lines/{id}/short", MinRole: sup, Scope: "", Handler: (*Server).markShort},
		{Method: "POST", Pattern: W + "/receipts", MinRole: op, Scope: "", Handler: (*Server).receive},

		{Method: "POST", Pattern: W + "/orders", MinRole: sup, Scope: "orders:write", Handler: (*Server).createOrder},
		{Method: "GET", Pattern: W + "/orders", MinRole: sup, Scope: "", Handler: (*Server).listOrders},
		{Method: "GET", Pattern: W + "/orders/{id}", MinRole: sup, Scope: "", Handler: (*Server).getOrder},
		{Method: "POST", Pattern: W + "/orders/{id}/allocate", MinRole: sup, Scope: "", Handler: (*Server).allocate},
		{Method: "POST", Pattern: W + "/orders/{id}/cancel", MinRole: sup, Scope: "orders:write", Handler: (*Server).cancelOrder},
		{Method: "POST", Pattern: W + "/orders/{id}/ship", MinRole: op, Scope: "", Handler: (*Server).ship},
		{Method: "POST", Pattern: W + "/allocation-sweeps", MinRole: sup, Scope: "", Handler: (*Server).sweep},
		{Method: "POST", Pattern: W + "/waves", MinRole: sup, Scope: "", Handler: (*Server).releaseWave},

		{Method: "GET", Pattern: W + "/tasks", MinRole: op, Scope: "", Handler: (*Server).listTasks},
		{Method: "POST", Pattern: W + "/tasks/claim", MinRole: op, Scope: "", Handler: (*Server).claimTask},
		{Method: "POST", Pattern: W + "/tasks/{id}/assign", MinRole: sup, Scope: "", Handler: (*Server).assignTask},
		{Method: "POST", Pattern: W + "/tasks/{id}/start", MinRole: op, Scope: "", Handler: taskCommand("start")},
		{Method: "POST", Pattern: W + "/tasks/{id}/complete", MinRole: op, Scope: "", Handler: (*Server).completeTask},
		{Method: "POST", Pattern: W + "/tasks/{id}/exception", MinRole: op, Scope: "", Handler: taskCommand("exception")},
		{Method: "POST", Pattern: W + "/commands", MinRole: op, Scope: "", Handler: (*Server).commands},
		{Method: "GET", Pattern: W + "/tasks/{id}/count-sheet", MinRole: op, Handler: (*Server).countSheet},

		{Method: "POST", Pattern: W + "/count-schedules", MinRole: sup, Handler: (*Server).scheduleCounts},
		{Method: "GET", Pattern: W + "/variances", MinRole: sup, Handler: (*Server).listVariances},
		{Method: "GET", Pattern: W + "/variances/{id}", MinRole: sup, Handler: (*Server).getVariance},
		{Method: "POST", Pattern: W + "/variances/{id}/confirm", MinRole: sup, Handler: (*Server).confirmVariance},
		{Method: "GET", Pattern: W + "/recon-breaks", MinRole: sup, Handler: (*Server).listBreaks},
		{Method: "POST", Pattern: W + "/recon-breaks/{id}/waive", MinRole: sup, Handler: (*Server).waiveBreak},
		{Method: "POST", Pattern: W + "/reconciliations", MinRole: sup, Handler: (*Server).runReconciliation},
		{Method: "GET", Pattern: W + "/workbench", MinRole: sup, Handler: (*Server).workbench},

		{Method: "POST", Pattern: W + "/delivery-outcomes", MinRole: sup, Scope: "outcomes:write", Handler: (*Server).recordOutcome},
		{Method: "GET", Pattern: W + "/shipments", MinRole: sup, Handler: (*Server).listShipments},
		{Method: "GET", Pattern: W + "/shipments/{id}/timeline", MinRole: sup, Handler: (*Server).shipmentTimeline},
		{Method: "GET", Pattern: W + "/carriers", MinRole: sup, Handler: (*Server).listCarriers},
		{Method: "PUT", Pattern: W + "/carriers/{carrier}/terms", MinRole: adm, Handler: (*Server).putCarrierTerms},
		{Method: "POST", Pattern: W + "/remittances", MinRole: sup, Scope: "remittances:write", MaxBody: 8 << 20, Handler: (*Server).uploadRemittance},
		{Method: "GET", Pattern: W + "/remittances", MinRole: sup, Handler: (*Server).listRemittances},
		{Method: "POST", Pattern: W + "/settlement-scans", MinRole: sup, Handler: (*Server).settlementScan},
		{Method: "GET", Pattern: W + "/settlement-breaks", MinRole: sup, Handler: (*Server).listSettlementBreaks},
		{Method: "POST", Pattern: W + "/settlement-breaks/{id}/resolve", MinRole: sup, Handler: (*Server).resolveSettlementBreak},
		{Method: "GET", Pattern: W + "/settlement/summary", MinRole: sup, Handler: (*Server).settlementSummary},
		{Method: "GET", Pattern: W + "/returns", MinRole: op, Handler: (*Server).listReturns},
		{Method: "GET", Pattern: W + "/returns/lookup", MinRole: op, Handler: (*Server).lookupReturn},

		{Method: "GET", Pattern: W + "/slotting/proposals", MinRole: sup, Handler: (*Server).listProposals},
		{Method: "GET", Pattern: W + "/slotting/proposals/{id}", MinRole: sup, Handler: (*Server).getProposal},
		{Method: "POST", Pattern: W + "/slotting/proposals/{id}/replay", MinRole: sup, Handler: (*Server).replayProposal},
		{Method: "POST", Pattern: W + "/slotting/proposals/{id}/accept", MinRole: sup, Handler: (*Server).acceptProposal},
		{Method: "POST", Pattern: W + "/slotting/proposals/{id}/reject", MinRole: sup, Handler: (*Server).rejectProposal},
		{Method: "GET", Pattern: W + "/floor-map", MinRole: sup, Handler: (*Server).floorMap},

		{Method: "POST", Pattern: "/v1/tokens", MinRole: adm, Secret: true, Handler: (*Server).createToken},
		{Method: "GET", Pattern: "/v1/tokens", MinRole: adm, Scope: "", Handler: (*Server).listTokens},
		{Method: "DELETE", Pattern: "/v1/tokens/{id}", MinRole: adm, Scope: "", Handler: (*Server).revokeToken},

		{Method: "POST", Pattern: W + "/endpoints", MinRole: adm, Secret: true, Handler: (*Server).createEndpoint},
		{Method: "GET", Pattern: W + "/endpoints", MinRole: sup, Handler: (*Server).listEndpoints},
		{Method: "GET", Pattern: W + "/endpoints/{id}", MinRole: sup, Handler: (*Server).getEndpoint},
		{Method: "DELETE", Pattern: W + "/endpoints/{id}", MinRole: adm, Handler: (*Server).disableEndpoint},
		{Method: "POST", Pattern: W + "/endpoints/{id}/rotate-secret", MinRole: adm, Secret: true, Handler: (*Server).rotateSecret},
		{Method: "GET", Pattern: W + "/endpoints/{id}/deliveries", MinRole: sup, Handler: (*Server).listDeliveries},
		{Method: "GET", Pattern: W + "/endpoints/{id}/deliveries/{did}/attempts", MinRole: sup, Handler: (*Server).deliveryAttempts},
		{Method: "POST", Pattern: W + "/endpoints/{id}/replay", MinRole: sup, Handler: (*Server).replay},
	}
}

// wid reads the warehouse from the path and 404s if it does not exist.
func (s *Server) wid(w http.ResponseWriter, r *http.Request) (inventory.WarehouseID, bool) {
	id := r.PathValue("wid")
	var ok bool
	if err := s.db.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM warehouses WHERE id=$1)`, id).Scan(&ok); err != nil {
		s.problem(w, r, err)
		return "", false
	}
	if !ok {
		s.problem(w, r, &httpErr{http.StatusNotFound, "not found", "no such warehouse"})
		return "", false
	}
	return inventory.WarehouseID(id), true
}

func limitParam(r *http.Request) int {
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if n <= 0 || n > 200 {
		return 50
	}
	return n
}

type page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// ---------------------------------------------------------------------------
// Identity and reference data
// ---------------------------------------------------------------------------

func (s *Server) me(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	writeJSON(w, 200, map[string]any{"user": p.User, "role": p.Role, "system": p.System, "scopes": p.Scopes})
}

func (s *Server) listWarehouses(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	rows, err := s.db.QueryContext(r.Context(), `SELECT id, name, time_zone FROM warehouses ORDER BY id`)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	defer rows.Close()
	items := []map[string]string{}
	for rows.Next() {
		var id, name, tz string
		if err := rows.Scan(&id, &name, &tz); err != nil {
			s.problem(w, r, err)
			return
		}
		items = append(items, map[string]string{"id": id, "name": name, "time_zone": tz})
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

// catalogue is the snapshot the scanner caches for offline use. It is not
// paginated on purpose: a client needs the whole set to work offline.
func (s *Server) catalogue(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	type sku struct {
		ID      string `json:"id"`
		Barcode string `json:"barcode"`
		Name    string `json:"name"`
	}
	type loc struct {
		Code string `json:"code"`
		Zone string `json:"zone"`
	}
	out := struct {
		SKUs      []sku `json:"skus"`
		Locations []loc `json:"locations"`
	}{SKUs: []sku{}, Locations: []loc{}}
	rows, err := s.db.QueryContext(r.Context(), `SELECT id, barcode, name FROM skus WHERE warehouse_id=$1 ORDER BY id`, string(wid))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	for rows.Next() {
		var x sku
		if err := rows.Scan(&x.ID, &x.Barcode, &x.Name); err != nil {
			rows.Close()
			s.problem(w, r, err)
			return
		}
		out.SKUs = append(out.SKUs, x)
	}
	rows.Close()
	rows, err = s.db.QueryContext(r.Context(), `
		SELECT l.code, z.purpose FROM locations l JOIN zones z ON z.id=l.zone_id WHERE l.warehouse_id=$1 ORDER BY l.code`, string(wid))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var x loc
		if err := rows.Scan(&x.Code, &x.Zone); err != nil {
			s.problem(w, r, err)
			return
		}
		out.Locations = append(out.Locations, x)
	}
	writeJSON(w, 200, out)
}

type positionJSON struct {
	SKU         string `json:"sku"`
	Lot         string `json:"lot"`
	Location    string `json:"location"`
	LPN         string `json:"lpn"`
	Available   int64  `json:"available"`
	Reserved    int64  `json:"reserved"`
	InTransit   int64  `json:"in_transit"`
	Quarantined int64  `json:"quarantined"`
	Damaged     int64  `json:"damaged"`
}

// stock is the stock explorer query: non-zero positions, filterable.
func (s *Server) stock(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	cur, err := decodeCursor(r.URL.Query().Get("cursor"), 4)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	q := r.URL.Query()
	limit := limitParam(r)
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT sku_id, lot_id, location, lpn_id, available, reserved, in_transit, quarantined, damaged
		FROM positions
		WHERE warehouse_id=$1 AND ($2='' OR sku_id=$2) AND ($3='' OR location=$3)
		  AND ($4 = '' OR (location, sku_id, lot_id, lpn_id) > ($4, $5, $6, $7))
		  AND location NOT IN ('SUPPLIER','CUSTOMER','CARRIER','VARIANCE','SCRAP','RETURN_INTAKE')
		  AND (available<>0 OR reserved<>0 OR in_transit<>0 OR quarantined<>0 OR damaged<>0)
		ORDER BY location, sku_id, lot_id, lpn_id LIMIT $8`,
		string(wid), q.Get("sku"), q.Get("location"), cur[0], cur[1], cur[2], cur[3], limit+1)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	defer rows.Close()
	out := page[positionJSON]{Items: []positionJSON{}}
	for rows.Next() {
		var x positionJSON
		if err := rows.Scan(&x.SKU, &x.Lot, &x.Location, &x.LPN, &x.Available, &x.Reserved, &x.InTransit, &x.Quarantined, &x.Damaged); err != nil {
			s.problem(w, r, err)
			return
		}
		out.Items = append(out.Items, x)
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		l := out.Items[limit-1]
		out.NextCursor = encodeCursor(l.Location, l.SKU, l.Lot, l.LPN)
	}
	writeJSON(w, 200, out)
}

type movementJSON struct {
	Seq        int64     `json:"seq"`
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	From       string    `json:"from_location"`
	FromState  string    `json:"from_state"`
	To         string    `json:"to_location"`
	ToState    string    `json:"to_state"`
	SKU        string    `json:"sku"`
	Lot        string    `json:"lot"`
	LPN        string    `json:"lpn"`
	Qty        int64     `json:"qty"`
	OccurredAt time.Time `json:"occurred_at"`
	TaskID     string    `json:"task_id,omitempty"`
	OrderID    string    `json:"order_id,omitempty"`
}

// movements is the timeline query: the log in seq order, filterable by SKU,
// location, order or task.
func (s *Server) movements(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	after, _ := strconv.ParseInt(q.Get("cursor"), 10, 64)
	limit := limitParam(r)
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT seq, id, kind, from_location, from_state, to_location, to_state, sku_id, lot_id, lpn_id, qty,
		       occurred_at, task_id, order_id
		FROM movements
		WHERE warehouse_id=$1 AND seq > $2
		  AND ($3='' OR sku_id=$3) AND ($4='' OR from_location=$4 OR to_location=$4)
		  AND ($5='' OR order_id=$5) AND ($6='' OR task_id=$6)
		ORDER BY seq LIMIT $7`,
		string(wid), after, q.Get("sku"), q.Get("location"), q.Get("order_id"), q.Get("task_id"), limit+1)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	defer rows.Close()
	out := page[movementJSON]{Items: []movementJSON{}}
	for rows.Next() {
		var x movementJSON
		if err := rows.Scan(&x.Seq, &x.ID, &x.Kind, &x.From, &x.FromState, &x.To, &x.ToState, &x.SKU, &x.Lot, &x.LPN,
			&x.Qty, &x.OccurredAt, &x.TaskID, &x.OrderID); err != nil {
			s.problem(w, r, err)
			return
		}
		out.Items = append(out.Items, x)
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		out.NextCursor = strconv.FormatInt(out.Items[limit-1].Seq, 10)
	}
	writeJSON(w, 200, out)
}

// ---------------------------------------------------------------------------
// Inbound
// ---------------------------------------------------------------------------

type asnLineJSON struct {
	ID          string `json:"id,omitempty"`
	SKU         string `json:"sku"`
	Lot         string `json:"lot,omitempty"`
	ExpectedQty int64  `json:"expected_qty"`
	ReceivedQty int64  `json:"received_qty,omitempty"`
	Short       bool   `json:"short,omitempty"`
}

type asnJSON struct {
	ID       string        `json:"id"`
	Ref      string        `json:"ref"`
	Supplier string        `json:"supplier"`
	State    string        `json:"state"`
	Lines    []asnLineJSON `json:"lines"`
}

func toASN(a inbound.ASN) asnJSON {
	out := asnJSON{ID: a.ID, Ref: a.Ref, Supplier: a.Supplier, State: a.State, Lines: []asnLineJSON{}}
	for _, l := range a.Lines {
		out.Lines = append(out.Lines, asnLineJSON{l.ID, string(l.SKU), string(l.Lot), l.ExpectedQty, l.ReceivedQty, l.Short})
	}
	return out
}

func (s *Server) createASN(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	var in struct {
		Ref      string        `json:"ref"`
		Supplier string        `json:"supplier"`
		Lines    []asnLineJSON `json:"lines"`
	}
	if err := decode(r, &in); err != nil {
		s.problem(w, r, err)
		return
	}
	lines := make([]inbound.ASNLine, len(in.Lines))
	for i, l := range in.Lines {
		lines[i] = inbound.ASNLine{SKU: inventory.SKUID(l.SKU), Lot: inventory.LotID(l.Lot), ExpectedQty: l.ExpectedQty}
	}
	a, err := s.inbound.CreateASN(r.Context(), wid, in.Ref, in.Supplier, lines)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toASN(a))
}

func (s *Server) getASN(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	a, err := s.inbound.GetASN(r.Context(), wid, r.PathValue("id"))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, 200, toASN(a))
}

func (s *Server) listASNs(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	cur, err := decodeCursor(r.URL.Query().Get("cursor"), 2)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	var afterTS time.Time
	if cur[0] != "" {
		afterTS, _ = time.Parse(time.RFC3339Nano, cur[0])
	}
	limit := limitParam(r)
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id, ref, supplier, state, created_at FROM asns
		WHERE warehouse_id=$1 AND ($2='' OR state=$2) AND ($4='' OR (created_at, id) > ($3, $4))
		ORDER BY created_at, id LIMIT $5`, string(wid), r.URL.Query().Get("state"), afterTS, cur[1], limit+1)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	defer rows.Close()
	type item struct {
		ID, Ref, Supplier, State string
		At                       time.Time
	}
	var items []item
	for rows.Next() {
		var x item
		if err := rows.Scan(&x.ID, &x.Ref, &x.Supplier, &x.State, &x.At); err != nil {
			s.problem(w, r, err)
			return
		}
		items = append(items, x)
	}
	out := page[map[string]string]{Items: []map[string]string{}}
	for i, x := range items {
		if i == limit {
			l := items[limit-1]
			out.NextCursor = encodeCursor(l.At.Format(time.RFC3339Nano), l.ID)
			break
		}
		out.Items = append(out.Items, map[string]string{"id": x.ID, "ref": x.Ref, "supplier": x.Supplier, "state": x.State})
	}
	writeJSON(w, 200, out)
}

func (s *Server) markShort(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	if err := s.inbound.MarkShort(r.Context(), wid, r.PathValue("id")); err != nil {
		s.problem(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type receiveBody struct {
	ASNLineID string     `json:"asn_line_id"`
	Qty       int64      `json:"qty"`
	LPN       string     `json:"lpn"`
	Location  string     `json:"location"`
	Lot       string     `json:"lot"`
	ExpiresAt *time.Time `json:"expires_at"`
}

func (s *Server) receive(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		s.problem(w, r, apperr.Invalid("Idempotency-Key header is required"))
		return
	}
	var in receiveBody
	if err := decode(r, &in); err != nil {
		s.problem(w, r, err)
		return
	}
	res, err := s.doReceive(r.Context(), wid, key, in)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

func (s *Server) doReceive(ctx context.Context, wid inventory.WarehouseID, key string, in receiveBody) (map[string]any, error) {
	rc, err := s.inbound.Receive(ctx, inbound.ReceiveInput{
		WarehouseID: wid, ASNLineID: in.ASNLineID, Qty: in.Qty, LPN: inventory.LPNID(in.LPN),
		Location: inventory.LocationCode(in.Location), Lot: inventory.LotID(in.Lot), ExpiresAt: in.ExpiresAt, Key: key,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"receipt_id": rc.ID, "putaway_task_id": rc.PutawayID,
		"putaway_location": rc.ToLocation, "duplicate": rc.Duplicate}, nil
}

// ---------------------------------------------------------------------------
// Outbound
// ---------------------------------------------------------------------------

type orderLineJSON struct {
	ID           string `json:"id,omitempty"`
	SKU          string `json:"sku"`
	Qty          int64  `json:"qty"`
	AllocatedQty int64  `json:"allocated_qty,omitempty"`
}

type orderJSON struct {
	ID     string          `json:"id"`
	Ref    string          `json:"ref"`
	State  string          `json:"state"`
	WaveID string          `json:"wave_id,omitempty"`
	Lines  []orderLineJSON `json:"lines,omitempty"`
}

func toOrder(o outbound.Order) orderJSON {
	out := orderJSON{ID: o.ID, Ref: o.Ref, State: o.State, WaveID: o.WaveID}
	for _, l := range o.Lines {
		out.Lines = append(out.Lines, orderLineJSON{l.ID, string(l.SKU), l.Qty, l.AllocatedQty})
	}
	return out
}

func (s *Server) createOrder(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	var in struct {
		Ref   string          `json:"ref"`
		Lines []orderLineJSON `json:"lines"`
	}
	if err := decode(r, &in); err != nil {
		s.problem(w, r, err)
		return
	}
	lines := make([]outbound.OrderLine, len(in.Lines))
	for i, l := range in.Lines {
		lines[i] = outbound.OrderLine{SKU: inventory.SKUID(l.SKU), Qty: l.Qty}
	}
	o, err := s.out.CreateOrder(r.Context(), wid, in.Ref, lines)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toOrder(o))
}

func (s *Server) getOrder(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	o, err := s.out.GetOrder(r.Context(), wid, r.PathValue("id"))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, 200, toOrder(o))
}

func (s *Server) listOrders(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	cur, err := decodeCursor(r.URL.Query().Get("cursor"), 2)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	var afterTS time.Time
	if cur[0] != "" {
		afterTS, _ = time.Parse(time.RFC3339Nano, cur[0])
	}
	limit := limitParam(r)
	// Orders carry no created_at in the domain type; read it for the cursor here.
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT id, ref, state, wave_id, created_at FROM orders
		WHERE warehouse_id=$1 AND ($2='' OR state=$2) AND ($4='' OR (created_at, id) > ($3, $4))
		ORDER BY created_at, id LIMIT $5`, string(wid), r.URL.Query().Get("state"), afterTS, cur[1], limit+1)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	defer rows.Close()
	out := page[orderJSON]{Items: []orderJSON{}}
	var stamps []time.Time
	for rows.Next() {
		var x orderJSON
		var at time.Time
		if err := rows.Scan(&x.ID, &x.Ref, &x.State, &x.WaveID, &at); err != nil {
			s.problem(w, r, err)
			return
		}
		out.Items = append(out.Items, x)
		stamps = append(stamps, at)
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		out.NextCursor = encodeCursor(stamps[limit-1].Format(time.RFC3339Nano), out.Items[limit-1].ID)
	}
	writeJSON(w, 200, out)
}

func (s *Server) allocate(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	st, err := s.out.Allocate(r.Context(), wid, r.PathValue("id"))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]string{"state": st})
}

func (s *Server) sweep(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	n, err := s.out.Sweep(r.Context(), wid)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]int{"changed": n})
}

func (s *Server) cancelOrder(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	if err := s.out.Cancel(r.Context(), wid, r.PathValue("id")); err != nil {
		s.problem(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) releaseWave(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	var in struct {
		OrderIDs []string `json:"order_ids"`
	}
	if err := decode(r, &in); err != nil {
		s.problem(w, r, err)
		return
	}
	wv, err := s.out.ReleaseWave(r.Context(), wid, in.OrderIDs)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": wv.ID, "route_distance_m": wv.RouteDistance, "pick_tasks": wv.PickTasks})
}

type shipBody struct {
	Carrier  string `json:"carrier"`
	Tracking string `json:"tracking"`
	COD      *int64 `json:"cod_amount_minor"`
}

func (s *Server) ship(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	var in shipBody
	if err := decode(r, &in); err != nil {
		s.problem(w, r, err)
		return
	}
	res, err := s.doShip(r.Context(), wid, r.PathValue("id"), in)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

func (s *Server) doShip(ctx context.Context, wid inventory.WarehouseID, orderID string, in shipBody) (map[string]any, error) {
	sh, err := s.out.Ship(ctx, wid, orderID, in.Carrier, in.Tracking, in.COD)
	if err != nil {
		return nil, err
	}
	return map[string]any{"shipment_id": sh.ID, "order_id": sh.OrderID, "carrier": sh.Carrier,
		"tracking": sh.Tracking, "cod_amount_minor": sh.CODAmountMinor}, nil
}

// ---------------------------------------------------------------------------
// Tasks
// ---------------------------------------------------------------------------

type taskJSON struct {
	ID              string    `json:"id"`
	Kind            string    `json:"kind"`
	State           string    `json:"state"`
	AssignedTo      string    `json:"assigned_to,omitempty"`
	SKU             string    `json:"sku,omitempty"`
	Lot             string    `json:"lot,omitempty"`
	LPN             string    `json:"lpn,omitempty"`
	FromLocation    string    `json:"from_location,omitempty"`
	ToLocation      string    `json:"to_location,omitempty"`
	Qty             int64     `json:"qty"`
	OrderID         string    `json:"order_id,omitempty"`
	WaveID          string    `json:"wave_id,omitempty"`
	RouteSeq        int       `json:"route_seq,omitempty"`
	ExceptionReason string    `json:"exception_reason,omitempty"`
	Reason          string    `json:"reason,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

func toTask(t tasks.Task) taskJSON {
	return taskJSON{t.ID, string(t.Kind), string(t.State), t.AssignedTo, string(t.SKU), string(t.Lot), string(t.LPN),
		string(t.FromLocation), string(t.ToLocation), t.Qty, t.OrderID, t.WaveID, t.RouteSeq, t.ExceptionReason, t.Reason, t.CreatedAt}
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := tasks.Filter{State: inventory.TaskState(q.Get("state")), Kind: inventory.TaskKind(q.Get("kind")), AssignedTo: q.Get("assigned_to")}
	if p.Role == auth.RoleOperator { // operators see only their own work
		f.AssignedTo = p.User
	}
	cur, err := decodeCursor(q.Get("cursor"), 2)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	var afterTS time.Time
	if cur[0] != "" {
		afterTS, _ = time.Parse(time.RFC3339Nano, cur[0])
	}
	limit := limitParam(r)
	ts, err := tasks.List(r.Context(), s.db, wid, f, afterTS, cur[1], limit+1)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	out := page[taskJSON]{Items: []taskJSON{}}
	for i, t := range ts {
		if i == limit {
			l := ts[limit-1]
			out.NextCursor = encodeCursor(l.CreatedAt.Format(time.RFC3339Nano), l.ID)
			break
		}
		out.Items = append(out.Items, toTask(t))
	}
	writeJSON(w, 200, out)
}

func (s *Server) claimTask(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	var in struct {
		Kinds []string `json:"kinds"`
	}
	if r.ContentLength != 0 {
		if err := decode(r, &in); err != nil {
			s.problem(w, r, err)
			return
		}
	}
	kinds := make([]inventory.TaskKind, len(in.Kinds))
	for i, k := range in.Kinds {
		kinds[i] = inventory.TaskKind(k)
	}
	t, err := tasks.Claim(r.Context(), s.db, wid, p.User, kinds)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	if t == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, 200, toTask(*t))
}

func (s *Server) assignTask(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	var in struct {
		Operator string `json:"operator"`
	}
	if err := decode(r, &in); err != nil {
		s.problem(w, r, err)
		return
	}
	if in.Operator == "" {
		s.problem(w, r, apperr.Invalid("operator is required"))
		return
	}
	if err := tasks.Assign(r.Context(), s.db, wid, r.PathValue("id"), in.Operator); err != nil {
		s.problem(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func taskCommand(typ string) handler {
	return func(s *Server, w http.ResponseWriter, r *http.Request, p auth.Principal) {
		wid, ok := s.wid(w, r)
		if !ok {
			return
		}
		var raw json.RawMessage = []byte("{}")
		if r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
				s.problem(w, r, &httpErr{http.StatusBadRequest, "bad request body", err.Error()})
				return
			}
		}
		raw = withTaskID(raw, r.PathValue("id"))
		res, err := s.execute(r.Context(), p, wid, typ, r.Header.Get("Idempotency-Key"), raw)
		if err != nil {
			s.problem(w, r, err)
			return
		}
		writeJSON(w, 200, res)
	}
}

// completeTask completes whichever kind of task it is (putaway, pick or pack).
func (s *Server) completeTask(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	t, err := tasks.Get(r.Context(), s.db, wid, r.PathValue("id"))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	var raw json.RawMessage = []byte("{}")
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			s.problem(w, r, &httpErr{http.StatusBadRequest, "bad request body", err.Error()})
			return
		}
	}
	res, err := s.execute(r.Context(), p, wid, string(t.Kind), r.Header.Get("Idempotency-Key"), withTaskID(raw, t.ID))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	writeJSON(w, 200, res)
}

func withTaskID(raw json.RawMessage, id string) json.RawMessage {
	m := map[string]json.RawMessage{}
	_ = json.Unmarshal(raw, &m)
	b, _ := json.Marshal(id)
	m["task_id"] = b
	out, _ := json.Marshal(m)
	return out
}

// ---------------------------------------------------------------------------
// Command execution (shared by single-call endpoints and offline sync)
// ---------------------------------------------------------------------------

// execute runs one operator command. Types: receive, start, putaway, pick,
// pack, exception, ship. Operators may only act on tasks assigned to them.
func (s *Server) execute(ctx context.Context, p auth.Principal, wid inventory.WarehouseID, typ, key string, payload json.RawMessage) (any, error) {
	strict := func(v any) error {
		dec := json.NewDecoder(bytes.NewReader(payload))
		dec.DisallowUnknownFields()
		if err := dec.Decode(v); err != nil {
			return apperr.Invalid("payload for %s: %v", typ, err)
		}
		return nil
	}
	taskCmd := func(want inventory.TaskKind, do func(id, dest string) error) (any, error) {
		var in struct {
			TaskID      string `json:"task_id"`
			Destination string `json:"destination"`
			Reason      string `json:"reason"`
		}
		if err := strict(&in); err != nil {
			return nil, err
		}
		t, err := tasks.Get(ctx, s.db, wid, in.TaskID)
		if err != nil {
			return nil, err
		}
		if p.Role == auth.RoleOperator && t.AssignedTo != p.User {
			return nil, tasks.ErrNotAssignee
		}
		if want != "" && t.Kind != want {
			return nil, apperr.Invalid("task %s is a %s task, not %s", t.ID, t.Kind, want)
		}
		if typ == "exception" {
			if in.Reason == "" {
				return nil, apperr.Invalid("reason is required")
			}
			return map[string]string{"task_id": t.ID}, tasks.Raise(ctx, s.db, wid, t.ID, in.Reason)
		}
		return map[string]string{"task_id": t.ID}, do(t.ID, in.Destination)
	}

	switch typ {
	case "receive":
		var in receiveBody
		if err := strict(&in); err != nil {
			return nil, err
		}
		if key == "" {
			return nil, apperr.Invalid("an idempotency key is required")
		}
		return s.doReceive(ctx, wid, key, in)
	case "ship":
		var in struct {
			OrderID string `json:"order_id"`
			shipBody
		}
		if err := strict(&in); err != nil {
			return nil, err
		}
		return s.doShip(ctx, wid, in.OrderID, in.shipBody)
	case "start":
		return taskCmd("", func(id, _ string) error { return tasks.Start(ctx, s.db, wid, id, p.User) })
	case "exception":
		return taskCmd("", nil)
	case string(inventory.TaskPutaway):
		return taskCmd(inventory.TaskPutaway, func(id, dest string) error {
			return s.inbound.CompletePutaway(ctx, wid, id, inventory.LocationCode(dest))
		})
	case string(inventory.TaskPick):
		return taskCmd(inventory.TaskPick, func(id, _ string) error { return s.out.CompletePick(ctx, wid, id) })
	case string(inventory.TaskPack):
		return taskCmd(inventory.TaskPack, func(id, _ string) error { return s.out.CompletePack(ctx, wid, id) })
	case "count_start", string(inventory.TaskCount):
		return s.executeCount(ctx, p, wid, typ, strict)
	case "return_receive", string(inventory.TaskDisposition):
		return s.executeReturn(ctx, p, wid, typ, key, strict)
	case string(inventory.TaskRelocate):
		return s.executeRelocate(ctx, p, wid, strict)
	}
	return nil, apperr.Invalid("unknown command type %q", typ)
}

// checkTaskOwner lets an operator act only on a task assigned to them.
func (s *Server) checkTaskOwner(ctx context.Context, p auth.Principal, wid inventory.WarehouseID, taskID string) error {
	t, err := tasks.Get(ctx, s.db, wid, taskID)
	if err != nil {
		return err
	}
	if p.Role == auth.RoleOperator && t.AssignedTo != p.User {
		return tasks.ErrNotAssignee
	}
	return nil
}

type commandIn struct {
	Key     string          `json:"key"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type commandResult struct {
	Key    string `json:"key"`
	Status string `json:"status"` // applied | duplicate | rejected
	Code   int    `json:"code"`
	Reason string `json:"reason,omitempty"`
	Result any    `json:"result,omitempty"`
}

// commands applies a batch of client-keyed commands in order (offline sync,
// spec 5.3). Each command is idempotent on its key: a repeat returns the
// stored outcome with status "duplicate". A rejected command does not stop
// the batch; its reason is returned for the operator.
func (s *Server) commands(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	wid, ok := s.wid(w, r)
	if !ok {
		return
	}
	var in struct {
		Commands []commandIn `json:"commands"`
	}
	if err := decode(r, &in); err != nil {
		s.problem(w, r, err)
		return
	}
	if len(in.Commands) == 0 || len(in.Commands) > 200 {
		s.problem(w, r, apperr.Invalid("send between 1 and 200 commands"))
		return
	}
	out := make([]commandResult, 0, len(in.Commands))
	for _, c := range in.Commands {
		out = append(out, s.runCommand(r.Context(), p, wid, c))
	}
	writeJSON(w, 200, map[string]any{"results": out})
}

func (s *Server) runCommand(ctx context.Context, p auth.Principal, wid inventory.WarehouseID, c commandIn) commandResult {
	if c.Key == "" {
		return commandResult{Key: c.Key, Status: "rejected", Code: 422, Reason: "key is required"}
	}
	sum := sha256.Sum256(append([]byte(c.Type+"\n"), c.Payload...))
	sig := hex.EncodeToString(sum[:])
	scope, dbKey := p.ID(), "cmd:"+string(wid)+":"+c.Key

	var storedSig string
	var stored []byte
	err := s.db.QueryRowContext(ctx, `SELECT request_sig, response FROM idempotency_keys WHERE scope=$1 AND key=$2`, scope, dbKey).
		Scan(&storedSig, &stored)
	if err == nil {
		if storedSig != sig {
			return commandResult{Key: c.Key, Status: "rejected", Code: 422, Reason: "key reused with a different command"}
		}
		var prev commandResult
		_ = json.Unmarshal(stored, &prev)
		prev.Status = "duplicate"
		return prev
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return commandResult{Key: c.Key, Status: "rejected", Code: 500, Reason: "temporary failure; retry"}
	}

	res, execErr := s.execute(ctx, p, wid, c.Type, c.Key, c.Payload)
	var cr commandResult
	if execErr != nil {
		he := toHTTPErr(execErr)
		if he.status >= 500 { // transient: do not record, let the client retry
			s.log.Error("command failed", "type", c.Type, "key", c.Key, "err", execErr)
			return commandResult{Key: c.Key, Status: "rejected", Code: 500, Reason: "temporary failure; retry"}
		}
		cr = commandResult{Key: c.Key, Status: "rejected", Code: he.status, Reason: he.title + ": " + he.detail}
	} else {
		cr = commandResult{Key: c.Key, Status: "applied", Code: 200, Result: res}
	}
	b, _ := json.Marshal(cr)
	_, _ = s.db.ExecContext(context.WithoutCancel(ctx), `
		INSERT INTO idempotency_keys (key, scope, request_sig, status, response) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT DO NOTHING`, dbKey, scope, sig, cr.Code, b)
	return cr
}

// ---------------------------------------------------------------------------
// Admin: API tokens
// ---------------------------------------------------------------------------

var validScopes = map[string]bool{"orders:write": true, "asn:write": true, "events:read": true, "outcomes:write": true, "remittances:write": true}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	var in struct {
		Name   string   `json:"name"`
		Scopes []string `json:"scopes"`
	}
	if err := decode(r, &in); err != nil {
		s.problem(w, r, err)
		return
	}
	if in.Name == "" || len(in.Scopes) == 0 {
		s.problem(w, r, apperr.Invalid("name and at least one scope are required"))
		return
	}
	for _, sc := range in.Scopes {
		if !validScopes[sc] {
			s.problem(w, r, apperr.Invalid("unknown scope %q", sc))
			return
		}
	}
	id, plain, err := auth.CreateToken(r.Context(), s.db, in.Name, in.Scopes)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	// The plaintext is shown once and never stored.
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "name": in.Name, "scopes": in.Scopes, "token": plain})
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	rows, err := s.db.QueryContext(r.Context(), `SELECT id, name, scopes, created_at, revoked_at FROM api_tokens ORDER BY created_at`)
	if err != nil {
		s.problem(w, r, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, name string
		var scopes pq.StringArray
		var created time.Time
		var revoked *time.Time
		if err := rows.Scan(&id, &name, &scopes, &created, &revoked); err != nil {
			s.problem(w, r, err)
			return
		}
		items = append(items, map[string]any{"id": id, "name": name, "scopes": []string(scopes), "created_at": created, "revoked_at": revoked})
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	res, err := s.db.ExecContext(r.Context(), `UPDATE api_tokens SET revoked_at=NOW() WHERE id=$1 AND revoked_at IS NULL`, r.PathValue("id"))
	if err != nil {
		s.problem(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		s.problem(w, r, &httpErr{http.StatusNotFound, "not found", "no active token with that id"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
