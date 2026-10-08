package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Prometheus text exposition, written by hand to avoid a dependency for what is
// one histogram and a handful of gauges (spec 13.2). Request timings are held in
// memory; business gauges are read from the database at scrape time.
// ponytail: the gauge queries scan whole tables, so add counters or a rollup if movements grows past millions.

var latencyBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5}

type routeStat struct {
	counts  []uint64 // cumulative per bucket
	n       uint64
	sum     float64
	byClass map[string]uint64
}

type metrics struct {
	mu     sync.Mutex
	routes map[string]*routeStat
}

func (m *metrics) observe(route string, status int, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.routes == nil {
		m.routes = map[string]*routeStat{}
	}
	st := m.routes[route]
	if st == nil {
		st = &routeStat{counts: make([]uint64, len(latencyBuckets)), byClass: map[string]uint64{}}
		m.routes[route] = st
	}
	sec := d.Seconds()
	for i, b := range latencyBuckets {
		if sec <= b {
			st.counts[i]++
		}
	}
	st.n++
	st.sum += sec
	st.byClass[fmt.Sprintf("%dxx", status/100)]++
}

func (m *metrics) write(sb *strings.Builder) {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.routes))
	for k := range m.routes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	sb.WriteString("# HELP crossdock_http_request_duration_seconds API request latency by route.\n# TYPE crossdock_http_request_duration_seconds histogram\n")
	for _, k := range keys {
		st := m.routes[k]
		for i, b := range latencyBuckets {
			fmt.Fprintf(sb, "crossdock_http_request_duration_seconds_bucket{route=%q,le=\"%g\"} %d\n", k, b, st.counts[i])
		}
		fmt.Fprintf(sb, "crossdock_http_request_duration_seconds_bucket{route=%q,le=\"+Inf\"} %d\ncrossdock_http_request_duration_seconds_sum{route=%q} %g\ncrossdock_http_request_duration_seconds_count{route=%q} %d\n", k, st.n, k, st.sum, k, st.n)
	}
	sb.WriteString("# HELP crossdock_http_requests_total API requests by route and status class.\n# TYPE crossdock_http_requests_total counter\n")
	for _, k := range keys {
		for c, n := range m.routes[k].byClass {
			fmt.Fprintf(sb, "crossdock_http_requests_total{route=%q,class=%q} %d\n", k, c, n)
		}
	}
}

// gauge runs a query returning (label, value) rows and writes one series per row.
func (s *Server) gauge(ctx context.Context, sb *strings.Builder, name, help, typ, label, query string) {
	fmt.Fprintf(sb, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		s.log.Error("metrics query", "metric", name, "err", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var l string
		var v float64
		if rows.Scan(&l, &v) != nil {
			continue
		}
		if label == "" {
			fmt.Fprintf(sb, "%s %g\n", name, v)
		} else {
			fmt.Fprintf(sb, "%s{%s=%q} %g\n", name, label, l, v)
		}
	}
}

func (s *Server) metricsHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var sb strings.Builder
	s.metrics.write(&sb)
	s.gauge(ctx, &sb, "crossdock_movements_total", "Movements ever written, by kind.", "counter", "kind",
		`SELECT kind, count(*) FROM movements GROUP BY kind`)
	s.gauge(ctx, &sb, "crossdock_tasks", "Tasks by kind and state.", "gauge", "state",
		`SELECT kind||'/'||state, count(*) FROM tasks GROUP BY 1`)
	s.gauge(ctx, &sb, "crossdock_task_completions_total", "Completed tasks by operator.", "counter", "operator",
		`SELECT assigned_to, count(*) FROM tasks WHERE state='completed' AND assigned_to<>'' GROUP BY 1`)
	s.gauge(ctx, &sb, "crossdock_outbox_pending", "Outbox rows not yet relayed.", "gauge", "",
		`SELECT '', count(*) FROM outbox WHERE relayed_at IS NULL`)
	s.gauge(ctx, &sb, "crossdock_deliveries", "Webhook deliveries by state (dead is the dead letter depth).", "gauge", "state",
		`SELECT state, count(*) FROM deliveries GROUP BY 1`)
	s.gauge(ctx, &sb, "crossdock_delivery_attempts_total", "Webhook attempts, by outcome.", "counter", "ok",
		`SELECT (status BETWEEN 200 AND 299)::text, count(*) FROM delivery_attempts GROUP BY 1`)
	// The headline row: unresolved items in the four ageing buckets (spec 8.4).
	age := func(table, where string) string {
		return `SELECT CASE WHEN opened_at > NOW()-interval '7 days' THEN 'under_7d' WHEN opened_at > NOW()-interval '30 days' THEN '7_30d'
			WHEN opened_at > NOW()-interval '90 days' THEN '30_90d' ELSE 'over_90d' END, count(*) FROM ` + table + ` WHERE ` + where + ` GROUP BY 1`
	}
	s.gauge(ctx, &sb, "crossdock_open_variances", "Open count variances by age.", "gauge", "bucket", age("variances", "state IN ('awaiting_recount','open')"))
	s.gauge(ctx, &sb, "crossdock_open_inbound_breaks", "Open inbound breaks by age.", "gauge", "bucket", age("recon_breaks", "state='open'"))
	s.gauge(ctx, &sb, "crossdock_open_settlement_breaks", "Open settlement breaks by age.", "gauge", "bucket", age("settlement_breaks", "state='open'"))
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = w.Write([]byte(sb.String()))
}
