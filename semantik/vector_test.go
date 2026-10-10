package semantik

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	gojson "github.com/goccy/go-json"
)

// vectorOf builds a deterministic n-dim vector with long mantissas so its
// JSON form approaches the worst-case ~13 bytes per component.
func vectorOf(n int) []float32 {
	v := make([]float32, n)
	for i := range v {
		v[i] = -0.012345678 - float32(i)*1e-6
	}
	return v
}

// Why: callers who do not ask for vectors must send a body byte-identical
// to what older SDKs sent; callers who do ask must put include_vector on
// the wire. Asserts on the raw bytes the server receives.
func TestSearch_IncludeVectorOnWireOnlyWhenSet(t *testing.T) {
	for _, on := range []bool{false, true} {
		var raw string
		_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			raw = string(b)
			writeJSON(t, w, http.StatusOK, SearchResponse{})
		})
		_, err := c.Search(t.Context(), SearchRequest{
			Query: "q", Namespace: "n", Model: "m", Dimensions: 4, IncludeVector: on,
		})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if got := strings.Contains(raw, `"include_vector":true`); got != on {
			t.Errorf("IncludeVector=%v: body %s", on, raw)
		}
		if !on && strings.Contains(raw, "include_vector") {
			t.Errorf("default body must not mention include_vector: %s", raw)
		}
	}
}

// Why: same wire guarantee for the subscribe request body.
func TestSubscribe_IncludeVectorOnWireOnlyWhenSet(t *testing.T) {
	for _, on := range []bool{false, true} {
		var raw string
		_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			raw = string(b)
			sseHandler(t, []string{"event: subscribed\ndata: {\"subscription_id\":\"s\"}\n\n"})(w, r)
		})
		sub, err := c.Subscribe(t.Context(), SubscribeRequest{
			Query: "q", Namespace: "n", Model: "m", Dimensions: 4, IncludeVector: on,
		})
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
		sub.Close()
		if got := strings.Contains(raw, `"include_vector":true`); got != on {
			t.Errorf("IncludeVector=%v: body %s", on, raw)
		}
		if !on && strings.Contains(raw, "include_vector") {
			t.Errorf("default body must not mention include_vector: %s", raw)
		}
	}
}

// Why: a result with a vector must expose it, and a result without one
// must leave Vector nil so callers can tell "not requested" from "empty".
// Also covers the truncated flag, which must default to false.
func TestSearch_DecodesVectorAndTruncated(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"results":[{"message_id":"a","score":0.9,"vector":[0.5,-0.25]},{"message_id":"b","score":0.8}],"truncated":true}`)
	})
	res, err := c.Search(t.Context(), SearchRequest{Query: "q", Namespace: "n", Model: "m", Dimensions: 2, IncludeVector: true})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Results) != 2 {
		t.Fatalf("results = %d", len(res.Results))
	}
	if v := res.Results[0].Vector; len(v) != 2 || v[0] != 0.5 || v[1] != -0.25 {
		t.Errorf("vector = %v", v)
	}
	if res.Results[1].Vector != nil {
		t.Errorf("absent vector must decode to nil, got %v", res.Results[1].Vector)
	}
	if !res.Truncated {
		t.Error("Truncated = false, want true")
	}
}

// Why: Truncated must be false when the server omits it, so a clamped
// (not truncated) page is never reported as cut short.
func TestSearch_TruncatedFalseWhenAbsent(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"results":[]}`)
	})
	res, err := c.Search(t.Context(), SearchRequest{Query: "q", Namespace: "n", Model: "m", Dimensions: 2})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Truncated {
		t.Error("Truncated = true, want false")
	}
}

// Why: the server's largest legal search page (50 results of 4096-dim
// vectors, a few MB) must clear the SDK's response size cap.
func TestSearch_DecodesMaxVectorPage(t *testing.T) {
	const n, dims = 50, 4096
	items := make([]ResultItem, n)
	for i := range items {
		items[i] = ResultItem{MessageID: fmt.Sprintf("m%d", i), Score: 0.5, Vector: vectorOf(dims)}
	}
	body, err := gojson.Marshal(SearchResponse{Results: items})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= 1<<20 {
		t.Fatalf("test page only %d bytes; does not exceed the old 1 MiB cap", len(body))
	}
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	})
	res, err := c.Search(t.Context(), SearchRequest{Query: "q", Namespace: "n", Model: "m", Dimensions: dims, IncludeVector: true})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Results) != n || len(res.Results[n-1].Vector) != dims {
		t.Errorf("decoded %d results, last vector len %d", len(res.Results), len(res.Results[n-1].Vector))
	}
}

// Why: seq and vector on a match frame must reach the caller; a frame
// without them must leave Vector nil.
func TestSubscribe_MatchDecodesSeqAndVector(t *testing.T) {
	_, c := newTestServer(t, sseHandler(t, []string{
		"event: subscribed\ndata: {\"subscription_id\":\"s\"}\n\n",
		"event: match\ndata: {\"message_id\":\"m1\",\"score\":0.9,\"seq\":42,\"vector\":[1,2,3]}\n\n",
		"event: match\ndata: {\"message_id\":\"m2\",\"score\":0.8}\n\n",
	}))
	sub, err := c.Subscribe(t.Context(), SubscribeRequest{Query: "q", Namespace: "n", Model: "m", Dimensions: 3, IncludeVector: true})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Close()
	ev, err := sub.Next(t.Context())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if ev.Seq != 42 || len(ev.Vector) != 3 || ev.Vector[2] != 3 {
		t.Errorf("event = %+v", ev)
	}
	ev2, err := sub.Next(t.Context())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if ev2.Vector != nil || ev2.Seq != 0 {
		t.Errorf("absent fields must be zero, got %+v", ev2)
	}
}

// Why: the largest legal match frame must fit inside the SSE per-frame
// cap, otherwise include_vector subscriptions on the widest models would
// die mid-stream. A published vector may carry any finite component, and
// magnitudes near 1e20 encode as 22-digit decimals, so the frame here is
// the worst case (about 94 KB), not a typical embedding (about 55 KB).
func TestSubscribe_Decodes4096DimMatchFrame(t *testing.T) {
	const dims = 4096
	worst := make([]float32, dims)
	for i := range worst {
		worst[i] = -1.2345679e20
	}
	data, err := gojson.Marshal(MatchEvent{MessageID: "m1", Score: 0.9, Seq: 7, Vector: worst})
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 90_000 {
		t.Fatalf("fixture is %d bytes, want the worst-case encoding above 90000", len(data))
	}
	_, c := newTestServer(t, sseHandler(t, []string{
		"event: subscribed\ndata: {\"subscription_id\":\"s\"}\n\n",
		"event: match\ndata: " + string(data) + "\n\n",
	}))
	sub, err := c.Subscribe(t.Context(), SubscribeRequest{Query: "q", Namespace: "n", Model: "m", Dimensions: dims, IncludeVector: true})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Close()
	ev, err := sub.Next(t.Context())
	if err != nil {
		t.Fatalf("Next on %d-byte frame: %v", len(data), err)
	}
	if len(ev.Vector) != dims || ev.Seq != 7 {
		t.Errorf("vector len %d seq %d", len(ev.Vector), ev.Seq)
	}
}
