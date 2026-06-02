package semantik

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

// sseHandler returns an HTTP handler that flushes the given SSE body
// incrementally.
func sseHandler(t *testing.T, frames []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("response writer is not a Flusher")
		}
		for _, f := range frames {
			if _, err := io.WriteString(w, f); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

func TestSubscribe_HappyPath(t *testing.T) {
	_, c := newTestServer(t, sseHandler(t, []string{
		"event: subscribed\ndata: {\"subscription_id\":\"sub_xyz\"}\n\n",
		"event: match\ndata: {\"message_id\":\"m1\",\"score\":0.9}\n\n",
		"event: match\ndata: {\"message_id\":\"m2\",\"score\":0.7}\n\n",
	}))
	sub, err := c.Subscribe(t.Context(), SubscribeRequest{
		Query: "q", Namespace: "n", Model: "m", Dimensions: 384,
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Close()
	if sub.ID() != "sub_xyz" {
		t.Errorf("ID = %q, want sub_xyz", sub.ID())
	}
	ev, err := sub.Next(t.Context())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if ev.MessageID != "m1" || ev.Score != 0.9 {
		t.Errorf("unexpected event: %+v", ev)
	}
	ev2, err := sub.Next(t.Context())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if ev2.MessageID != "m2" {
		t.Errorf("unexpected event: %+v", ev2)
	}
	_, err = sub.Next(t.Context())
	if !errors.Is(err, io.EOF) {
		t.Errorf("want EOF after stream ends, got %v", err)
	}
}

func TestSubscribe_EventsIterator(t *testing.T) {
	_, c := newTestServer(t, sseHandler(t, []string{
		"event: subscribed\ndata: {\"subscription_id\":\"s\"}\n\n",
		"event: match\ndata: {\"message_id\":\"m1\",\"score\":0.5}\n\n",
		"event: match\ndata: {\"message_id\":\"m2\",\"score\":0.5}\n\n",
		"event: match\ndata: {\"message_id\":\"m3\",\"score\":0.5}\n\n",
	}))
	sub, err := c.Subscribe(t.Context(), SubscribeRequest{Query: "q", Namespace: "n", Model: "m", Dimensions: 3})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Close()

	var gotIDs []string
	for ev, err := range sub.Events(t.Context()) {
		if err != nil {
			if !errors.Is(err, io.EOF) {
				t.Errorf("iterator error: %v", err)
			}
			break
		}
		gotIDs = append(gotIDs, ev.MessageID)
	}
	if len(gotIDs) != 3 {
		t.Errorf("got %d events, want 3: %v", len(gotIDs), gotIDs)
	}
}

func TestSubscribe_CloseIdempotent(t *testing.T) {
	_, c := newTestServer(t, sseHandler(t, []string{
		"event: subscribed\ndata: {\"subscription_id\":\"s\"}\n\n",
	}))
	sub, err := c.Subscribe(t.Context(), SubscribeRequest{Query: "q", Namespace: "n", Model: "m", Dimensions: 3})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if err := sub.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := sub.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestSubscribe_ContextCancel(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		_, _ = io.WriteString(w, "event: subscribed\ndata: {\"subscription_id\":\"s\"}\n\n")
		fl.Flush()
		// Block until the client disconnects.
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(t.Context())
	sub, err := c.Subscribe(ctx, SubscribeRequest{Query: "q", Namespace: "n", Model: "m", Dimensions: 3})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Close()

	errCh := make(chan error, 1)
	go func() {
		_, err := sub.Next(ctx)
		errCh <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-errCh:
		// On cancel, the stream read unblocks with a transport error
		// (the underlying conn is closed). Accept either ctx.Err or
		// any non-nil error — what matters is that Next unblocks.
		if err == nil {
			t.Fatal("Next should have returned an error on cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Next did not unblock after cancel")
	}
}

func TestSubscribe_ServerRejectsWith401(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeError(t, w, http.StatusUnauthorized, CodeUnauthorized, "bad key", 0)
	})
	_, err := c.Subscribe(t.Context(), SubscribeRequest{Query: "q", Namespace: "n", Model: "m", Dimensions: 3})
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("want ErrUnauthorized, got %v", err)
	}
}

func TestSubscribe_RejectsWrongContentType(t *testing.T) {
	// Server returns 200 + valid-looking SSE bytes but the wrong
	// Content-Type. The SDK must refuse to consume this.
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: subscribed\ndata: {\"subscription_id\":\"s\"}\n\n")
	})
	_, err := c.Subscribe(t.Context(), SubscribeRequest{
		Query: "q", Namespace: "n", Model: "m", Dimensions: 3,
	})
	if !errors.Is(err, ErrMalformedSSE) {
		t.Errorf("want ErrMalformedSSE for wrong content-type, got %v", err)
	}
}

func TestSubscribe_AcceptsContentTypeWithCharset(t *testing.T) {
	// "text/event-stream; charset=utf-8" is valid per RFC 9110.
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		_, _ = io.WriteString(w, "event: subscribed\ndata: {\"subscription_id\":\"s\"}\n\n")
		fl.Flush()
	})
	sub, err := c.Subscribe(t.Context(), SubscribeRequest{
		Query: "q", Namespace: "n", Model: "m", Dimensions: 3,
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Close()
	if sub.ID() != "s" {
		t.Errorf("ID = %q, want s", sub.ID())
	}
}

func TestSubscribe_MissingSubscribedFrame(t *testing.T) {
	_, c := newTestServer(t, sseHandler(t, []string{
		"event: match\ndata: {\"message_id\":\"m1\",\"score\":0.5}\n\n",
	}))
	_, err := c.Subscribe(t.Context(), SubscribeRequest{Query: "q", Namespace: "n", Model: "m", Dimensions: 3})
	if !errors.Is(err, ErrMalformedSSE) {
		t.Errorf("want ErrMalformedSSE, got %v", err)
	}
}

// The SDK applies no targeting defaults: an unset Namespace, Model, or
// Dimensions is a fail-fast preflight error. Defaulting Namespace to a
// shared value would let a forgotten field subscribe to a namespace the
// caller never intended.
func TestSubscribe_RequiresNamespace(t *testing.T) {
	c, _ := New(testKey)
	_, err := c.Subscribe(t.Context(), SubscribeRequest{
		Query: "q", Model: "Qwen3-Embedding-4B", Dimensions: 1024,
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("want ErrInvalidRequest for empty Namespace, got %v", err)
	}
}

func TestSubscribe_RequiresModel(t *testing.T) {
	c, _ := New(testKey)
	_, err := c.Subscribe(t.Context(), SubscribeRequest{
		Query: "q", Namespace: "global", Dimensions: 1024,
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("want ErrInvalidRequest for empty Model, got %v", err)
	}
}

func TestSubscribe_RequiresDimensions(t *testing.T) {
	c, _ := New(testKey)
	_, err := c.Subscribe(t.Context(), SubscribeRequest{
		Query: "q", Namespace: "global", Model: "Qwen3-Embedding-4B",
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("want ErrInvalidRequest for zero Dimensions, got %v", err)
	}
}

// A fully-specified request reaches the wire with its targeting fields
// intact — the SDK neither rewrites nor injects them.
func TestSubscribe_FullySpecifiedReachesWireUnmodified(t *testing.T) {
	var body SubscribeRequest
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		readJSON(t, r, &body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		_, _ = io.WriteString(w, "event: subscribed\ndata: {\"subscription_id\":\"s\"}\n\n")
		fl.Flush()
	})
	sub, err := c.Subscribe(t.Context(), SubscribeRequest{
		Query: "q", Namespace: "global",
		Model: "Qwen3-Embedding-4B", Dimensions: 1024,
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Close()
	if body.Namespace != "global" {
		t.Errorf("Namespace = %q, want %q", body.Namespace, "global")
	}
	if body.Model != "Qwen3-Embedding-4B" {
		t.Errorf("Model = %q, want %q", body.Model, "Qwen3-Embedding-4B")
	}
	if body.Dimensions != 1024 {
		t.Errorf("Dimensions = %d, want %d", body.Dimensions, 1024)
	}
}

func TestSubscribe_PreflightRejectsEmptyQuery(t *testing.T) {
	c, _ := New(testKey)
	_, err := c.Subscribe(t.Context(), SubscribeRequest{Namespace: "n", Model: "m", Dimensions: 3})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("want ErrInvalidRequest, got %v", err)
	}
}
