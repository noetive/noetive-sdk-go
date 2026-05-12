package semantik

import (
	"errors"
	"io"
	"net/http"
	"testing"
)

// TestSubscribe_RejectsMalformedSubscribedFrame covers the JSON
// decoder branch in Subscribe: a server that emits a "subscribed"
// event whose data is not valid JSON must be rejected as malformed
// rather than silently producing a broken Subscription.
func TestSubscribe_RejectsMalformedSubscribedFrame(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		_, _ = io.WriteString(w, "event: subscribed\ndata: not-json-at-all\n\n")
		fl.Flush()
	})
	_, err := c.Subscribe(t.Context(), SubscribeRequest{
		Query: "q", Namespace: "n", Model: "m", Dimensions: 3,
	})
	if !errors.Is(err, ErrMalformedSSE) {
		t.Errorf("want ErrMalformedSSE for non-JSON subscribed frame, got %v", err)
	}
}

// TestSubscription_NextOnMalformedMatchFrame exercises the match-frame
// JSON-decode error path in Next. The error must wrap ErrMalformedSSE
// so callers can route structured-stream failures through a single
// errors.Is check, and the error must stick so a subsequent Next
// surfaces it too.
func TestSubscription_NextOnMalformedMatchFrame(t *testing.T) {
	_, c := newTestServer(t, sseHandler(t, []string{
		"event: subscribed\ndata: {\"subscription_id\":\"s\"}\n\n",
		"event: match\ndata: {this-is-not-json\n\n",
	}))
	sub, err := c.Subscribe(t.Context(), SubscribeRequest{
		Query: "q", Namespace: "n", Model: "m", Dimensions: 3,
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Close()

	_, err = sub.Next(t.Context())
	if !errors.Is(err, ErrMalformedSSE) {
		t.Fatalf("want ErrMalformedSSE on bad match frame, got %v", err)
	}
	// Sticky: the same error must surface on the next call, not a
	// fresh EOF from the drained body.
	_, err2 := sub.Next(t.Context())
	if !errors.Is(err2, ErrMalformedSSE) {
		t.Errorf("error should stick across Next calls; got %v", err2)
	}
}

// TestStatusCodeFallback_ExhaustiveTable guards every mapping in
// statusCodeFallback. Missing a status-to-code wire-up is a silent
// downgrade into CodeInternalError that a caller with
// errors.Is-based routing will never catch.
func TestStatusCodeFallback_ExhaustiveTable(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{http.StatusBadRequest, CodeInvalidRequest},
		{http.StatusUnauthorized, CodeUnauthorized},
		{http.StatusPaymentRequired, CodeNotBillable},
		{http.StatusForbidden, CodeNamespaceDisabled},
		{http.StatusMethodNotAllowed, CodeMethodNotAllowed},
		{http.StatusRequestEntityTooLarge, CodeRequestTooLarge},
		{http.StatusUnsupportedMediaType, CodeUnsupportedMediaType},
		{http.StatusTooManyRequests, CodeRateLimited},
		{http.StatusServiceUnavailable, CodeUnavailable},
		{http.StatusInternalServerError, CodeInternalError}, // default
		{http.StatusTeapot, CodeInternalError},              // default branch
	}
	for _, c := range cases {
		if got := statusCodeFallback(c.status); got != c.want {
			t.Errorf("statusCodeFallback(%d) = %q, want %q", c.status, got, c.want)
		}
	}
}
