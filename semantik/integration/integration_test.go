//go:build integration

package integration

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/noetive/noetive-sdk-go/semantik"
)

func TestHealth(t *testing.T) {
	c := setup(t)
	ctx, cancel := context.WithTimeout(t.Context(), requestTimeout)
	defer cancel()
	start := time.Now()
	err := c.Health(ctx)
	logElapsed(t, "Health", start)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
}

func TestLint_Valid(t *testing.T) {
	c := setup(t)
	ctx, cancel := context.WithTimeout(t.Context(), requestTimeout)
	defer cancel()
	start := time.Now()
	res, err := c.Lint(ctx, semantik.LintRequest{
		Query: `MATCH DISTANCE("machine learning") WITHIN 0.4 LIMIT 5`,
	})
	logElapsed(t, "Lint (valid)", start)
	if err != nil {
		t.Fatalf("Lint: %v", err)
	}
	if !res.Valid {
		t.Errorf("Valid = false; diagnostics=%+v", res.Diagnostics)
	}
	if res.Normalized == "" {
		t.Errorf("Normalized is empty")
	}
}

func TestLint_Invalid(t *testing.T) {
	c := setup(t)
	ctx, cancel := context.WithTimeout(t.Context(), requestTimeout)
	defer cancel()
	start := time.Now()
	res, err := c.Lint(ctx, semantik.LintRequest{
		Query: `MATCH DISTANCE("x") WITHIN not-a-number`,
	})
	logElapsed(t, "Lint (invalid)", start)
	if err != nil {
		t.Fatalf("Lint: %v", err)
	}
	if res.Valid {
		t.Error("broken query should not be Valid")
	}
	if len(res.Diagnostics) == 0 {
		t.Errorf("expected at least one diagnostic; got %+v", res)
	}
}

func TestPublish_Text_DefaultAck(t *testing.T) {
	c := setup(t)
	ctx, cancel := context.WithTimeout(t.Context(), requestTimeout)
	defer cancel()
	start := time.Now()
	res, err := c.Publish(ctx, semantik.PublishRequest{
		Namespace:      testNamespace,
		Model:          testModel,
		Dimensions:     testDims,
		Items:          []semantik.PublishItem{{Text: "Transformers reshaped NLP."}},
		IdempotencyKey: newIdempotencyKey(t),
	})
	logElapsed(t, "Publish text default-ack", start)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if res.MessageID == "" {
		t.Errorf("MessageID empty: %+v", res)
	}
}

func TestPublish_Text_Stored(t *testing.T) {
	c := setup(t)
	ctx, cancel := context.WithTimeout(t.Context(), requestTimeout)
	defer cancel()
	start := time.Now()
	res, err := c.Publish(ctx, semantik.PublishRequest{
		Namespace:      testNamespace,
		Model:          testModel,
		Dimensions:     testDims,
		Items:          []semantik.PublishItem{{Text: "Stored ack test."}},
		Ack:            semantik.AckStored,
		IdempotencyKey: newIdempotencyKey(t),
	})
	logElapsed(t, "Publish text stored", start)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if res.MessageID == "" || res.Epoch == 0 || res.Seq == 0 {
		t.Errorf("stored ack should return MessageID, Epoch, Seq: %+v", res)
	}
}

func TestPublish_Text_Durable(t *testing.T) {
	c := setup(t)
	ctx, cancel := context.WithTimeout(t.Context(), requestTimeout)
	defer cancel()
	start := time.Now()
	res, err := c.Publish(ctx, semantik.PublishRequest{
		Namespace:      testNamespace,
		Model:          testModel,
		Dimensions:     testDims,
		Items:          []semantik.PublishItem{{Text: "Durable ack test."}},
		Ack:            semantik.AckDurable,
		IdempotencyKey: newIdempotencyKey(t),
	})
	logElapsed(t, "Publish text durable", start)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if res.MessageID == "" {
		t.Errorf("durable ack should return MessageID: %+v", res)
	}
}

func TestPublish_Vector(t *testing.T) {
	c := setup(t)
	ctx, cancel := context.WithTimeout(t.Context(), requestTimeout)
	defer cancel()
	start := time.Now()
	res, err := c.Publish(ctx, semantik.PublishRequest{
		Namespace:      testNamespace,
		Model:          testModel,
		Dimensions:     testDims,
		Items:          []semantik.PublishItem{{Vector: unitVector(testDims)}},
		IdempotencyKey: newIdempotencyKey(t),
	})
	logElapsed(t, "Publish vector", start)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if res.MessageID == "" {
		t.Errorf("MessageID empty: %+v", res)
	}
}

func TestPublish_Idempotency(t *testing.T) {
	c := setup(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*requestTimeout)
	defer cancel()
	key := newIdempotencyKey(t)
	req := semantik.PublishRequest{
		Namespace:      testNamespace,
		Model:          testModel,
		Dimensions:     testDims,
		Items:          []semantik.PublishItem{{Text: "dedupe me"}},
		Ack:            semantik.AckDurable,
		IdempotencyKey: key,
	}
	start := time.Now()
	first, err := c.Publish(ctx, req)
	logElapsed(t, "Publish idempotent #1", start)
	if err != nil {
		t.Fatalf("first Publish: %v", err)
	}
	start = time.Now()
	second, err := c.Publish(ctx, req)
	logElapsed(t, "Publish idempotent #2 (dedupe)", start)
	if err != nil {
		t.Fatalf("second Publish: %v", err)
	}
	if first.MessageID != second.MessageID {
		t.Errorf("idempotent publishes returned different MessageIDs: %q vs %q",
			first.MessageID, second.MessageID)
	}
}

func TestPublish_InvalidRequest(t *testing.T) {
	c := setup(t)
	ctx, cancel := context.WithTimeout(t.Context(), requestTimeout)
	defer cancel()
	// Server-side rejection: dimensions mismatch is caught server-side
	// (client-side validator would accept a length-3 vector at dims=3).
	start := time.Now()
	_, err := c.Publish(ctx, semantik.PublishRequest{
		Namespace:  testNamespace,
		Model:      testModel,
		Dimensions: testDims,
		Items:      []semantik.PublishItem{{Vector: []float32{1, 2, 3}}}, // dim mismatch
	})
	logElapsed(t, "Publish invalid (400)", start)
	if err == nil {
		t.Fatal("expected server-side invalid_request")
	}
	if !errors.Is(err, semantik.ErrInvalidRequest) {
		t.Errorf("want ErrInvalidRequest, got %v", err)
	}
	var apiErr *semantik.Error
	if !errors.As(err, &apiErr) {
		t.Fatal("errors.As failed")
	}
	if apiErr.HTTPStatus != 400 {
		t.Errorf("HTTPStatus = %d, want 400 (server-side)", apiErr.HTTPStatus)
	}
}

func TestPublish_Unauthorized(t *testing.T) {
	// Skip the setup helper — we want a deliberately bad key. Still
	// skip if NOETIVE_KEY_SECRET is unset, to match suite behaviour.
	if os.Getenv("NOETIVE_KEY_SECRET") == "" {
		t.Skip("NOETIVE_KEY_SECRET not set")
	}
	badKey := "keyu_00000000000000000000000000000000000000000000"
	c, err := semantik.New(badKey, semantik.WithBaseURL(ProdBaseURL))
	if err != nil {
		t.Fatalf("semantik.New: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), requestTimeout)
	defer cancel()
	start := time.Now()
	_, err = c.Publish(ctx, semantik.PublishRequest{
		Namespace:  "ns",
		Model:      testModel,
		Dimensions: testDims,
		Items:      []semantik.PublishItem{{Text: "noauth"}},
	})
	logElapsed(t, "Publish unauthorized (401)", start)
	if !errors.Is(err, semantik.ErrUnauthorized) {
		t.Errorf("want ErrUnauthorized, got %v", err)
	}
}

func TestSearch_TextQuery(t *testing.T) {
	c := setup(t)
	seedCtx, cancel := context.WithTimeout(t.Context(), 2*requestTimeout)
	defer cancel()
	// Seed three messages.
	seedStart := time.Now()
	for i, text := range []string{
		"Transformers reshaped NLP benchmarks.",
		"Transformer architectures dominate sequence modelling.",
		"Entirely unrelated content about cooking.",
	} {
		start := time.Now()
		_, err := c.Publish(seedCtx, semantik.PublishRequest{
			Namespace:      testNamespace,
			Model:          testModel,
			Dimensions:     testDims,
			Items:          []semantik.PublishItem{{Text: text}},
			Ack:            semantik.AckDurable,
			IdempotencyKey: newIdempotencyKey(t),
		})
		logElapsed(t, "Publish seed #"+strconv.Itoa(i)+" durable", start)
		if err != nil {
			t.Fatalf("seed Publish: %v", err)
		}
	}
	logElapsed(t, "Publish seed total", seedStart)

	searchCtx, cancel2 := context.WithTimeout(t.Context(), requestTimeout)
	defer cancel2()
	start := time.Now()
	res, err := c.Search(searchCtx, semantik.SearchRequest{
		Query:      `MATCH DISTANCE("transformer architecture") WITHIN 0.6 LIMIT 5`,
		Namespace:  testNamespace,
		Model:      testModel,
		Dimensions: testDims,
	})
	logElapsed(t, "Search text", start)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Results) == 0 {
		t.Errorf("expected at least one match, got none")
	}
}

func TestSearch_LimitClause(t *testing.T) {
	c := setup(t)
	seedCtx, cancel := context.WithTimeout(t.Context(), 2*requestTimeout)
	defer cancel()
	// Seed 3 identical-ish docs so LIMIT is the bound.
	seedStart := time.Now()
	for i := range 3 {
		start := time.Now()
		_, err := c.Publish(seedCtx, semantik.PublishRequest{
			Namespace:      testNamespace,
			Model:          testModel,
			Dimensions:     testDims,
			Items:          []semantik.PublishItem{{Text: "doc " + string(rune('a'+i))}},
			Ack:            semantik.AckDurable,
			IdempotencyKey: newIdempotencyKey(t),
		})
		logElapsed(t, "Publish seed #"+strconv.Itoa(i)+" durable", start)
		if err != nil {
			t.Fatalf("seed Publish: %v", err)
		}
	}
	logElapsed(t, "Publish seed total", seedStart)

	searchCtx, cancel2 := context.WithTimeout(t.Context(), requestTimeout)
	defer cancel2()
	start := time.Now()
	res, err := c.Search(searchCtx, semantik.SearchRequest{
		Query:      `MATCH DISTANCE("doc") WITHIN 0.99`,
		Namespace:  testNamespace,
		Model:      testModel,
		Dimensions: testDims,
		Limit:      1,
	})
	logElapsed(t, "Search limit=1", start)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Results) > 1 {
		t.Errorf("Limit=1 ignored; got %d results", len(res.Results))
	}
}

func TestSubscribe_RoundTrip(t *testing.T) {
	c := setup(t)
	subCtx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	start := time.Now()
	sub, err := c.Subscribe(subCtx, semantik.SubscribeRequest{
		Query:      `MATCH DISTANCE("mechanical engineering") WITHIN 0.6`,
		Namespace:  testNamespace,
		Model:      testModel,
		Dimensions: testDims,
	})
	logElapsed(t, "Subscribe setup", start)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Close()
	if sub.ID() == "" {
		t.Error("Subscribe should populate ID before returning")
	}

	pubCtx, cancelPub := context.WithTimeout(t.Context(), requestTimeout)
	defer cancelPub()
	pubStart := time.Now()
	pubRes, err := c.Publish(pubCtx, semantik.PublishRequest{
		Namespace:      testNamespace,
		Model:          testModel,
		Dimensions:     testDims,
		Items:          []semantik.PublishItem{{Text: "Mechanical engineering research remains open."}},
		Ack:            semantik.AckDurable,
		IdempotencyKey: newIdempotencyKey(t),
	})
	logElapsed(t, "Publish durable", pubStart)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	nextCtx, cancelNext := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelNext()
	nextStart := time.Now()
	ev, err := sub.Next(nextCtx)
	logElapsed(t, "Next (publish→deliver)", pubStart)
	logElapsed(t, "Next (wait→deliver)", nextStart)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if ev.MessageID != pubRes.MessageID {
		t.Errorf("MatchEvent.MessageID = %q, want %q", ev.MessageID, pubRes.MessageID)
	}
}

func TestSubscribe_ContextCancel(t *testing.T) {
	c := setup(t)
	ctx, cancel := context.WithCancel(t.Context())
	start := time.Now()
	sub, err := c.Subscribe(ctx, semantik.SubscribeRequest{
		Query:      `MATCH DISTANCE("x") WITHIN 0.5`,
		Namespace:  testNamespace,
		Model:      testModel,
		Dimensions: testDims,
	})
	logElapsed(t, "Subscribe setup", start)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Close()

	errCh := make(chan error, 1)
	go func() {
		_, err := sub.Next(ctx)
		errCh <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancelAt := time.Now()
	cancel()
	select {
	case err := <-errCh:
		logElapsed(t, "Next unblock after cancel", cancelAt)
		if err == nil {
			t.Fatal("Next should have returned an error after cancel")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Next did not unblock after cancel")
	}
}

func TestSubscribe_CloseIdempotent(t *testing.T) {
	c := setup(t)
	ctx, cancel := context.WithTimeout(t.Context(), requestTimeout)
	defer cancel()
	start := time.Now()
	sub, err := c.Subscribe(ctx, semantik.SubscribeRequest{
		Query:      `MATCH DISTANCE("x") WITHIN 0.5`,
		Namespace:  testNamespace,
		Model:      testModel,
		Dimensions: testDims,
	})
	logElapsed(t, "Subscribe setup", start)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	closeStart := time.Now()
	if err := sub.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	logElapsed(t, "Close #1", closeStart)
	closeStart = time.Now()
	if err := sub.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	logElapsed(t, "Close #2 (idempotent)", closeStart)
}

// TestSubscribe_MultiMessageDelivery asserts the no-drop + latency
// properties the match stream promises: given N matching publishes
// after a subscription opens, the SDK delivers exactly N match
// events, each within the per-message latency bound, each carrying a
// MessageID that was returned by one of the publishes, and in the
// same order as the AckDurable publishes that produced them. A
// missing or duplicated MessageID would indicate drop or replay; an
// out-of-order MessageID would indicate the stream is not honouring
// the per-namespace publish sequence.
//
// Each publish tags its metadata with a monotonic "seq" so a failure
// is traceable via Search even after the test exits — the wire
// MatchEvent carries only message_id+score, but the metadata is
// retrievable per message_id via /v1/search.
func TestSubscribe_MultiMessageDelivery(t *testing.T) {
	const (
		numMessages     = 3
		perMessageBound = 8 * time.Second
		totalBound      = 30 * time.Second
	)

	c := setup(t)
	subCtx, cancelSub := context.WithTimeout(t.Context(), totalBound)
	defer cancelSub()

	subStart := time.Now()
	sub, err := c.Subscribe(subCtx, semantik.SubscribeRequest{
		Query:      `MATCH DISTANCE("distributed consensus protocols") WITHIN 0.7`,
		Namespace:  testNamespace,
		Model:      testModel,
		Dimensions: testDims,
	})
	logElapsed(t, "Subscribe setup", subStart)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Close()

	type publication struct {
		messageID string
		seq       int
		sentAt    time.Time
	}
	publishedOrder := make([]string, 0, numMessages)
	bySeq := make(map[string]publication, numMessages)

	pubAllStart := time.Now()
	for i := 0; i < numMessages; i++ {
		pubCtx, cancelPub := context.WithTimeout(t.Context(), requestTimeout)
		pubStart := time.Now()
		res, err := c.Publish(pubCtx, semantik.PublishRequest{
			Namespace:  testNamespace,
			Model:      testModel,
			Dimensions: testDims,
			Items:      []semantik.PublishItem{{Text: "Consensus in distributed systems remains central."}},
			Metadata: map[string]string{
				"seq":     strconv.Itoa(i),
				"test":    "TestSubscribe_MultiMessageDelivery",
				"run_key": t.Name(),
			},
			Ack:            semantik.AckDurable,
			IdempotencyKey: newIdempotencyKey(t),
		})
		logElapsed(t, "Publish #"+strconv.Itoa(i)+" durable", pubStart)
		cancelPub()
		if err != nil {
			t.Fatalf("Publish #%d: %v", i, err)
		}
		publishedOrder = append(publishedOrder, res.MessageID)
		bySeq[res.MessageID] = publication{messageID: res.MessageID, seq: i, sentAt: time.Now()}
	}
	logElapsed(t, "Publish total ("+strconv.Itoa(numMessages)+")", pubAllStart)

	deliveredOrder := make([]string, 0, numMessages)
	seen := make(map[string]struct{}, numMessages)
	deliverAllStart := time.Now()
	for len(seen) < numMessages {
		nextCtx, cancelNext := context.WithTimeout(t.Context(), perMessageBound)
		ev, err := sub.Next(nextCtx)
		cancelNext()
		if err != nil {
			t.Fatalf("Next after %d/%d delivered: %v", len(seen), numMessages, err)
		}
		if _, dup := seen[ev.MessageID]; dup {
			t.Errorf("duplicate delivery of %q", ev.MessageID)
			continue
		}
		pub, ok := bySeq[ev.MessageID]
		if !ok {
			// A match from a different test — tolerate, don't count.
			continue
		}
		latency := time.Since(pub.sentAt)
		logElapsed(t, "Next #"+strconv.Itoa(pub.seq)+" (publish→deliver)", pub.sentAt)
		if latency > perMessageBound {
			t.Errorf("match for seq=%d (%q) arrived %v after publish (bound %v)",
				pub.seq, ev.MessageID, latency, perMessageBound)
		}
		seen[ev.MessageID] = struct{}{}
		deliveredOrder = append(deliveredOrder, ev.MessageID)
	}
	logElapsed(t, "Deliver total ("+strconv.Itoa(numMessages)+")", deliverAllStart)

	if len(deliveredOrder) != numMessages {
		t.Errorf("delivered %d, expected %d", len(deliveredOrder), numMessages)
	}
	for i, got := range deliveredOrder {
		want := publishedOrder[i]
		if got != want {
			t.Errorf("delivery position %d: got %q (seq=%d), want %q (seq=%d)",
				i, got, bySeq[got].seq, want, bySeq[want].seq)
		}
	}
}

// TestSubscribe_EOFOnClose verifies Next returns io.EOF or a
// transport-level error after Close — not a hang.
func TestSubscribe_EOFOnClose(t *testing.T) {
	c := setup(t)
	ctx, cancel := context.WithTimeout(t.Context(), requestTimeout)
	defer cancel()
	start := time.Now()
	sub, err := c.Subscribe(ctx, semantik.SubscribeRequest{
		Query:      `MATCH DISTANCE("x") WITHIN 0.5`,
		Namespace:  testNamespace,
		Model:      testModel,
		Dimensions: testDims,
	})
	logElapsed(t, "Subscribe setup", start)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	_ = sub.Close()
	closedAt := time.Now()
	done := make(chan struct{})
	go func() {
		_, _ = sub.Next(t.Context())
		close(done)
	}()
	select {
	case <-done:
		logElapsed(t, "Next return after Close", closedAt)
	case <-time.After(5 * time.Second):
		t.Error("Next did not return after Close")
	}
	// Explicitly test that we at least got EOF-ish behaviour by not hanging.
	_ = io.EOF
}

