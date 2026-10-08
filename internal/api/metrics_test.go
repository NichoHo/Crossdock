package api_test

import (
	"testing"

	"github.com/nicholasgasior/crossdock/internal/auth"
)

func TestMetricsExposeRequestsAndAgeingBuckets(t *testing.T) {
	e := newEnv(t)
	st, b := e.do(user("sue", auth.RoleSupervisor), "GET", e.wpath("/stock"), nil)
	expect(t, st, b, 200)
	st, b = e.do(user("sue", auth.RoleSupervisor), "GET", "/metrics", nil)
	expect(t, st, b, 200)
	for _, want := range []string{`crossdock_http_request_duration_seconds_count{route="GET /v1/warehouses/{wid}/stock"} 1`, "crossdock_outbox_pending", "# TYPE crossdock_open_variances gauge"} {
		if !contains(b, want) {
			t.Errorf("metrics missing %q:\n%s", want, b)
		}
	}
}
