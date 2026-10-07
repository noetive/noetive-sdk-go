package bud

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// The client, and the one decision in it worth arguing about.
//
// # A refusal is a value, not a Go error
//
// Bud answers a rejected request with the operation's own output type: a
// populated Error inside a well-formed envelope, at a status derived from the
// code. This package returns that with a nil error, because the envelope is what
// the caller acts on — the code to branch on, the hint that says what would
// unblock it, the limit that fired and when it clears. Wrapping it as a Go error would put every caller in the position of
// rebuilding a value it was already handed.
//
// A non-nil error means something that is not about the request: a connection
// that failed, a body that did not decode, a context that expired, or this
// package refusing to send at all. Those carry no code.
//
//	out, err := c.DescribeMessage(ctx, bud.ByID{ID: id})
//	switch {
//	case err != nil:       // transport; nothing to branch on
//	case out.Error != nil: // refusal; branch on out.Error.Code
//	default:               // out.Text, out.Content
//	}
//
// This differs from the semantik package in this module, which returns a non-2xx
// as *semantik.Error. The divergence is deliberate: that service's envelope is
// four flat fields, and bud's carries the things a caller needs in order to
// recover. Flattening it to match would delete them.
//
// [Client.Watch] is the exception. A stream has no envelope to carry a refusal,
// so Watch returns the server's refusal as an *Error, and [Stream.Err] does the
// same for one sent in flight; use errors.As and branch on its Code.

// maxResponseBytes bounds a reply.
//
// Generous, because a rendered thread is legitimately large, and bounded, because
// the alternative is letting a misbehaving proxy decide how much this process
// allocates.
const maxResponseBytes = 32 << 20

// Client talks to one bud deployment.
//
// Immutable after construction and safe for concurrent use. It holds a base URL,
// a transport and at most one credential; everything per-request lives on the
// request.
type Client struct {
	doer    Doer
	baseURL string
	retry   RetryPolicy

	// authorization is the header value, precomputed. Empty on a forwarding
	// client, which reads it from the context instead.
	authorization string

	// forwarding says the credential comes from the context per call.
	forwarding bool
}

// New builds a client that carries one Noetive developer key, sealed by the
// control plane. Bud mints no credential of its own: this key is what an
// agent authenticates with.
func New(token string, opts ...Option) (*Client, error) {
	if strings.TrimSpace(token) == "" {
		return nil, preflight(CodeInvalid, "a key is required; pass one or use Forwarding")
	}
	c := newClient(opts...)
	c.authorization = "Bearer " + token
	return c, nil
}

// Forwarding builds a client that holds no credential and takes one from the
// context on every call.
//
// For a relay. It does not validate, parse or cache what it is given, and a call
// with nothing in the context goes out with no Authorization header at all — bud
// then answers its own unauthorized envelope, with its own hint, and the caller
// relays that. Manufacturing a refusal here would hand the caller a failure in a
// shape bud never produces.
func Forwarding(opts ...Option) (*Client, error) {
	c := newClient(opts...)
	c.forwarding = true
	return c, nil
}

func newClient(opts ...Option) *Client {
	cfg := config{baseURL: DefaultBaseURL, retry: defaultRetry(), httpClient: defaultHTTPClient()}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &Client{
		doer:          cfg.httpClient,
		baseURL:       strings.TrimRight(cfg.baseURL, "/"),
		retry:         cfg.retry,
		authorization: cfg.authorization,
	}
}

// String redacts. GoString too, so %#v in a log line cannot print a credential.
func (c *Client) String() string   { return "bud.Client{baseURL: " + c.baseURL + ", token: [redacted]}" }
func (c *Client) GoString() string { return c.String() }

// --- The operations ---------------------------------------------------------
//
// One method per intent, named as the server names them. A typed request each,
// validated before anything is sent: a request this package can tell is wrong is
// cheaper to refuse here than to send and have refused.

// ByID names one object by its self-describing identifier.
//
// One field, because the identifier's prefix says which kind it is. A caller that
// pasted a thread identifier where a message one belonged is refused by the
// server's parser rather than by a handler that guessed.
//
// Shared by the describe operations, and each takes only its own fields: a field
// another operation reads — MaxChars on DescribeThread, Mode on DescribeMessage —
// is refused as invalid. Part is the exception, and is ignored outside
// DescribePart.
type ByID struct {
	ID string `json:"id"`

	// MaxChars bounds DescribeMessage's rendering: at most, and by default,
	// 20000. A longer one comes back Truncated with a Cursor to continue from.
	// Ignored with RenderRaw.
	MaxChars int `json:"max_chars,omitempty"`

	// Render is how DescribeMessage renders: RenderText, RenderRaw or RenderParts.
	Render string `json:"render,omitempty"`

	// Cursor continues a truncated rendering.
	Cursor string `json:"cursor,omitempty"`

	// Part names one part of a message, by the number a RenderParts reading
	// lists it under. Only DescribePart reads it; the other reads ignore it.
	Part string `json:"part,omitempty"`

	// Mode is how DescribePart returns the part: PartModeText or PartModeBytes.
	Mode string `json:"mode,omitempty"`

	// MaxMessages is how many of a thread's most recent messages DescribeThread
	// renders: 20 by default, at most 50.
	MaxMessages int `json:"max_messages,omitempty"`
}

// In names a collection by the identifier of whatever holds it, with the filters
// that used to be a query string.
//
// Typed fields rather than a string to assemble: a schema a caller can see is the
// difference between a filter that works and one that is silently ignored.
type In struct {
	// In is the mailbox, by its agent's identifier.
	In string `json:"in"`

	// Folder is FolderInbox, the default, FolderSent or FolderQuarantine.
	Folder string `json:"folder,omitempty"`

	// Unread and Agent narrow to unread messages and to messages carrying a
	// machine-readable part. False is no filter.
	Unread bool `json:"unread,omitempty"`
	Agent  bool `json:"agent,omitempty"`

	// Since and Before bound the message date, RFC 3339; Since is inclusive.
	// Anything else is refused rather than read as a different window.
	Since  string `json:"since,omitempty"`
	Before string `json:"before,omitempty"`

	From   string `json:"from,omitempty"`
	Thread string `json:"thread,omitempty"`

	// Q narrows correspondents to addresses containing it, ignoring case.
	Q string `json:"q,omitempty"`

	// Limit is how many entries to examine for this page, not how many match:
	// 50 by default, at most 200. With a filter a page can come back short or
	// empty and still carry a Cursor; keep paging until there is none.
	Limit int `json:"limit,omitempty"`

	// Cursor resumes a listing. Pass back unchanged the one the previous page
	// of the same operation returned.
	Cursor string `json:"cursor,omitempty"`
}

// Change writes to an existing object.
//
// There is no version to quote: an update is not compare-and-swap, and the last
// write wins. Labels replace the message's labels whole. A body that carries a
// version anyway is refused as invalid, at /version, which is why Change has no
// field for one.
type Change struct {
	ID string `json:"id"`

	// Changes is {"read": bool, "labels": [string]}, at least one. Labels
	// replace the message's labels whole, so an empty list removes them all.
	Changes json.RawMessage `json:"changes"`

	// IdempotencyKey is remembered on a best-effort basis for up to an hour.
	// Reusing one for a different change is refused as invalid.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// DescribeMe returns who this caller is and what it can reach.
//
// The first call worth making: every reference needs an identifier and this is
// where they come from.
func (c *Client) DescribeMe(ctx context.Context) (ReadOutput, error) {
	var out ReadOutput
	return out, c.call(ctx, "me.describe", struct{}{}, &out)
}

// Health reports whether this credential is accepted, and as which agent.
//
// Smaller than DescribeMe on purpose. It answers the one question a probe asks,
// and the unauthenticated /health cannot answer it at all.
func (c *Client) Health(ctx context.Context) (Health, error) {
	var out Health
	return out, c.call(ctx, "health", struct{}{}, &out)
}

// Health is what a health probe returns.
type Health struct {
	Contract string `json:"contract"`
	Agent    string `json:"agent"`
	Tenant   string `json:"tenant"`

	// Error is a refusal: a key that reaches no agent here is forbidden_scope, an
	// account that cannot be charged is not_billable. Either way the key itself
	// works; only unauthorized means it does not.
	Error *Error `json:"error,omitempty"`
}

// DescribeCatalog returns the operations this deployment serves and the reference
// shapes beneath them.
//
// Worth one call at startup: it says which operations this build actually
// implements, which is otherwise discovered as a refusal in the middle of a task.
func (c *Client) DescribeCatalog(ctx context.Context) (Catalog, error) {
	var out Catalog
	return out, c.call(ctx, "catalog.describe", struct{}{}, &out)
}

// Catalog is what DescribeCatalog returns.
type Catalog struct {
	Contract   string             `json:"contract"`
	Operations []CatalogOperation `json:"operations"`
	Kinds      []CatalogKind      `json:"kinds"`

	Error *Error `json:"error,omitempty"`
}

// CatalogOperation is one operation, as a client discovers it.
type CatalogOperation struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Kind   string `json:"kind,omitempty"`
	Served bool   `json:"served"`
	Doc    string `json:"doc,omitempty"`
	Body   string `json:"body,omitempty"`
}

// CatalogKind is one reference shape.
type CatalogKind struct {
	Kind     string   `json:"kind"`
	Pattern  string   `json:"pattern"`
	Doc      string   `json:"doc,omitempty"`
	Readable bool     `json:"readable"`
	Writable bool     `json:"writable"`
	Served   bool     `json:"served"`
	Verb     string   `json:"verb,omitempty"`
	Action   string   `json:"action,omitempty"`
	Params   []string `json:"params,omitempty"`
}

// Served reports whether this deployment implements an operation.
func (c Catalog) Served(name string) bool {
	for _, op := range c.Operations {
		if op.Name == name {
			return op.Served
		}
	}
	return false
}

// ListFolder returns a folder's messages, oldest first, in the order they were
// filed. A message that arrives mid-listing appears on a later page rather than
// shuffling one already read.
//
// The summaries carry subject, sender, provenance and unread, which is what makes
// triage one call rather than one call per message.
func (c *Client) ListFolder(ctx context.Context, in In) (ReadOutput, error) {
	var out ReadOutput
	if err := requireID(in.In, PrefixAgent, "ListFolder"); err != nil {
		return out, err
	}
	return out, c.call(ctx, "folder.list", in, &out)
}

// DescribeMessage returns one message, rendered and structured.
func (c *Client) DescribeMessage(ctx context.Context, in ByID) (ReadOutput, error) {
	var out ReadOutput
	if err := requireID(in.ID, PrefixMessage, "DescribeMessage"); err != nil {
		return out, err
	}
	return out, c.call(ctx, "message.describe", in, &out)
}

// DescribeThread returns a conversation's most recent messages, in the order
// they were sent. Each is cut at 4000 characters; read one with
// DescribeMessage for the whole of it. Truncated means older messages were left
// out, not that one was cut.
func (c *Client) DescribeThread(ctx context.Context, in ByID) (ReadOutput, error) {
	var out ReadOutput
	if err := requireID(in.ID, PrefixThread, "DescribeThread"); err != nil {
		return out, err
	}
	return out, c.call(ctx, "thread.describe", in, &out)
}

// DescribePart returns one part of a message: an attachment, or a body
// alternative.
//
// PartModeText, the default, renders a part that is already text into Text,
// under the same banner as a message body; any other type is refused as
// unavailable in that mode. At most the first 2 MiB is read, and a longer part
// comes back Truncated with no cursor to continue from.
//
// PartModeBytes puts the whole content in [PartView.Bytes]. A part too large to
// return whole is refused as policy_refused, guard "part_size", rather than cut.
// A message withheld for malware refuses every part, guard "virus".
//
// Read either through [ReadOutput.Into] with a *PartView.
func (c *Client) DescribePart(ctx context.Context, in ByID) (ReadOutput, error) {
	var out ReadOutput
	if err := requireID(in.ID, PrefixMessage, "DescribePart"); err != nil {
		return out, err
	}
	if in.Part == "" {
		return out, preflight(CodeInvalid, "DescribePart needs a Part")
	}
	return out, c.call(ctx, "part.describe", in, &out)
}

// DescribeMailbox returns a mailbox's own state: what is waiting in each folder,
// whether it may send, and what is left of its limits.
//
// The identifier is the agent's, since an agent and its mailbox are one. Read
// the answer through [ReadOutput.Into] with a *MailboxView.
func (c *Client) DescribeMailbox(ctx context.Context, in ByID) (ReadOutput, error) {
	var out ReadOutput
	if err := requireID(in.ID, PrefixAgent, "DescribeMailbox"); err != nil {
		return out, err
	}
	return out, c.call(ctx, "mailbox.describe", in, &out)
}

// ListCorrespondents returns who a mailbox has exchanged mail with.
//
// Q, Limit and Cursor are the filters it accepts; the server ignores Folder and
// refuses the rest. Read the answer through
// [ReadOutput.Into] with a *Correspondents.
func (c *Client) ListCorrespondents(ctx context.Context, in In) (ReadOutput, error) {
	var out ReadOutput
	if err := requireID(in.In, PrefixAgent, "ListCorrespondents"); err != nil {
		return out, err
	}
	return out, c.call(ctx, "correspondent.list", in, &out)
}

// UpdateMessage marks a message read or unread, or replaces its labels.
//
// A CodePreconditionFailed means other label writes kept landing first. A read
// change in the same request may already have applied; the request is safe to
// repeat as it was.
func (c *Client) UpdateMessage(ctx context.Context, in Change) (PutOutput, error) {
	var out PutOutput
	if err := requireChange(in, PrefixMessage, "UpdateMessage"); err != nil {
		return out, err
	}
	return out, c.call(ctx, "message.update", in, &out)
}

// Send composes and sends a message.
//
// The server either queues the message or refuses it, naming the guard; nothing
// waits for approval.
//
// Pass an IdempotencyKey on every send: without one, a timeout retried is a second
// message, and this package will not retry a send without one, because it cannot
// invent a key that survives a restart. A repeat with the same key returns the
// first send's result for seven days and sends nothing; one that arrives while
// the first is still running is refused as rate_limited with a wait.
func (c *Client) Send(ctx context.Context, in SendInput) (SendOutput, error) {
	var out SendOutput
	if len(in.To)+len(in.CC)+len(in.BCC) == 0 && in.InReplyTo == "" {
		return out, preflight(CodeInvalid, "Send needs a recipient in To, CC or BCC, or InReplyTo for a reply")
	}
	return out, c.call(ctx, "send", in, &out)
}

// --- Plumbing --------------------------------------------------------------

// requireID refuses an identifier of the wrong kind before sending.
//
// The prefix is the kind, so this is checkable here, and catching it here turns a
// round trip into a compile-adjacent mistake. It does not validate the rest: that
// is the server's parser's job and duplicating it would be a second grammar.
func requireID(id, prefix, op string) error {
	switch {
	case id == "":
		return preflight(CodeInvalid, "%s needs an ID", op)
	case !strings.HasPrefix(id, prefix):
		return preflight(CodeInvalid, "%s needs an identifier beginning %q, got %q", op, prefix, firstPrefix(id))
	default:
		return nil
	}
}

func requireChange(in Change, prefix, op string) error {
	if err := requireID(in.ID, prefix, op); err != nil {
		return err
	}
	if len(in.Changes) == 0 {
		return preflight(CodeInvalid, "%s needs Changes", op)
	}
	return nil
}

// firstPrefix is the part of an identifier up to and including the underscore,
// for a message that names what arrived without quoting the whole thing.
func firstPrefix(id string) string {
	if i := strings.IndexByte(id, '_'); i >= 0 {
		return id[:i+1]
	}
	if len(id) > 16 {
		return id[:16]
	}
	return id
}

// call sends one operation and decodes its envelope.
func (c *Client) call(ctx context.Context, op string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return preflight(CodeInvalid, "the request could not be encoded: %v", err)
	}

	attempt := 0
	for {
		status, raw, requestID, err := c.send(ctx, op, body)
		if err == nil {
			return decodeEnvelope(status, raw, requestID, out)
		}

		// Only a connection that failed before any byte of a response is worth
		// retrying, and only where retrying cannot duplicate an effect.
		if !c.retry.ShouldRetry(attempt, op, in) {
			return err
		}
		if waitErr := c.retry.Wait(ctx, attempt); waitErr != nil {
			return err
		}
		attempt++
	}
}

// send performs one request.
func (c *Client) send(ctx context.Context, op string, body []byte) (int, []byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/v1/"+op, bytes.NewReader(body))
	if err != nil {
		return 0, nil, "", preflight(CodeInvalid, "the request could not be built: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent())
	if header := c.credential(ctx); header != "" {
		req.Header.Set("Authorization", header)
	}

	resp, err := c.doer.Do(req)
	if err != nil {
		return 0, nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return resp.StatusCode, nil, resp.Header.Get("X-Request-Id"), err
	}
	return resp.StatusCode, raw, resp.Header.Get("X-Request-Id"), nil
}

// credential is the Authorization header for this call.
func (c *Client) credential(ctx context.Context) string {
	if c.forwarding {
		return AuthorizationFrom(ctx)
	}
	return c.authorization
}

// decodeEnvelope reads the operation's own output out of a response.
//
// Decoded with encoding/json rather than this module's usual encoder, and that is
// a deliberate divergence from the semantik package. These bodies carry text a
// stranger wrote — a subject, a display name, a filename — and goccy/go-json
// v0.10.6 has two out-of-bounds reads on exactly that kind of input, one of which
// is a fatal checkptr violation under -race that recover cannot intercept. The
// server encodes with goccy and decodes with the standard library for this reason;
// this package is the other end of the same exchange and takes the same side.
func decodeEnvelope(status int, raw []byte, requestID string, out any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return errorFrom(status, raw, requestID)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		// The status said something; the body did not say it in our shape. An
		// ALB's HTML 502 arrives here, and so does a proxy's plain-text 413.
		return errorFrom(status, raw, requestID)
	}

	// A refusal is a value. Everything the server said about what to do next is
	// already in the envelope; this only fills in what the envelope cannot carry.
	if e := envelopeError(out); e != nil {
		e.HTTPStatus = status
		if e.RequestID == "" {
			e.RequestID = requestID
		}
		return nil
	}

	// A success at a failing status is the one combination that cannot be
	// believed: either the body is not the operation's output or the status is
	// wrong, and guessing which would hide a real disagreement.
	if status < 200 || status > 299 {
		return errorFrom(status, raw, requestID)
	}
	return nil
}

// envelopeError reaches the Error inside whichever output this was.
//
// A type switch over the five, rather than reflection: the set is closed, and a
// switch is the spelling a reader can check. An output missing from it decodes a
// refusal and then reports the status as malformed, so a new output type belongs
// here the day it is added.
func envelopeError(out any) *Error {
	switch v := out.(type) {
	case *ReadOutput:
		return v.Error
	case *PutOutput:
		return v.Error
	case *SendOutput:
		return v.Error
	case *Health:
		return v.Error
	case *Catalog:
		return v.Error
	default:
		return nil
	}
}
