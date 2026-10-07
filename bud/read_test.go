package bud_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.noetive.io/noetive-sdk-go/bud"
)

// TestHealthAndCatalogCarryRefusals: a key that reaches no agent here is answered
// 403 forbidden_scope by every operation, the probe and the catalog included. Read
// as anything else — "the body was not an envelope" — it sends somebody to debug a
// proxy instead of the key's reach.
func TestHealthAndCatalogCarryRefusals(t *testing.T) {
	t.Parallel()

	srv := refusingServer(t, http.StatusForbidden,
		`{"error":{"code":"forbidden_scope","message":"this key does not reach an agent on this service"}}`)
	c := mustClient(t, srv.URL)

	health, err := c.Health(t.Context())
	if err != nil {
		t.Fatalf("Health: a refusal arrived as a Go error: %v", err)
	}
	catalog, err := c.DescribeCatalog(t.Context())
	if err != nil {
		t.Fatalf("DescribeCatalog: a refusal arrived as a Go error: %v", err)
	}

	for name, refusal := range map[string]*bud.Error{"Health": health.Error, "DescribeCatalog": catalog.Error} {
		if refusal == nil || refusal.Code != bud.CodeForbiddenScope {
			t.Errorf("%s: Error = %v, want forbidden_scope", name, refusal)
			continue
		}
		if refusal.HTTPStatus != http.StatusForbidden || refusal.RequestID != "request_01refused" {
			t.Errorf("%s: status %d, request %q", name, refusal.HTTPStatus, refusal.RequestID)
		}
	}
}

// TestRetryableFollowsTheServersAdvice: the server says which refusals clear on
// their own. Retrying one that does not burns budget forever; not retrying one
// that does turns a blip into an outage.
func TestRetryableFollowsTheServersAdvice(t *testing.T) {
	t.Parallel()

	for code, want := range map[string]bool{
		bud.CodeUpstreamUnavailable: true, // the request had no effect
		bud.CodePreconditionFailed:  true, // read again and retry
		bud.CodeInternal:            true,

		bud.CodePaused:            false, // an operator's decision; clears only when released
		bud.CodeNotBillable:       false, // waits on billing, not on time
		bud.CodeUnavailable:       false, // this build does not serve it
		bud.CodeUnauthorized:      false,
		bud.CodeForbiddenScope:    false,
		bud.CodeNotFound:          false,
		bud.CodePolicyRefused:     false,
		bud.CodeInvalid:           false,
		bud.CodeMalformedResponse: false,
	} {
		if got := (&bud.Error{Code: code}).Retryable(); got != want {
			t.Errorf("%s: Retryable = %v, want %v", code, got, want)
		}
	}
	// A rate limit is worth waiting out only when the server says how long.
	if !(&bud.Error{Code: bud.CodeRateLimited, RetryAfterMs: 1000}).Retryable() {
		t.Error("a rate limit with a wait is not retryable")
	}
	if (&bud.Error{Code: bud.CodeRateLimited}).Retryable() {
		t.Error("a rate limit with no wait is retryable; the request has to change")
	}
	if (*bud.Error)(nil).Retryable() {
		t.Error("a nil refusal is retryable")
	}
}

// TestTheNewCodesMatchTheirSentinels lets a caller branch on errors.Is for the two
// codes the server added.
func TestTheNewCodesMatchTheirSentinels(t *testing.T) {
	t.Parallel()

	srv := refusingServer(t, http.StatusPaymentRequired,
		`{"error":{"code":"not_billable","message":"this account cannot incur usage"}}`)
	out, err := mustClient(t, srv.URL).DescribeMe(t.Context())
	if err != nil {
		t.Fatalf("DescribeMe: %v", err)
	}
	if !errors.Is(out.Error, bud.ErrNotBillable) || errors.Is(out.Error, bud.ErrUpstreamUnavailable) {
		t.Errorf("Error = %v, want not_billable and only that", out.Error)
	}
	if !errors.Is(&bud.Error{Code: bud.CodeUpstreamUnavailable}, bud.ErrUpstreamUnavailable) {
		t.Error("ErrUpstreamUnavailable does not match its own code")
	}
}

// readServer answers each operation with a canned body, keyed by path.
func readServer(t *testing.T, answers map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		answer, ok := answers[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"no such operation"}}`)
			return
		}
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestTheTypedViewsReadWhatTheServerSends covers the three operations whose
// content has a view of its own, end to end: the request reaches the right
// operation with the fields that select it, and Into turns the answer into the
// view — bytes included — without the caller decoding anything itself.
func TestTheTypedViewsReadWhatTheServerSends(t *testing.T) {
	t.Parallel()

	srv := readServer(t, map[string]string{
		"/v1/mailbox.describe": `{"ref":"ag_01x","kind":"mailbox","content":{"mailbox":"ag_01x",` +
			`"folders":{"inbox":3},"sending":{"status":"provisioning","reason":"domain verifying"}}}`,
		"/v1/correspondent.list": `{"ref":"ag_01x/corr","kind":"corr","content":{"mailbox":"ag_01x",` +
			`"correspondents":[{"addr":"ada@example.com","first_seen":"2026-01-02T03:04:05Z",` +
			`"last_seen":"2026-01-03T03:04:05Z","inbound":2,"last_aligned":true}]}}`,
		"/v1/part.describe": `{"ref":"message_01x/part/2","kind":"part","content":{"message":"message_01x",` +
			`"part":"2","mode":"bytes","type":"text/plain","bytes_base64":"aGVsbG8="}}`,
	})
	c := mustClient(t, srv.URL)

	mailbox, err := c.DescribeMailbox(t.Context(), bud.ByID{ID: "ag_01x"})
	if err != nil {
		t.Fatalf("DescribeMailbox: %v", err)
	}
	var mv bud.MailboxView
	if err := mailbox.Into(&mv); err != nil {
		t.Fatalf("Into MailboxView: %v", err)
	}
	if mv.Folders["inbox"] != 3 || mv.Sending == nil || mv.Sending.Status != bud.SendingProvisioning {
		t.Errorf("MailboxView = %+v", mv)
	}

	corr, err := c.ListCorrespondents(t.Context(), bud.In{In: "ag_01x", Q: "ada"})
	if err != nil {
		t.Fatalf("ListCorrespondents: %v", err)
	}
	var cv bud.Correspondents
	if err := corr.Into(&cv); err != nil {
		t.Fatalf("Into Correspondents: %v", err)
	}
	if len(cv.Correspondents) != 1 || cv.Correspondents[0].Addr != "ada@example.com" ||
		cv.Correspondents[0].FirstSeen.IsZero() || !cv.Correspondents[0].LastAligned {
		t.Errorf("Correspondents = %+v", cv)
	}

	part, err := c.DescribePart(t.Context(), bud.ByID{ID: "message_01x", Part: "2", Mode: bud.PartModeBytes})
	if err != nil {
		t.Fatalf("DescribePart: %v", err)
	}
	var pv bud.PartView
	if err := part.Into(&pv); err != nil {
		t.Fatalf("Into PartView: %v", err)
	}
	if string(pv.Bytes) != "hello" {
		t.Errorf("Bytes = %q; the base64 on the wire is decoded for the caller", pv.Bytes)
	}

	// A view of the wrong kind is refused rather than decoded into zero values
	// that would read as an empty mailbox.
	if err := part.Into(&mv); !errors.Is(err, bud.ErrInvalid) {
		t.Errorf("a part decoded into a MailboxView: %v", err)
	}
}

// TestTheRequestsCarryTheFieldsThatSelectThem pins the wire names the server
// routes on, so a renamed tag is a red test rather than a filter silently dropped.
func TestTheRequestsCarryTheFieldsThatSelectThem(t *testing.T) {
	t.Parallel()

	got := make(chan map[string]any, 3)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["_path"] = r.URL.Path
		got <- body
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	c := mustClient(t, srv.URL)

	if _, err := c.DescribePart(t.Context(), bud.ByID{ID: "message_01x", Part: "2", Mode: bud.PartModeText}); err != nil {
		t.Fatalf("DescribePart: %v", err)
	}
	if _, err := c.DescribeThread(t.Context(), bud.ByID{ID: "thread_01x", MaxMessages: 5}); err != nil {
		t.Fatalf("DescribeThread: %v", err)
	}
	if _, err := c.ListCorrespondents(t.Context(), bud.In{In: "ag_01x", Limit: 10}); err != nil {
		t.Fatalf("ListCorrespondents: %v", err)
	}

	for _, want := range []map[string]any{
		{"_path": "/v1/part.describe", "id": "message_01x", "part": "2", "mode": "text"},
		{"_path": "/v1/thread.describe", "id": "thread_01x", "max_messages": float64(5)},
		{"_path": "/v1/correspondent.list", "in": "ag_01x", "limit": float64(10)},
	} {
		body := <-got
		for k, v := range want {
			if body[k] != v {
				t.Errorf("%s: %s = %v, want %v", want["_path"], k, body[k], v)
			}
		}
	}
}

// TestIntoReturnsTheRefusalOrSaysThereIsNothing: Into on a refusal hands back the
// refusal itself, and on an answer with no structure says so rather than leaving
// the view zeroed.
func TestIntoReturnsTheRefusalOrSaysThereIsNothing(t *testing.T) {
	t.Parallel()

	refused := bud.ReadOutput{Error: &bud.Error{Code: bud.CodeNotFound}}
	if err := refused.Into(&bud.MailboxView{}); !errors.Is(err, bud.ErrNotFound) {
		t.Errorf("Into on a refusal = %v, want the refusal", err)
	}

	textOnly := bud.ReadOutput{Kind: bud.KindHelp, Text: "the grammar"}
	var anything map[string]any
	if err := textOnly.Into(&anything); err == nil {
		t.Error("Into on an answer with no content returned nil")
	}
}

// TestNewFromEnvReadsTheOneNoetiveKey: one key reaches every Noetive product, so
// bud reads the same variable the rest of the SDK does and no product-prefixed one.
func TestNewFromEnvReadsTheOneNoetiveKey(t *testing.T) {
	auth := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth <- r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"contract":"bud/v1"}`)
	}))
	defer srv.Close()

	t.Setenv("NOETIVE_KEY_SECRET", "keya_test")
	t.Setenv("NOETIVE_BUD_BASE_URL", srv.URL)

	c, err := bud.NewFromEnv(bud.WithRetry(bud.NoRetry{}))
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	if _, err := c.Health(t.Context()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if got := <-auth; got != "Bearer keya_test" {
		t.Errorf("Authorization = %q, want the key from NOETIVE_KEY_SECRET", got)
	}

	t.Setenv("NOETIVE_KEY_SECRET", "")
	t.Setenv("NOETIVE_BUD_KEY_SECRET", "keya_old")
	_, err = bud.NewFromEnv()
	if !errors.Is(err, bud.ErrInvalid) || !strings.Contains(err.Error(), "NOETIVE_KEY_SECRET") {
		t.Errorf("err = %v; with the shared key unset, the refusal must name it and nothing else be read", err)
	}
}

// TestASendNeedsARecipientAnywhere: To may be empty when CC or BCC names
// someone, as the server allows. Refusing that here would block a send the
// server accepts.
func TestASendNeedsARecipientAnywhere(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"message":"message_01x","state":"queued"}`)
	}))
	defer srv.Close()
	c := mustClient(t, srv.URL)

	for name, in := range map[string]bud.SendInput{
		"cc only":  {CC: []string{"ada@example.com"}, Text: "hi"},
		"bcc only": {BCC: []string{"ada@example.com"}, Text: "hi"},
		"a reply":  {InReplyTo: "message_01p", Text: "hi"},
	} {
		out, err := c.Send(t.Context(), in)
		if err != nil || out.State != bud.StateQueued {
			t.Errorf("%s: Send = %+v, %v; the server accepts this send", name, out, err)
		}
	}
}
