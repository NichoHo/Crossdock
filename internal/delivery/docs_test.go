package delivery_test

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/nicholasgasior/crossdock/internal/delivery"
)

// TestDocsSigningVector keeps docs/snippets honest: the signature in the
// published test vector is what the server really produces, and the server's
// own Verify accepts it.
func TestDocsSigningVector(t *testing.T) {
	raw, err := os.ReadFile("../../docs/snippets/vector.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Secret, Timestamp, Signature, Body string
		Now                                int64
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	ts := time.Unix(v.Now-30, 0) // the vector's timestamp is 30 s before its "now"
	if got := delivery.SignatureHeader([]string{v.Secret}, ts, []byte(v.Body)); got != v.Signature {
		t.Errorf("server signs the vector as %s, docs say %s", got, v.Signature)
	}
	if !delivery.Verify([]string{v.Secret}, v.Timestamp, v.Signature, []byte(v.Body), time.Unix(v.Now, 0), 5*time.Minute) {
		t.Error("server Verify rejects the documented vector")
	}
}
