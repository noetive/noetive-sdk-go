//go:build integration

// Package integration drives the Noetive SDK against the production
// Semantik endpoint. Every publish carries a unique idempotency key so
// the suite is safe to re-run against shared server state.
//
// Required environment:
//
//	NOETIVE_KEY_SECRET  — a valid production API key (keyu_... or keyt_...)
//
// Invocation:
//
//	NOETIVE_KEY_SECRET=... go test -tags=integration -count=1 -v ./integration/...
//
// If NOETIVE_KEY_SECRET is unset, every test calls t.Skip. This lets
// `go test ./...` stay green without network access.
package integration

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/noetive/noetive-sdk-go/semantik"
)

// ProdBaseURL is the production Semantik endpoint. Hardcoded
// intentionally — the integration suite exists to catch drift between
// the SDK and the real server; an override would defeat that.
const ProdBaseURL = "https://semantik.noetive.io"

const (
	// testNamespace is the single shared namespace the suite targets.
	// Only one namespace — "global" — is provisioned on production.
	testNamespace = "global"

	// Model and dimensions provisioned for "global": Qwen3-Embedding-4B
	// at 1024 dimensions.
	testModel = "Qwen3-Embedding-4B"
	testDims  = 1024

	// requestTimeout bounds each individual SDK call so a single slow
	// run cannot stall the whole suite. The default retry policy can
	// spend up to 15 s on fallback backoffs (1 + 2 + 3 + 4 + 5) across
	// five retries, so the budget has to leave room for that on top of
	// real server latency. Subscribe uses its own deadline — see
	// TestSubscribe_*.
	requestTimeout = 45 * time.Second
)

// setup returns an authenticated Client. It calls t.Skip if
// NOETIVE_KEY_SECRET is not set.
func setup(t *testing.T) *semantik.Client {
	t.Helper()
	key := os.Getenv("NOETIVE_KEY_SECRET")
	if key == "" {
		t.Skip("NOETIVE_KEY_SECRET not set; skipping integration test")
	}
	c, err := semantik.New(key, semantik.WithBaseURL(ProdBaseURL))
	if err != nil {
		t.Fatalf("semantik.New: %v", err)
	}
	return c
}

// newIdempotencyKey returns a fresh key suitable for Publish calls.
// Using one per publish lets the suite be re-run safely against the
// same server without duplicating state.
func newIdempotencyKey(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return "sdk-it-" + hex.EncodeToString(b[:])
}

// unitVector returns a vector of dim whose components are
// deterministic floats in [0, 1). Useful for Publish requests that
// need a plausible embedding shape without caring about semantics.
func unitVector(dim int) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = float32(i%100) / 100.0
	}
	return v
}

// logElapsed prints the duration since start under op's label so the
// integration suite doubles as a coarse latency probe. Always called
// after the SDK call returns (success or failure) so a failed run also
// records where time was spent.
func logElapsed(t *testing.T, op string, start time.Time) {
	t.Helper()
	t.Logf("  %-28s %s", op, time.Since(start).Round(time.Millisecond))
}
