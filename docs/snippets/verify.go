//go:build ignore

package main

// ANCHOR: verify
import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

// Verify reports whether signatureHeader carries a valid signature for body.
// secrets: every currently valid signing secret (two during a rotation).
// body: the raw request body, before any JSON parsing.
func Verify(secrets []string, timestamp, signatureHeader string, body []byte, now time.Time, window time.Duration) bool {
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || math.Abs(float64(now.Unix()-ts)) > window.Seconds() {
		return false
	}
	for _, secret := range secrets {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(timestamp + "."))
		mac.Write(body)
		want := "v1=" + hex.EncodeToString(mac.Sum(nil))
		for _, got := range strings.Split(signatureHeader, ",") {
			if hmac.Equal([]byte(got), []byte(want)) {
				return true
			}
		}
	}
	return false
}
// ANCHOR_END: verify

// Self-check against docs/snippets/vector.json (run: go run docs/snippets/verify.go).
func main() {
	raw, err := os.ReadFile("docs/snippets/vector.json")
	if err != nil {
		panic(err)
	}
	var v struct {
		Secret, Timestamp, Signature, Body string
		Now                                int64
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		panic(err)
	}
	now, body, w := time.Unix(v.Now, 0), []byte(v.Body), 5*time.Minute
	ok := Verify([]string{v.Secret}, v.Timestamp, v.Signature, body, now, w)
	tampered := Verify([]string{v.Secret}, v.Timestamp, v.Signature, append(body, ' '), now, w)
	stale := Verify([]string{v.Secret}, v.Timestamp, v.Signature, body, now.Add(time.Hour), w)
	rotated := Verify([]string{"old", v.Secret}, v.Timestamp, "v1=00,"+v.Signature, body, now, w)
	if !ok || tampered || stale || !rotated {
		fmt.Println("go: FAIL", ok, tampered, stale, rotated)
		os.Exit(1)
	}
	fmt.Println("go: ok")
}
