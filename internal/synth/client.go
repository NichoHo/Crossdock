// Package synth drives a running Crossdock API the way people and external
// systems do. The synthetic operators (spec 13.5) and the load scenarios
// (spec 15) share it, so both exercise the real HTTP surface and not the services.
package synth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/nicholasgasior/crossdock/internal/auth"
)

type Client struct {
	Base, Token, SigningKey, WID string
	HTTP                         *http.Client
}

func New(base, token, key, wid string) *Client {
	return &Client{Base: base, Token: token, SigningKey: key, WID: wid, HTTP: &http.Client{Timeout: 60 * time.Second,
		Transport: &http.Transport{MaxIdleConns: 64, MaxIdleConnsPerHost: 64, IdleConnTimeout: 30 * time.Second}}}
}

// Do calls a warehouse-scoped path as user/role. out may be nil. It returns the
// status and the call's wall time; a non-2xx status is an error unless it is 204.
func (c *Client) Do(ctx context.Context, user string, role auth.Role, method, path string, in, out any, hdr ...string) (int, time.Duration, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, 0, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+"/v1/warehouses/"+c.WID+path, body)
	if err != nil {
		return 0, 0, err
	}
	id, sig := auth.SignIdentity(c.SigningKey, user, role, time.Now())
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("X-Crossdock-Identity", id)
	req.Header.Set("X-Crossdock-Signature", sig)
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	start := time.Now()
	res, err := c.HTTP.Do(req)
	if err != nil {
		return 0, time.Since(start), err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	d := time.Since(start)
	if res.StatusCode >= 300 {
		return res.StatusCode, d, fmt.Errorf("%s %s: %d %s", method, path, res.StatusCode, bytes.TrimSpace(raw))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return res.StatusCode, d, err
		}
	}
	return res.StatusCode, d, nil
}

type Command struct {
	Key     string         `json:"key"`
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
}

type CommandResult struct {
	Key, Status, Reason string
	Code                int
}

// Commands posts an offline-style batch as an operator.
func (c *Client) Commands(ctx context.Context, user string, cmds []Command) ([]CommandResult, time.Duration, error) {
	var out struct{ Results []CommandResult }
	_, d, err := c.Do(ctx, user, auth.RoleOperator, "POST", "/commands", map[string]any{"commands": cmds}, &out)
	return out.Results, d, err
}

type Task struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	State        string `json:"state"`
	SKU          string `json:"sku"`
	OrderID      string `json:"order_id"`
	FromLocation string `json:"from_location"`
	ToLocation   string `json:"to_location"`
	Qty          int64  `json:"qty"`
}

// Claim asks for the next task of the given kinds; nil means nothing is waiting.
func (c *Client) Claim(ctx context.Context, user string, kinds ...string) (*Task, time.Duration, error) {
	var t Task
	st, d, err := c.Do(ctx, user, auth.RoleOperator, "POST", "/tasks/claim", map[string]any{"kinds": kinds}, &t)
	if err != nil || st == http.StatusNoContent {
		return nil, d, err
	}
	return &t, d, nil
}
