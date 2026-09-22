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
// unblock it, and on a conflict the object as it now stands plus the version to
// quote. Wrapping it as a Go error would put every caller in the position of
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
type ByID struct {
	ID string `json:"id"`

	// MaxChars bounds the rendering. Omit for the server's default.
	MaxChars int `json:"max_chars,omitempty"`

	// Quoted expands the earlier messages a reply folded away.
	Quoted bool `json:"quoted,omitempty"`

	Render string `json:"render,omitempty"`
	Format string `json:"format,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

// In names a collection by the identifier of whatever holds it, with the filters
// that used to be a query string.
//
// Typed fields rather than a string to assemble: a schema a caller can see is the
// difference between a filter that works and one that is silently ignored.
type In struct {
	In     string `json:"in"`
	Folder string `json:"folder,omitempty"`
	Month  string `json:"month,omitempty"`

	Unread bool   `json:"unread,omitempty"`
	Since  string `json:"since,omitempty"`
	Before string `json:"before,omitempty"`
	From   string `json:"from,omitempty"`
	Thread string `json:"thread,omitempty"`
	Agent  bool   `json:"agent,omitempty"`
	Q      string `json:"q,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

// Change writes to an existing object.
type Change struct {
	ID      string          `json:"id"`
	Changes json.RawMessage `json:"changes"`

	// Version is required to change something that exists. A conflict comes back
	// carrying the current object and the version to quote, so the retry is a
	// merge rather than another read.
	Version string `json:"version,omitempty"`

	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// Create makes a new object inside a collection.
type Create struct {
	// In is the collection that will hold it: a calendar identifier makes an
	// event, a book identifier a contact. Empty where the collection is implied,
	// as it is for a draft or a blob.
	In   string          `json:"in,omitempty"`
	Body json.RawMessage `json:"body"`

	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// Remove deletes or cancels an object.
//
// Named for the caller's intent. What it does depends on the kind: a draft is
// discarded, an event is cancelled and its attendees are told.
type Remove struct {
	ID      string `json:"id"`
	Version string `json:"version,omitempty"`
}

// DescribeMe returns who this caller is and what it can reach.
//
// The first call worth making: every reference needs an identifier and this is
// where they come from.
func (c *Client) DescribeMe(ctx context.Context) (ReadOutput, error) {
	var out ReadOutput
	return out, c.call(ctx, "me.describe", struct{}{}, &out)
}

// Health reports whether this credential is accepted and the node is ready.
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

// ListFolder returns a folder's messages.
//
// The summaries carry subject, sender, provenance and unread, which is what makes
// triage one call rather than one call per message.
func (c *Client) ListFolder(ctx context.Context, in In) (ReadOutput, error) {
	var out ReadOutput
	if in.In == "" {
		return out, preflight(CodeInvalid, "ListFolder needs a mailbox in In")
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

// DescribeThread returns a conversation in order.
func (c *Client) DescribeThread(ctx context.Context, in ByID) (ReadOutput, error) {
	var out ReadOutput
	if err := requireID(in.ID, PrefixThread, "DescribeThread"); err != nil {
		return out, err
	}
	return out, c.call(ctx, "thread.describe", in, &out)
}

// UpdateMessage marks a message read, labels it, or moves it between folders.
func (c *Client) UpdateMessage(ctx context.Context, in Change) (PutOutput, error) {
	var out PutOutput
	if err := requireChange(in, PrefixMessage, "UpdateMessage"); err != nil {
		return out, err
	}
	return out, c.call(ctx, "message.update", in, &out)
}

// UpdateThread sets a conversation's state, owner or labels.
func (c *Client) UpdateThread(ctx context.Context, in Change) (PutOutput, error) {
	var out PutOutput
	if err := requireChange(in, PrefixThread, "UpdateThread"); err != nil {
		return out, err
	}
	return out, c.call(ctx, "thread.update", in, &out)
}

// CreateDraft stores a message without sending it.
func (c *Client) CreateDraft(ctx context.Context, in Create) (PutOutput, error) {
	var out PutOutput
	if len(in.Body) == 0 {
		return out, preflight(CodeInvalid, "CreateDraft needs a Body")
	}
	return out, c.call(ctx, "draft.create", in, &out)
}

// UpdateDraft changes a stored draft.
func (c *Client) UpdateDraft(ctx context.Context, in Change) (PutOutput, error) {
	var out PutOutput
	if err := requireChange(in, PrefixDraft, "UpdateDraft"); err != nil {
		return out, err
	}
	return out, c.call(ctx, "draft.update", in, &out)
}

// DeleteDraft discards a draft.
func (c *Client) DeleteDraft(ctx context.Context, in Remove) (PutOutput, error) {
	var out PutOutput
	if err := requireID(in.ID, PrefixDraft, "DeleteDraft"); err != nil {
		return out, err
	}
	return out, c.call(ctx, "draft.delete", in, &out)
}

// DescribeDraft returns a stored draft.
func (c *Client) DescribeDraft(ctx context.Context, in ByID) (ReadOutput, error) {
	var out ReadOutput
	if err := requireID(in.ID, PrefixDraft, "DescribeDraft"); err != nil {
		return out, err
	}
	return out, c.call(ctx, "draft.describe", in, &out)
}

// DescribeHold returns a message waiting for a person to approve it.
func (c *Client) DescribeHold(ctx context.Context, in ByID) (ReadOutput, error) {
	var out ReadOutput
	if err := requireID(in.ID, PrefixMessage, "DescribeHold"); err != nil {
		return out, err
	}
	return out, c.call(ctx, "hold.describe", in, &out)
}

// UpdateHold approves or rejects a held message.
func (c *Client) UpdateHold(ctx context.Context, in Change) (PutOutput, error) {
	var out PutOutput
	if err := requireChange(in, PrefixMessage, "UpdateHold"); err != nil {
		return out, err
	}
	return out, c.call(ctx, "hold.update", in, &out)
}

// Send composes and sends a message.
//
// Read State on the way out: "held" means a person has to approve it, and sending
// again sends a second copy. Pass an IdempotencyKey if the call may be retried —
// this package will not retry a send without one, because it cannot invent a key
// that survives a restart.
func (c *Client) Send(ctx context.Context, in SendInput) (SendOutput, error) {
	var out SendOutput
	if len(in.To) == 0 && in.Draft == "" {
		return out, preflight(CodeInvalid, "Send needs To, or a Draft to send")
	}
	return out, c.call(ctx, "send", in, &out)
}

// Search finds messages across every mailbox this caller can read.
func (c *Client) Search(ctx context.Context, in In) (ReadOutput, error) {
	var out ReadOutput
	if in.Q == "" {
		return out, preflight(CodeInvalid, "Search needs a query in Q")
	}
	return out, c.call(ctx, "search", in, &out)
}

// Wait blocks until something happens, or until the timeout.
//
// An empty result is success: see [WaitOutput.Empty]. For a long-lived consumer
// prefer [Client.Watch], which holds one connection instead of paying a round trip
// per quiet interval.
func (c *Client) Wait(ctx context.Context, in WaitInput) (WaitOutput, error) {
	var out WaitOutput
	return out, c.call(ctx, "watch", in, &out)
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
// A type switch over the four, rather than reflection: the set is closed, and a
// switch is the spelling a reader can check.
func envelopeError(out any) *Error {
	switch v := out.(type) {
	case *ReadOutput:
		return v.Error
	case *PutOutput:
		return v.Error
	case *SendOutput:
		return v.Error
	case *WaitOutput:
		return v.Error
	default:
		return nil
	}
}
