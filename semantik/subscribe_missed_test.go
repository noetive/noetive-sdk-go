package semantik

import (
	"errors"
	"testing"
)

// missedStream is a stream that reports four missed matches between two
// delivered ones.
var missedStream = []string{
	"event: subscribed\ndata: {\"subscription_id\":\"s\"}\n\n",
	"event: match\ndata: {\"message_id\":\"m1\",\"score\":0.9}\n\n",
	"event: missed\ndata: {\"count\":4}\n\n",
	"event: match\ndata: {\"message_id\":\"m2\",\"score\":0.8}\n\n",
}

// TestSubscribe_NextReportsMissedMatchesAndContinues verifies that a "missed"
// frame reaches the caller as a MissedMatchesError carrying the count, and that
// the stream keeps delivering afterwards.
//
// Skipping the frame, as unknown events are skipped, would hide the gap and
// leave a caller believing it saw every match. Treating it as terminal would
// throw away a healthy stream for a gap the caller may be able to tolerate.
func TestSubscribe_NextReportsMissedMatchesAndContinues(t *testing.T) {
	sub := subscribeTo(t, missedStream)

	if ev, err := sub.Next(t.Context()); err != nil || ev.MessageID != "m1" {
		t.Fatalf("first Next = %+v, %v; want m1", ev, err)
	}
	_, err := sub.Next(t.Context())
	var missed *MissedMatchesError
	if !errors.As(err, &missed) || missed.Count != 4 {
		t.Fatalf("second Next err = %v, want MissedMatchesError with Count 4", err)
	}
	if ev, err := sub.Next(t.Context()); err != nil || ev.MessageID != "m2" {
		t.Fatalf("Next after missed = %+v, %v; want m2: the stream must continue", ev, err)
	}
}

// TestSubscribe_EventsEndsAtMissedMatchesAndCanResume verifies that the
// iterator ends when the stream reports a gap, and that ranging over Events
// again picks the stream up where it left off.
//
// A loop written as `for ev := range sub.Events(ctx)` never sees the error
// value. Carrying on past a gap would hand that loop a zero MatchEvent as if it
// were a match and hide the gap entirely; ending the iteration is what keeps
// that form honest.
func TestSubscribe_EventsEndsAtMissedMatchesAndCanResume(t *testing.T) {
	sub := subscribeTo(t, missedStream)

	var first []string
	var stoppedOn error
	for ev, err := range sub.Events(t.Context()) {
		if err != nil {
			stoppedOn = err
			break
		}
		first = append(first, ev.MessageID)
	}
	var missed *MissedMatchesError
	if len(first) != 1 || !errors.As(stoppedOn, &missed) || missed.Count != 4 {
		t.Fatalf("first range got %v, stopped on %v; want [m1] then MissedMatchesError 4", first, stoppedOn)
	}

	var second []string
	for ev, err := range sub.Events(t.Context()) {
		if err != nil {
			break
		}
		second = append(second, ev.MessageID)
	}
	if len(second) != 1 || second[0] != "m2" {
		t.Errorf("second range got %v, want [m2]: the stream must still be readable", second)
	}
}

// TestSubscribe_NextReportsEachMissedFrame verifies that consecutive "missed"
// frames, including one straight after the handshake, each reach the caller
// with their own count, so a caller summing them gets the true total.
func TestSubscribe_NextReportsEachMissedFrame(t *testing.T) {
	sub := subscribeTo(t, []string{
		"event: subscribed\ndata: {\"subscription_id\":\"s\"}\n\n",
		"event: missed\ndata: {\"count\":2}\n\n",
		"event: missed\ndata: {\"count\":5}\n\n",
		"event: match\ndata: {\"message_id\":\"m1\",\"score\":0.9}\n\n",
	})

	var total uint64
	for range 2 {
		_, err := sub.Next(t.Context())
		var missed *MissedMatchesError
		if !errors.As(err, &missed) {
			t.Fatalf("Next err = %v, want MissedMatchesError", err)
		}
		total += missed.Count
	}
	if total != 7 {
		t.Errorf("summed missed = %d, want 7", total)
	}
	if ev, err := sub.Next(t.Context()); err != nil || ev.MessageID != "m1" {
		t.Errorf("Next after two missed frames = %+v, %v; want m1", ev, err)
	}
}

// TestSubscribe_MalformedMissedFrameEndsStream verifies that a "missed" frame
// the caller cannot act on ends the stream as malformed instead of being passed
// on. A count of zero reports no gap, and an unreadable count hides one; either
// way the caller can no longer trust what the stream says it delivered.
func TestSubscribe_MalformedMissedFrameEndsStream(t *testing.T) {
	for name, data := range map[string]string{
		"zero count":     `{"count":0}`,
		"no count":       `{}`,
		"unreadable":     `{"count":`,
		"negative count": `{"count":-1}`,
	} {
		t.Run(name, func(t *testing.T) {
			sub := subscribeTo(t, []string{
				"event: subscribed\ndata: {\"subscription_id\":\"s\"}\n\n",
				"event: missed\ndata: " + data + "\n\n",
				"event: match\ndata: {\"message_id\":\"m1\",\"score\":0.9}\n\n",
			})

			_, err := sub.Next(t.Context())
			var stream *SubscribeStreamError
			if !errors.As(err, &stream) {
				t.Fatalf("Next err = %v, want SubscribeStreamError", err)
			}
			if _, again := sub.Next(t.Context()); !errors.As(again, &stream) {
				t.Errorf("Next after malformed frame = %v, want the same terminal error", again)
			}
		})
	}
}

// subscribeTo opens a subscription against a server that sends frames, and
// closes it when the test ends.
func subscribeTo(t *testing.T, frames []string) *Subscription {
	t.Helper()
	_, c := newTestServer(t, sseHandler(t, frames))
	sub, err := c.Subscribe(t.Context(), SubscribeRequest{Query: "q", Namespace: "n", Model: "m", Dimensions: 3})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	return sub
}
