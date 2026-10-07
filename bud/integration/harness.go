//go:build integration

// Package integration drives the bud client against the production Bud
// endpoint.
//
// Required environment:
//
//	NOETIVE_KEY_SECRET  — a production Noetive key (keya_...)
//
// Invocation:
//
//	NOETIVE_KEY_SECRET=... go test -tags=integration -count=1 -v ./bud/integration/...
//
// The suite reads, waits and refuses; it never delivers mail. Sending is
// exercised only through requests the service refuses before anything is
// queued, and the one write it makes, marking a message read or unread, sets
// the message to the state it already has. That write still emits a mail.read
// event, which anyone watching the mailbox sees.
//
// If NOETIVE_KEY_SECRET is unset every test calls t.Skip, so `go test ./...`
// stays green without network access.
package integration

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"go.noetive.io/noetive-sdk-go/bud"
)

// ProdBaseURL is the production Bud endpoint. Hardcoded on purpose: the suite
// exists to catch drift between this client and the real service, and an
// override would defeat that.
const ProdBaseURL = "https://bud.noetive.io"

// requestTimeout bounds each call so one slow request cannot stall the suite.
const requestTimeout = 30 * time.Second

// key returns the key under test, or skips.
func key(t *testing.T) string {
	t.Helper()
	k := strings.TrimSpace(os.Getenv("NOETIVE_KEY_SECRET"))
	if k == "" {
		t.Skip("NOETIVE_KEY_SECRET not set; skipping integration test")
	}
	return k
}

// setup returns a client whose key the service accepts, and who it is.
//
// A key the service refuses fails here, once and by name, rather than as a
// refusal in the middle of every test that follows.
func setup(t *testing.T) (*bud.Client, bud.Me) {
	t.Helper()
	c, err := bud.New(key(t), bud.WithBaseURL(ProdBaseURL))
	if err != nil {
		t.Fatalf("bud.New: %v", err)
	}
	ctx := callContext(t)

	out, err := c.DescribeMe(ctx)
	if err != nil {
		t.Fatalf("DescribeMe: %v", err)
	}
	if out.Error != nil {
		t.Fatalf("the service refused NOETIVE_KEY_SECRET: %v\n\t"+
			"Bud needs a current Noetive key (keya_...); legacy keys are not accepted", out.Error)
	}
	var me bud.Me
	if err := out.Into(&me); err != nil {
		t.Fatalf("Into(Me): %v", err)
	}
	if !strings.HasPrefix(me.Mailbox, bud.PrefixAgent) {
		t.Fatalf("me.Mailbox = %q, want an agent identifier", me.Mailbox)
	}
	return c, me
}

// callContext bounds one call and ends with the test.
func callContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), requestTimeout)
	t.Cleanup(cancel)
	return ctx
}

// refused fails unless out carries a refusal with the given code.
func refused(t *testing.T, op string, err error, refusal *bud.Error, code string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: a refusal arrived as a Go error: %v", op, err)
	}
	if refusal == nil {
		t.Fatalf("%s: want a %s refusal, got an answer", op, code)
	}
	if refusal.Code != code {
		t.Fatalf("%s: refusal %v, want code %s", op, refusal, code)
	}
	if refusal.RequestID == "" {
		t.Errorf("%s: the refusal carries no request id to quote", op)
	}
}

// answered fails unless the call succeeded.
func answered(t *testing.T, op string, err error, refusal *bud.Error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
	if refusal != nil {
		t.Fatalf("%s: refused: %v", op, refusal)
	}
}
