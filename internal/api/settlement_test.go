package api_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/nicholasgasior/crossdock/internal/auth"
	"github.com/nicholasgasior/crossdock/internal/wmstest"
)

// upload sends a CSV body as the courier's finance team would.
func (e *env) upload(who caller, path, csv string) (int, []byte) {
	e.t.Helper()
	req, _ := http.NewRequest("POST", e.ts.URL+path, bytes.NewBufferString(csv))
	req.Header.Set("Content-Type", "text/csv")
	who(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestRejectedParcelAndRemittanceOverHTTP follows a COD parcel the way the
// outside world would: the courier calls the outcome API with a token, the
// operator scans the returned parcel and decides the stock on the scanner,
// the courier's finance team uploads a file, and a supervisor works the case.
func TestRejectedParcelAndRemittanceOverHTTP(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	sup, op, adm := user("sue", auth.RoleSupervisor), user("olga", auth.RoleOperator), user("ada", auth.RoleAdmin)
	_, courierTok, _ := auth.CreateToken(ctx, e.db, "jnt-api", []string{"outcomes:write", "remittances:write"})
	courier := token(courierTok)

	st, b := e.do(adm, "PUT", e.wpath("/carriers/JNT/terms"), map[string]any{"cod_fee_bps": 200, "cod_fee_flat_minor": 1000, "shipping_cost_minor": 4500, "settlement_days": 7})
	expect(t, st, b, 204)
	st, b = e.do(sup, "PUT", e.wpath("/carriers/JNT/terms"), map[string]any{})
	expect(t, st, b, 403)

	cod := int64(100000)
	_, shipA := wmstest.ShipOrder(t, e.db, e.store, e.w, e.w.SKUs[0], 2, &cod, "JNT", "TRK-A")
	_, shipB := wmstest.ShipOrder(t, e.db, e.store, e.w, e.w.SKUs[1], 1, &cod, "JNT", "TRK-B")

	// The courier reports: A rejected, B delivered. A repeat is accepted quietly.
	st, b = e.do(courier, "POST", e.wpath("/delivery-outcomes"), map[string]any{"tracking": "TRK-A", "outcome": "rejected"})
	expect(t, st, b, 201)
	st, b = e.do(courier, "POST", e.wpath("/delivery-outcomes"), map[string]any{"tracking": "TRK-A", "outcome": "rejected"})
	expect(t, st, b, 200)
	st, b = e.do(courier, "POST", e.wpath("/delivery-outcomes"), map[string]any{"tracking": "TRK-B", "outcome": "delivered_collected"})
	expect(t, st, b, 201)
	st, b = e.do(courier, "POST", e.wpath("/delivery-outcomes"), map[string]any{"tracking": "NOPE", "outcome": "lost"})
	expect(t, st, b, 404)
	// A courier token cannot read or do anything else.
	st, b = e.do(courier, "GET", e.wpath("/shipments"), nil)
	expect(t, st, b, 403)

	// The operator scans the returned parcel on the scanner, offline-style via /commands.
	st, b = e.do(op, "GET", e.wpath("/returns/lookup?tracking=TRK-A"), nil)
	expect(t, st, b, 200)
	st, b = e.do(op, "POST", e.wpath("/commands"), map[string]any{"commands": []map[string]any{
		{"key": "ret-1", "type": "return_receive", "payload": map[string]any{"tracking": "TRK-A",
			"lines": []map[string]any{{"sku": e.w.SKUs[0], "qty": 2, "condition": "good"}}}},
	}})
	expect(t, st, b, 200)
	var rcv struct {
		Results []struct {
			Status string
			Result struct {
				Tasks []string `json:"disposition_tasks"`
			}
		}
	}
	mustJSON(t, b, &rcv)
	if len(rcv.Results) != 1 || rcv.Results[0].Status != "applied" || len(rcv.Results[0].Result.Tasks) != 1 {
		t.Fatalf("return_receive = %s", b)
	}
	taskID := rcv.Results[0].Result.Tasks[0]

	// Another operator cannot disposition a task that is not theirs; the assignee can.
	cmd := func(who caller, key string) []byte {
		st, b := e.do(who, "POST", e.wpath("/commands"), map[string]any{"commands": []map[string]any{
			{"key": key, "type": "disposition", "payload": map[string]any{"task_id": taskID, "decision": "restock"}}}})
		expect(t, st, b, 200)
		return b
	}
	if b := cmd(user("mallory", auth.RoleOperator), "d-bad"); !contains(b, `"rejected"`) {
		t.Errorf("an unassigned operator dispositioned the task: %s", b)
	}
	st, b = e.do(sup, "POST", e.wpath("/tasks/"+taskID+"/assign"), map[string]any{"operator": "olga"})
	expect(t, st, b, 204)
	if b := cmd(op, "d-1"); !contains(b, `"applied"`) {
		t.Fatalf("disposition = %s", b)
	}

	// One timeline.
	st, b = e.do(sup, "GET", e.wpath("/shipments/"+shipA+"/timeline"), nil)
	expect(t, st, b, 200)
	for _, want := range []string{"dispatched", "rejected", "Shipping cost lost: 45.00", "Restocked 2 x"} {
		if !contains(b, want) {
			t.Errorf("timeline lacks %q: %s", want, b)
		}
	}

	// The courier's finance team uploads a file: B is short, one tracking number is unknown.
	csv := "courier_ref,tracking,collected,fee,net\nCR-1,TRK-B,1000.00,200.00,800.00\nCR-2,GHOST-9,500.00,20.00,480.00\n"
	st, b = e.upload(courier, e.wpath("/remittances?carrier=JNT&filename=oct.csv"), csv)
	expect(t, st, b, 201)
	var up struct {
		Matched int
		Breaks  map[string]int
	}
	mustJSON(t, b, &up)
	if up.Matched != 0 || up.Breaks["short_remitted"] != 1 || up.Breaks["unknown_shipment"] != 1 {
		t.Fatalf("upload result = %s", b)
	}
	st, b = e.upload(courier, e.wpath("/remittances?carrier=JNT&filename=oct.csv"), csv)
	expect(t, st, b, 200)
	st, b = e.upload(courier, e.wpath("/remittances?carrier=JNT"), "courier_ref,tracking\nx,y\n")
	expect(t, st, b, 422)

	// The supervisor works the short case.
	st, b = e.do(sup, "GET", e.wpath("/settlement-breaks?kind=short_remitted"), nil)
	expect(t, st, b, 200)
	var cases struct {
		Items []struct {
			ID         string
			DeltaMinor int64 `json:"delta_minor"`
			ShipmentID string `json:"shipment_id"`
		}
	}
	mustJSON(t, b, &cases)
	if len(cases.Items) != 1 || cases.Items[0].DeltaMinor != 17000 || cases.Items[0].ShipmentID != shipB {
		t.Fatalf("cases = %s", b)
	}
	id := cases.Items[0].ID
	st, b = e.do(sup, "POST", e.wpath("/settlement-breaks/"+id+"/resolve"), map[string]any{"resolution": "written_off", "note": "agreed with JNT"})
	expect(t, st, b, 422) // no classified reason
	st, b = e.do(sup, "POST", e.wpath("/settlement-breaks/"+id+"/resolve"), map[string]any{"resolution": "written_off", "note": "agreed with JNT", "writeoff_reason": "courier_fee_dispute"})
	expect(t, st, b, 200)

	st, b = e.do(sup, "GET", e.wpath("/settlement/summary"), nil)
	expect(t, st, b, 200)
	var sm map[string]int64
	mustJSON(t, b, &sm)
	if sm["written_off_minor"] != 17000 || sm["shipping_cost_lost_minor"] != 4500 {
		t.Errorf("summary = %s", b)
	}
	st, b = e.do(sup, "GET", e.wpath("/workbench"), nil)
	expect(t, st, b, 200)
	if !contains(b, `"settlement"`) {
		t.Errorf("workbench does not include settlement cases: %s", b)
	}
	st, b = e.do(sup, "GET", e.wpath("/shipments?status=rejected"), nil)
	expect(t, st, b, 200)
	if !contains(b, shipA) || contains(b, shipB) {
		t.Errorf("shipments filtered by status rejected = %s", b)
	}
	wmstest.CheckInvariants(t, e.db, e.store, e.w.WID)
}
