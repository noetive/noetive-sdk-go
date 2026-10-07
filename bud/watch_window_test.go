package bud

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestTheWaitWindowIsBounded: the server holds a quiet interval for at most
// MaxWaitSeconds, so a longer window would only wait on keepalives, and a zero or
// negative one means the default rather than a Wait that never waits.
func TestTheWaitWindowIsBounded(t *testing.T) {
	t.Parallel()

	max := MaxWaitSeconds * time.Second
	for seconds, want := range map[int]time.Duration{
		-1:                 max,
		0:                  max,
		1:                  time.Second,
		MaxWaitSeconds - 1: max - time.Second,
		MaxWaitSeconds:     max,
		MaxWaitSeconds + 1: max,
	} {
		if got := waitWindow(seconds); got != want {
			t.Errorf("waitWindow(%d) = %v, want %v", seconds, got, want)
		}
	}
}

// TestAStreamThatNeverOpensIsBounded: headers alone do not open a stream. A
// server or proxy that answers 200 and then holds the body would otherwise
// leave the handshake waiting on the caller's context alone.
func TestAStreamThatNeverOpensIsBounded(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	defer srv.Close()

	c, err := New("t", WithBaseURL(srv.URL), WithRetry(NoRetry{}))
	if err != nil {
		t.Fatal(err)
	}
	c.openTimeout = 200 * time.Millisecond

	start := time.Now()
	if _, err := c.Wait(context.Background(), WaitInput{TimeoutSeconds: 1}); err == nil {
		t.Fatal("a stream that never opened was reported as an answer")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("the handshake waited %v for an opening frame", elapsed)
	}
}
