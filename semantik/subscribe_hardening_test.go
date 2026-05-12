package semantik

import (
	"errors"
	"io"
	"net/http"
	"testing"
)

// TestSubscribe_RejectsEmptySubscriptionID asserts that a server which
// delivers a "subscribed" frame with an empty subscription_id is
// treated as a malformed SSE stream rather than silently yielding
// sub.ID() == "". A server that cannot provide a subscription ID has
// either a bug or a compromised intermediary; propagating "" would
// shift the detection burden onto every caller.
func TestSubscribe_RejectsEmptySubscriptionID(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		_, _ = io.WriteString(w, "event: subscribed\ndata: {\"subscription_id\":\"\"}\n\n")
		fl.Flush()
	})
	sub, err := c.Subscribe(t.Context(), SubscribeRequest{
		Query: "q", Namespace: "n", Model: "m", Dimensions: 3,
	})
	if err == nil {
		_ = sub.Close()
		t.Fatalf("Subscribe accepted empty subscription_id; sub.ID() = %q", sub.ID())
	}
	if !errors.Is(err, ErrMalformedSSE) {
		t.Errorf("want ErrMalformedSSE for empty subscription_id, got %v", err)
	}
}

// TestSubscribe_RejectsMissingSubscriptionIDField covers the adjacent
// case where the server emits a subscribed frame whose JSON omits the
// field entirely. Must also be rejected.
func TestSubscribe_RejectsMissingSubscriptionIDField(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		_, _ = io.WriteString(w, "event: subscribed\ndata: {}\n\n")
		fl.Flush()
	})
	sub, err := c.Subscribe(t.Context(), SubscribeRequest{
		Query: "q", Namespace: "n", Model: "m", Dimensions: 3,
	})
	if err == nil {
		_ = sub.Close()
		t.Fatalf("Subscribe accepted missing subscription_id")
	}
	if !errors.Is(err, ErrMalformedSSE) {
		t.Errorf("want ErrMalformedSSE, got %v", err)
	}
}
