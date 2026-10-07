// Package bud is the official Go client for Noetive Bud, managed mail for
// agents: read a mailbox, wait for what happens in it, and send.
//
// # Quick start
//
//	c, err := bud.NewFromEnv() // or bud.New("keya_...")
//	if err != nil { log.Fatal(err) }
//
//	out, err := c.ListFolder(ctx, bud.In{In: "ag_...", Unread: true})
//	switch {
//	case err != nil:       // the connection failed; nothing to branch on
//	case out.Error != nil: // a refusal; branch on out.Error.Code
//	default:
//	    var inbox bud.Listing
//	    _ = out.Into(&inbox)
//	}
//
// # Authentication
//
// Requests carry a Noetive key as Authorization: Bearer <key>; one key
// reaches every Noetive service. [NewFromEnv] reads NOETIVE_KEY_SECRET
// (required) and NOETIVE_BUD_BASE_URL (optional, defaults to
// https://bud.noetive.io). A relay that passes each caller's own credential
// through uses [Forwarding] and [WithAuthorizationContext] instead.
//
// # Refusals are values
//
// A refused request comes back as the operation's own output with Error set
// and a nil Go error: the code to branch on, a hint that says what would
// unblock it, and on a rate limit when to try again. A non-nil Go error means
// the request never got an answer — a failed connection, an unreadable
// response, an expired context, or a request this package refused before
// sending (an *[Error] with HTTPStatus 0). [Error.Retryable] says whether
// waiting and trying again can succeed. [Client.Watch] is the one exception:
// a stream has no envelope, so its refusals arrive as *[Error].
//
// # Reading what a stranger wrote
//
// Mail carries text somebody else chose. A message's rendered Text begins with
// a banner marking what follows as data, not instructions ([HasBanner]), and
// [Provenance] says whether the sender could be verified — check
// Provenance.Aligned before acting on what a message asks. Structured
// content decodes into the typed views through [ReadOutput.Into].
//
// # Events
//
// [Client.Wait] blocks for one window and returns the first batch of events,
// or an empty result when nothing happened; pass its cursor to the next Wait.
// [Client.Watch] holds one stream open for a long-running consumer. Delivery
// is at least once: dedupe on [JournalEvent.ID]. Resuming from a cursor
// replays everything after it, including what arrived while nothing was
// listening.
//
// # Writes
//
// Pass an IdempotencyKey on every [Client.Send]: a repeat with the same key
// returns the first send's result instead of sending again, and this package
// never retries a send without one.
package bud
