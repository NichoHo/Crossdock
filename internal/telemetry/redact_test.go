package telemetry_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/nicholasgasior/crossdock/internal/telemetry"
)

func TestLogsHideSecretsButKeepScannedCodes(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{ReplaceAttr: telemetry.RedactAttr}))
	log.Info("request", "authorization", "Bearer abc123", "service_token", "tok-9", "Cookie", "cx_session=zzz",
		"err", "bad token cdk_0123456789abcdef", "barcode", "4900000000001\nINFO forged line", "path", "/v1/x")
	out := buf.String()
	for _, secret := range []string{"abc123", "tok-9", "cx_session=zzz", "cdk_0123456789abcdef"} {
		if strings.Contains(out, secret) {
			t.Errorf("log leaks %q: %s", secret, out)
		}
	}
	if !strings.Contains(out, "4900000000001") {
		t.Errorf("a scanned code should stay visible as a field: %s", out)
	}
	if strings.Count(out, "\n") != 1 {
		t.Errorf("a newline in a scanned code forged a second log line: %q", out)
	}
}
