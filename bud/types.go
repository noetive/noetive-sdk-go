package bud

import (
	"encoding/json"
	"time"
)

// The wire types, hand-written to match a contract the server also declares.
//
// # Why hand-written and not generated
//
// These are the same shapes the service declares. Its definitions are not
// published as a Go module, so this package cannot import them, and there are
// only a couple of dozen structs — few enough that writing them is cheaper than owning a
// generator, and the doc comments are worth more than anything a generator would
// produce.
//
// What is checked rather than hoped for is the *encoding*. The service describes
// its own shapes, that description is vendored under testdata, and this package
// reproduces it by reflection over these structs and fails its build when the two
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

	// Truncated says there is more than this answer carries. Cursor, when set,
	// continues it; a thread that left out older messages and a part cut at
	// its read limit have none.
	Cursor    string `json:"cursor,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`

	// Version is declared by the wire and not sent today: no update takes a
	// version to quote.
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

	// ThreadJoin is how the message joined its conversation: JoinNew,
	// JoinVerified, or JoinClaimed for one that only claims to be a reply.
	ThreadJoin string `json:"thread_join,omitempty"`

	// Folder and Removed are declared by the wire and not sent today.
	Folder  string         `json:"folder,omitempty"`
	Removed map[string]int `json:"removed,omitempty"`
}

// PutInput writes to whatever a reference names.
type PutInput struct {
	Ref  string          `json:"ref"`
	Body json.RawMessage `json:"body"`

	// Version is declared by the wire; no write served today takes one, and
	// one sent is refused as invalid.
	Version string `json:"version,omitempty"`

	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// PutOutput is what a write returns.
type PutOutput struct {
	Ref string `json:"ref,omitempty"`

	// Version is declared by the wire and not sent today.
	Version string `json:"version,omitempty"`
	Created bool   `json:"created,omitempty"`

	// Effects names what the write changed: EffectMailRead, EffectLabelled.
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
	// To may be empty when CC or BCC names someone, or on a reply, which takes
	// its recipients from the message replied to. A send with no recipients at
	// all is refused.
	To      []string `json:"to"`
	Subject string   `json:"subject,omitempty"`
	Text    string   `json:"text,omitempty"`
	HTML    string   `json:"html,omitempty"`

	CC  []string `json:"cc,omitempty"`
	BCC []string `json:"bcc,omitempty"`

	From string `json:"from,omitempty"`

	// OnBehalfOf writes as another agent. It needs a grant, and this server
	// refuses it: nothing here can confirm a person decided that message.
	OnBehalfOf string `json:"on_behalf_of,omitempty"`

	InReplyTo string `json:"in_reply_to,omitempty"`
	ReplyAll  bool   `json:"reply_all,omitempty"`

	// Attach is declared by the wire and refused today as invalid: attaching is
	// not served yet.
	Attach []string `json:"attach,omitempty"`

	// Agent is the machine-readable part, for an exchange between two agents:
	// an object with bud, intent, corr, in_reply_to_corr, schema, hops and
	// payload. Other keys are dropped, and expect is refused. Send hops as the
	// count on the message being answered; the server adds one.
	Agent json.RawMessage `json:"agent,omitempty"`

	// IdempotencyKey makes a retried send return the first one's result for
	// seven days instead of sending again. A different message under the same
	// key also returns the first result and sends nothing, so never reuse one.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// SendOutput is what a send returns.
//
// A new send is StateQueued. A repeat with the same IdempotencyKey reports the
// message's state now, which may be any later one. A refusal comes back as
// Error, naming the guard that fired.
type SendOutput struct {
	Message string `json:"message,omitempty"`
	Thread  string `json:"thread,omitempty"`

	State string `json:"state,omitempty"`

	// Reason says why a queued message waits instead of going now —
	// "provisioning" while the sender's domain is verified, or "paused" — and,
	// on a repeat for a message that failed, why it failed.
	Reason string `json:"reason,omitempty"`

	// Remaining is what is left of each sending limit after this message.
	Remaining map[string]int `json:"remaining,omitempty"`

	Error *Error `json:"error,omitempty"`
}

// WaitInput says what Watch and Wait follow, and from where.
type WaitInput struct {
	// Cursor is where to resume. Empty starts at the tail.
	Cursor string `json:"cursor,omitempty"`

	// Mailbox narrows to one mailbox; empty means every mailbox the key can
	// read, and one it cannot is refused as forbidden_scope. Carries no omitempty: the server's
	// identifier type encodes its zero value as "", so a document that omitted
	// the field would not be one the server produces.
	Mailbox string `json:"mailbox"`

	// TimeoutSeconds is how long a quiet interval lasts before the server sends a
	// keepalive, and for Wait, how long the window is. At most MaxWaitSeconds;
	// zero takes it. It never ends a Watch.
	TimeoutSeconds int `json:"timeout_s,omitempty"`

	// Types narrows to these event types, the Event constants. Empty matches
	// everything; an unknown type is refused before the stream opens.
	Types []string `json:"types,omitempty"`

	// AgentPart, when set, narrows to messages carrying a machine-readable part
	// (true) or to messages without one (false). Nil matches both.
	AgentPart *bool `json:"agent_part,omitempty"`
}

// WaitOutput is what Wait returns, and what each batch on a stream carries.
//
// An empty Events with a nil Error is success, not failure: nothing happened
// within the window. Cursor is where to resume, including after a refusal.
type WaitOutput struct {
	Events []JournalEvent `json:"events,omitempty"`
	Cursor string         `json:"cursor"`
	Error  *Error         `json:"error,omitempty"`
}

// Empty reports that the window closed and nothing happened.
//
// It is not a failure and there is nothing to retry: call Wait again from the
// returned Cursor, which waits another window. Treating it as an error —
// backing off, alerting, discarding the cursor — is the mistake this exists to
// prevent.
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
	// Kind is ActorSystem, ActorAgent or ActorOperator.
	Kind string `json:"kind"`

	// Agent is the agent that acted, when Kind is ActorAgent.
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

	Guard   string `json:"guard,omitempty"`
	Counter string `json:"counter,omitempty"`
	Reason  string `json:"reason,omitempty"`
	State   string `json:"state,omitempty"`

	// Provider is an opaque delivery reference for a message that was handed on.
	Provider string `json:"provider,omitempty"`

	// MessageID is the RFC 5322 Message-ID, without angle brackets. Set on
	// mail.sent: the Message-ID a recipient sees is the one the provider assigns
	// when it accepts the message, after Send has answered, so SendOutput cannot
	// carry it.
	MessageID string `json:"message_id,omitempty"`
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

	Mailbox string `json:"mailbox,omitempty"`

	// ReadableMailboxes is every mailbox this caller may read. A finite list
	// rather than a rule, because no scope means "all".
	ReadableMailboxes []string `json:"readable_mailboxes,omitempty"`

	Scopes  []string `json:"scopes,omitempty"`
	Granted []string `json:"granted,omitempty"`

	// Limits is what is left of each sending budget.
	Limits map[string]int `json:"limits,omitempty"`

	// Sending says whether mail sent from these addresses goes out now.
	Sending *SendingStatus `json:"sending,omitempty"`
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

// The types below are not in the contract: the views Content holds for the kinds
// that have one, and SendingStatus, which Me carries. The contract describes Me
// only as carrying a "sending object", so nothing checks these against the
// service; they follow the published API description field for field. Read the
// views through [ReadOutput.Into].

// SendingStatus says whether mail an agent sends now goes out now, and if not,
// why. Status is one of SendingReady, SendingProvisioning or SendingPaused.
//
// Worth reading before composing: a new agent's domain takes minutes to verify,
// and a send in that window is queued, not delivered.
type SendingStatus struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// MailboxView is a mailbox's own state: how much is waiting and what it may do.
// Content for KindMailbox.
type MailboxView struct {
	Mailbox string `json:"mailbox"`

	// Paused says an operator stopped this mailbox sending, and Reason says why.
	Paused bool   `json:"paused,omitempty"`
	Reason string `json:"reason,omitempty"`

	// Folders is how many messages are in each.
	Folders map[string]int `json:"folders,omitempty"`

	Correspondents int `json:"correspondents,omitempty"`

	Limits map[string]int `json:"limits,omitempty"`

	// Approximate says a count hit its bound and the real number is at least
	// what is reported.
	Approximate bool `json:"approximate,omitempty"`

	Sending *SendingStatus `json:"sending,omitempty"`
}

// Correspondents is who a mailbox has exchanged mail with. Content for KindCorr.
type Correspondents struct {
	Mailbox        string              `json:"mailbox"`
	Correspondents []CorrespondentView `json:"correspondents,omitempty"`
}

// CorrespondentView is one address and the traffic with it.
//
// Facts about traffic, not a curated contact. Addr is whatever a sender put on
// the wire.
type CorrespondentView struct {
	Addr string `json:"addr"`

	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`

	Inbound  int `json:"inbound,omitempty"`
	Outbound int `json:"outbound,omitempty"`

	// LastAuth and LastAligned are the last verdict rather than a summary: an
	// address that passed a hundred times and fails now is the shape of an
	// account takeover, and an average would hide it.
	LastAuth    string `json:"last_auth,omitempty"`
	LastAligned bool   `json:"last_aligned,omitempty"`
}

// PartView is one part of a message — an attachment or a body alternative — in
// the mode that was asked for. Content for KindPart.
type PartView struct {
	Message string `json:"message"`
	Part    string `json:"part"`
	Mode    string `json:"mode"`

	// Type and Filename are what the sender declared, and are claims rather than
	// facts.
	Type     string `json:"type,omitempty"`
	Filename string `json:"filename,omitempty"`
	Size     int64  `json:"size,omitempty"`

	// Bytes is the part's content, for PartModeBytes. The server sends standard
	// base64; a byte slice is what the decoder turns that into, so the caller
	// never handles the encoding.
	Bytes []byte `json:"bytes_base64,omitempty"`
}

// Into decodes Content into one of the views.
//
// Everything Content carries about mail was written by somebody else — an
// address, a subject, a filename — and this decodes it with the same decoder the
// rest of the package uses, so a caller does not pick its own for stranger-written
// bytes. A refusal comes back as itself. A view of the wrong kind is refused,
// as a CodeInvalid *Error with no HTTPStatus, rather than decoded into zero
// values that would read as an empty mailbox; so is an answer with no Content,
// such as help, whose Text is the whole of it.
//
// The kind is checked for this package's views — *Me, *MailboxView, *Listing,
// *Correspondents, *Conversation, *PartView. Any other type, a caller's own
// struct for a message's state included, is decoded as asked.
func (o ReadOutput) Into(v any) error {
	if o.Error != nil {
		return o.Error
	}
	if want := viewKind(v); want != "" && o.Kind != want {
		return preflight(CodeInvalid, "a %s answer does not decode into a %s view", quoted(o.Kind), quoted(want))
	}
	if len(o.Content) == 0 {
		return preflight(CodeInvalid, "this %s answer carries no content; read its Text", quoted(o.Kind))
	}
	return json.Unmarshal(o.Content, v)
}

// viewKind is the kind whose Content a view decodes, or empty for a type this
// package does not know, which is decoded as asked.
func viewKind(v any) string {
	switch v.(type) {
	case *Me:
		return KindMe
	case *MailboxView:
		return KindMailbox
	case *Listing:
		return KindFolder
	case *Correspondents:
		return KindCorr
	case *Conversation:
		return KindThread
	case *PartView:
		return KindPart
	default:
		return ""
	}
}
