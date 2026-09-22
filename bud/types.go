package bud

import (
	"encoding/json"
	"time"
)

// The wire types, hand-written to match a contract the server also declares.
//
// # Why hand-written and not generated
//
// These are the same shapes noetive-bud declares in its own pkg/wire. That module
// is private, so a public SDK cannot import it, and there are only eighteen
// structs — few enough that writing them is cheaper than owning a generator, and
// the doc comments are worth more than anything a generator would produce.
//
// What is checked rather than hoped for is the *encoding*. The server emits a
// description of its own shapes with `bud contract`; this package reproduces that
// description by reflection over these structs and fails its build when the two
// disagree. See contract.go. A field renamed, retyped or re-tagged on either side
// is a red test, not a field that silently stops arriving.
//
// # Three rules that look like style and are not
//
// An identifier field is never `omitempty`. The server's identifiers are typed
// structs whose zero value encodes as "", so omitting the field produces a
// different document than the server ever sends. `WaitOutput.Cursor` has no
// omitempty for the same reason and one more: the server declares it required
// even on a refusal.
//
// Nothing here declares MarshalJSON or UnmarshalJSON. Three encoders see these
// shapes — this package's, the server's, and whatever an MCP runtime uses — and
// they agree byte for byte exactly as long as none of them is overridden.

// ReadInput asks for whatever a reference names.
type ReadInput struct {
	Ref      string `json:"ref"`
	MaxChars int    `json:"max_chars,omitempty"`
	Cursor   string `json:"cursor,omitempty"`
}

// ReadOutput is what a read returns.
//
// Text and Content are both set for most kinds and carry the same object two
// ways: Text is the rendering a model reads, Content the structure a program
// branches on. A caller that gates on provenance should read Provenance rather
// than parse Text — that is the whole reason the two travel together.
type ReadOutput struct {
	Ref  string `json:"ref,omitempty"`
	Kind string `json:"kind,omitempty"`

	// Text is the rendering. For a message or a thread it begins with a banner
	// naming what follows as data rather than instructions; see HasBanner.
	Text string `json:"text,omitempty"`

	Content    json.RawMessage `json:"content,omitempty"`
	Provenance *Provenance     `json:"provenance,omitempty"`

	Cursor    string `json:"cursor,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`

	// Version is what a caller must quote to change this object, for the kinds
	// that are mutable. Empty for the ones that are not.
	Version string `json:"version,omitempty"`

	Error *Error `json:"error,omitempty"`
}

// Provenance is how much a message's claims about itself can be trusted.
//
// Separate from the content so a caller can refuse to act on an unauthenticated
// message without reading a word of it.
type Provenance struct {
	SPF   string `json:"spf,omitempty"`
	DKIM  string `json:"dkim,omitempty"`
	DMARC string `json:"dmarc,omitempty"`

	// Aligned is the one to branch on: the authenticated domain and the visible
	// From agree. SPF passing on its own says only that somebody was allowed to
	// send from that server.
	Aligned  bool `json:"aligned"`
	InTenant bool `json:"in_tenant"`
	Known    bool `json:"known"`

	ThreadJoin string `json:"thread_join,omitempty"`
	Folder     string `json:"folder,omitempty"`

	// Removed counts characters the renderer dropped, by reason — bidi
	// overrides, zero-width joiners, hidden HTML. A non-empty map means the text
	// a human would have seen is not the text that arrived.
	Removed map[string]int `json:"removed,omitempty"`
}

// PutInput writes to whatever a reference names.
type PutInput struct {
	Ref  string          `json:"ref"`
	Body json.RawMessage `json:"body"`

	// Version is required to change something that already exists. Omitting it
	// on an existing object is refused rather than treated as "overwrite".
	Version string `json:"version,omitempty"`

	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// PutOutput is what a write returns.
type PutOutput struct {
	Ref     string `json:"ref,omitempty"`
	Version string `json:"version,omitempty"`
	Created bool   `json:"created,omitempty"`

	// Effects names what else the write caused — messages queued, invitations
	// sent. A write that sends mail does not do it silently.
	Effects []Effect `json:"effects,omitempty"`

	Error *Error `json:"error,omitempty"`
}

// Effect is one consequence of a write.
type Effect struct {
	Kind  string `json:"kind"`
	Count int    `json:"count,omitempty"`
	Ref   string `json:"ref,omitempty"`
}

// SendInput composes and sends a message.
type SendInput struct {
	To      []string `json:"to"`
	Subject string   `json:"subject,omitempty"`
	Text    string   `json:"text,omitempty"`
	HTML    string   `json:"html,omitempty"`

	CC  []string `json:"cc,omitempty"`
	BCC []string `json:"bcc,omitempty"`

	From string `json:"from,omitempty"`

	// OnBehalfOf writes as another agent. It needs a grant and is normally held
	// for a person to approve, which is the point rather than a limitation.
	OnBehalfOf string `json:"on_behalf_of,omitempty"`

	InReplyTo string `json:"in_reply_to,omitempty"`
	ReplyAll  bool   `json:"reply_all,omitempty"`
	Draft     string `json:"draft,omitempty"`

	// Attach takes references, not bytes: "blob_01j…" or "message_01j…/part/3".
	// Forwarding an attachment therefore copies nothing.
	Attach []string `json:"attach,omitempty"`

	// Agent is the machine-readable part, for an exchange between two agents.
	Agent json.RawMessage `json:"agent,omitempty"`

	// Hold asks for approval even where policy would let the message go.
	Hold bool `json:"hold,omitempty"`

	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// SendOutput is what a send returns.
//
// State is the field to read. "held" is not a failure and not an error — the
// message is waiting for a person, and sending again sends a second copy.
type SendOutput struct {
	Message string `json:"message,omitempty"`
	Thread  string `json:"thread,omitempty"`

	// MessageID is the RFC 5322 Message-ID, without angle brackets.
	MessageID string `json:"message_id,omitempty"`

	State  string `json:"state,omitempty"`
	Reason string `json:"reason,omitempty"`

	// Remaining is what is left of each sending limit after this message.
	Remaining map[string]int `json:"remaining,omitempty"`

	Error *Error `json:"error,omitempty"`
}

// Held reports that the message is waiting for a person to approve it.
//
// A helper because the mistake it prevents is the expensive one: reading "held"
// as a failure and sending again, which queues a second copy behind the first.
func (o SendOutput) Held() bool { return o.Error == nil && o.State == StateHeld }

// WaitInput is the long poll.
type WaitInput struct {
	Cursor string `json:"cursor,omitempty"`

	// Mailbox narrows to one mailbox. Carries no omitempty: the server's
	// identifier type encodes its zero value as "", so a document that omitted
	// the field would not be one the server produces.
	Mailbox string `json:"mailbox"`

	TimeoutSeconds int      `json:"timeout_s,omitempty"`
	Types          []string `json:"types,omitempty"`

	// AgentPart narrows to messages carrying a machine-readable part.
	AgentPart *bool `json:"agent_part,omitempty"`
}

// WaitOutput is what a poll returns.
//
// An empty Events with a nil Error is success, not failure: nothing happened
// within the timeout. Cursor is always set, including on a refusal.
type WaitOutput struct {
	Events []JournalEvent `json:"events,omitempty"`
	Cursor string         `json:"cursor"`
	Error  *Error         `json:"error,omitempty"`
}

// Empty reports that the poll completed and nothing happened.
//
// It is not a failure. Calling again immediately spends the budget the call was
// meant to wait with — which is the single most expensive mistake on this
// surface, because the retry is instant and the next poll is identical.
func (o WaitOutput) Empty() bool { return o.Error == nil && len(o.Events) == 0 }

// JournalEvent is one thing that happened.
type JournalEvent struct {
	ID     string    `json:"id"`
	Tenant string    `json:"tenant"`
	At     time.Time `json:"ts"`

	// Seq orders events within a mailbox. Delivery is at-least-once, so dedupe
	// on ID rather than assuming each event arrives once.
	Seq uint64 `json:"seq"`

	Type    string `json:"type"`
	Mailbox string `json:"mailbox"`
	Message string `json:"message"`
	Thread  string `json:"thread"`

	Actor Actor `json:"actor"`

	// Corr ties this event to the request that caused it, and to the other
	// events that request caused. It is the same token the response carried as
	// X-Request-Id, so a send and its delivery receipt are one story.
	Corr string `json:"corr,omitempty"`

	Data EventData `json:"data"`
}

// Actor is who caused an event.
type Actor struct {
	Kind  string `json:"kind"`
	Agent string `json:"agent"`
}

// EventData is the little an event carries about itself.
//
// Enough to triage on without opening anything, and deliberately no more: every
// field here is also a field somebody else chose, so it is summary rather than
// content.
type EventData struct {
	From     string `json:"from,omitempty"`
	Subject  string `json:"subject,omitempty"`
	Aligned  bool   `json:"aligned,omitempty"`
	InTenant bool   `json:"in_tenant,omitempty"`
	Known    bool   `json:"known,omitempty"`

	AgentPart bool   `json:"agent_part,omitempty"`
	Folder    string `json:"folder,omitempty"`
	ITIP      string `json:"itip,omitempty"`

	Guard    string `json:"guard,omitempty"`
	Counter  string `json:"counter,omitempty"`
	Reason   string `json:"reason,omitempty"`
	State    string `json:"state,omitempty"`
	Provider string `json:"provider,omitempty"`
}

// Me is who the caller is and what it can reach.
//
// The first call worth making. Every other reference needs an identifier, and
// this is where they come from.
type Me struct {
	Agent  string `json:"agent"`
	Tenant string `json:"tenant"`
	Kind   string `json:"kind,omitempty"`
	Name   string `json:"name,omitempty"`

	// Addresses are what this agent sends as. The first is the default.
	Addresses []string `json:"addresses,omitempty"`

	Mailbox   string   `json:"mailbox,omitempty"`
	Calendars []string `json:"calendars,omitempty"`
	Book      string   `json:"book,omitempty"`

	// ReadableMailboxes is every mailbox this caller may read. A finite list
	// rather than a rule, because no scope means "all".
	ReadableMailboxes []string `json:"readable_mailboxes,omitempty"`

	Scopes  []string `json:"scopes,omitempty"`
	Granted []string `json:"granted,omitempty"`

	// Limits is what is left of each sending budget.
	Limits map[string]int `json:"limits,omitempty"`
}

// Listing is a folder's contents.
type Listing struct {
	Mailbox  string    `json:"mailbox"`
	Folder   string    `json:"folder"`
	Messages []Summary `json:"messages,omitempty"`
}

// Conversation is a thread's messages in order.
type Conversation struct {
	Thread   string    `json:"thread"`
	Messages []Summary `json:"messages,omitempty"`
}

// Summary is one message, as much as a listing shows.
type Summary struct {
	Message string    `json:"message"`
	Thread  string    `json:"thread,omitempty"`
	Subject string    `json:"subject,omitempty"`
	Date    time.Time `json:"date"`

	From     string `json:"from,omitempty"`
	FromName string `json:"from_name,omitempty"`

	// The provenance to triage on without opening anything.
	Aligned  bool   `json:"aligned"`
	InTenant bool   `json:"in_tenant"`
	Known    bool   `json:"known"`
	Join     string `json:"join,omitempty"`

	Attachments int `json:"attachments,omitempty"`

	// Unread is stated rather than inferred from an absent field, because it is
	// the first thing a listing is triaged on and an inference would be a guess
	// about an omission.
	Unread bool `json:"unread,omitempty"`

	// Incomplete means the stored message is missing parts — a rendering that
	// failed, a blob still uploading. What is here is true; it is not all of it.
	Incomplete bool `json:"incomplete,omitempty"`
}
