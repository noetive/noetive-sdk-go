package semantik

import (
	"strings"
	"testing"

	"go.noetive.io/noetive-sdk-go/internal/sse"
)

// TestZeroAllocHot guards the hot encode and parse paths against
// unintended allocation growth. Thresholds are set slightly above the
// observed baseline on go1.25/linux-amd64 so routine changes don't
// trip it; genuine regressions (new alloc in the encoder, retained
// pool buffers, accidental conversions) will.
//
// Run with:  go test -run=TestZeroAllocHot -count=1 .
func TestZeroAllocHot(t *testing.T) {
	t.Run("SearchEncode", func(t *testing.T) {
		req := SearchRequest{
			Query:      `MATCH DISTANCE("x") WITHIN 0.4 LIMIT 10`,
			Namespace:  "n",
			Model:      "text-embedding-3-small",
			Dimensions: 384,
			Limit:      10,
		}
		assertAllocsAtMost(t, 6, func() {
			b, release, err := encodeJSON(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = b
			release()
		})
	})

	t.Run("PublishVectorEncode", func(t *testing.T) {
		vec := make([]float32, 384)
		req := PublishRequest{
			Namespace:  "n",
			Model:      "m",
			Dimensions: 384,
			Items:      []PublishItem{{Vector: vec}},
		}
		assertAllocsAtMost(t, 8, func() {
			b, release, err := encodeJSON(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = b
			release()
		})
	})

	t.Run("SSEFrameParse", func(t *testing.T) {
		// Model the real hot path: one Scanner per subscription, many
		// frames over its lifetime. Reset reader per iteration so we
		// measure Scan + Frame allocations only.
		input := "event: match\ndata: {\"message_id\":\"m1\",\"score\":0.9}\n\n"
		reader := strings.NewReader(input)
		s := sse.NewScanner(reader)
		assertAllocsAtMost(t, 3, func() {
			reader.Reset(input)
			if !s.Scan() {
				t.Fatalf("scan: %v", s.Err())
			}
			_ = s.Frame()
		})
	})
}

// assertAllocsAtMost fails the test if the average number of
// allocations per fn call exceeds want. 100 runs give a reasonable
// average; ceiling allows a small buffer for GC jitter.
func assertAllocsAtMost(t *testing.T, want float64, fn func()) {
	t.Helper()
	got := testingAllocsPerRun(100, fn)
	if got > want {
		t.Errorf("allocations: got %.1f/op, want <= %.1f/op", got, want)
	}
	t.Logf("allocations: %.1f/op (ceiling %.1f)", got, want)
}

// testingAllocsPerRun wraps testing.AllocsPerRun to make the intent
// explicit at call sites.
func testingAllocsPerRun(runs int, fn func()) float64 {
	return testingAllocsPerRunImpl(runs, fn)
}
