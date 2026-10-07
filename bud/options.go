package bud

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"strings"
	"time"
)

// Configuration, and the one timeout that must not exist.
//
// # Why there is no WithTimeout
//
// http.Client.Timeout bounds the whole exchange including the body, and two calls
// here legitimately hold a response open: Wait blocks for up to twenty-five
// seconds, and Watch streams for as long as the caller stays attached. A single
// timeout covering both would sever exactly the calls this package exists to
// make, and it would do it in a way that looks like the server going quiet.
//
// So there are two knobs and no third: how long to wait for a connection, and how
// long to wait for the first byte of a response. Neither bounds the body.

// DefaultBaseURL is the endpoint a client uses unless told otherwise.
//
// The apex name rather than a regional one. A base URL compiled into other
// people's programs is the one string that cannot move, so it points at the name
// that is geo-routed rather than at the region that happens to serve it today.
const DefaultBaseURL = "https://bud.noetive.io"

// Environment variables. Exactly two, and resisting a third is the point: a knob
// that arrives through ambient environment is one a caller did not choose and
// cannot see. Everything else is an Option.
//
// The key is the same variable every Noetive SDK reads, because one key reaches
// every product. The base URL is not: each product has its own endpoint, so each
// has its own variable.
const (
	// EnvToken is the agent's Noetive developer key. Bud mints no credential of
	// its own; this is the key the control plane sealed for the agent.
	EnvToken = "NOETIVE_KEY_SECRET"

	// EnvBaseURL points the client somewhere other than production.
	EnvBaseURL = "NOETIVE_BUD_BASE_URL"
)

// Doer is the part of *http.Client this package uses.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Option configures a Client.
type Option func(*config)

type config struct {
	httpClient Doer
	retry      RetryPolicy
	baseURL    string
}

// WithBaseURL points the client at another deployment.
func WithBaseURL(url string) Option {
	return func(c *config) { c.baseURL = url }
}

// WithHTTPClient replaces the transport.
//
// Do not pass a client with Timeout set. See the note at the top of this file:
// it would cut Wait's window and the stream, which are the two calls whose whole
// purpose is to stay open. Bound the wait for response headers instead, as the
// default transport does with [DefaultResponseTimeout]: without it, a server that
// accepts the connection and never answers holds a Wait handshake for as long as
// the caller's context allows.
func WithHTTPClient(d Doer) Option {
	return func(c *config) { c.httpClient = d }
}

// WithRetry installs a retry policy. [NoRetry] opts out.
func WithRetry(p RetryPolicy) Option {
	return func(c *config) { c.retry = p }
}

// NewFromEnv builds a client from the two environment variables.
func NewFromEnv(opts ...Option) (*Client, error) {
	token := strings.TrimSpace(getenv(EnvToken))
	if token == "" {
		return nil, preflight(CodeInvalid, "%s is not set", EnvToken)
	}
	if isUnexpandedPlaceholder(token) {
		// An editor launched from a desktop icon often never reads a shell
		// profile, so "${NOETIVE_KEY_SECRET}" arrives literally. Sending it
		// would get back "unauthorized" and send somebody to check an account
		// that is fine.
		return nil, preflight(CodeInvalid,
			"%s contains an unexpanded ${...} placeholder rather than a token", EnvToken)
	}

	all := opts
	if base := strings.TrimSpace(getenv(EnvBaseURL)); base != "" {
		// Before the caller's options, so an explicit WithBaseURL still wins:
		// an argument is a decision, the environment is a fallback.
		all = append([]Option{WithBaseURL(base)}, opts...)
	}
	return New(token, all...)
}

// isUnexpandedPlaceholder recognises a shell variable that was never substituted.
func isUnexpandedPlaceholder(v string) bool {
	return strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}")
}

// --- The credential on a context -------------------------------------------

type authKey struct{}

// WithAuthorizationContext carries a credential for one call.
//
// For a [Forwarding] client: the header belongs to the request being relayed and
// must not outlive it, so it travels on the context rather than on the client.
func WithAuthorizationContext(ctx context.Context, header string) context.Context {
	if header == "" {
		return ctx
	}
	return context.WithValue(ctx, authKey{}, header)
}

// AuthorizationFrom returns the credential a context is carrying, or empty.
//
// Empty is a normal answer. A forwarding client then sends no Authorization
// header and the server answers its own refusal, which is the one the caller
// should relay — see [Forwarding].
func AuthorizationFrom(ctx context.Context) string {
	header, _ := ctx.Value(authKey{}).(string)
	return header
}

// --- Transport -------------------------------------------------------------

// Connect and response budgets. Separate, because a connection that is not there
// and a server that has not answered are different problems with different fixes.
const (
	// DefaultConnectTimeout bounds reaching the host.
	DefaultConnectTimeout = 5 * time.Second

	// DefaultResponseTimeout bounds waiting for the first byte of a response.
	//
	// It ends once headers arrive, so it never cuts the stream Wait and Watch
	// hold open: the server sends its headers as soon as the stream opens. It
	// is generous because it is also what bounds the handshake of a Wait, which
	// starts its own window only after the stream is open.
	DefaultResponseTimeout = 45 * time.Second
)

// MaxWaitSeconds is the longest window Wait holds the stream open, and the
// longest quiet interval the server allows before a keepalive.
//
// A caller whose own deadline is shorter than its window will see its normal
// empty answer as a cancelled request — which is the reading that makes an agent
// retry immediately, against the description that told it not to.
const MaxWaitSeconds = 25

func defaultHTTPClient() *http.Client {
	// Cloned rather than DefaultTransport itself: a package that mutated the
	// process-wide transport would change behaviour for everything else in the
	// program.
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Client{CheckRedirect: refuseRedirect}
	}
	t := tr.Clone()
	t.ResponseHeaderTimeout = DefaultResponseTimeout
	t.IdleConnTimeout = 90 * time.Second

	return &http.Client{
		Transport: t,
		// No Timeout. See the note at the top of this file.
		CheckRedirect: refuseRedirect,
	}
}

// refuseRedirect stops a 3xx from relocating the credential.
//
// Following one would re-attach the Authorization header to whatever Location
// names, which is a host the caller never chose.
func refuseRedirect(req *http.Request, _ []*http.Request) error {
	return errors.New("bud: refusing to follow a redirect to " + req.URL.Host +
		"; a redirect would send the credential somewhere the caller did not name")
}

// UserAgent identifies this SDK to the server.
//
// The same shape the other packages in this module use, so server logs can group
// requests by SDK family.
func UserAgent() string {
	return "noetive-sdk-go/" + Version + " (" + runtime.Version() + "; " +
		runtime.GOOS + "/" + runtime.GOARCH + ")"
}

// Version is this package's version. Bumped with the module.
const Version = "0.1.0"
