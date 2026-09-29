package semantik

import "strings"

// Forwarding a caller's own credential, for a relay.
//
// A gateway that serves many principals holds none of their keys: it takes the
// Authorization header off the request it is relaying and passes it to the backend
// verbatim. That is why [NewForwarding] exists beside [New] — and why it skips the
// non-empty check that [New] makes.
//
// The check is right for [New]: a program configuring itself with an empty key has
// made a mistake worth catching before a round trip. It is wrong for a relay. An
// absent credential there is not the relay's mistake, and refusing locally would
// answer with [ErrInvalidAPIKey] — a client-side error, with no request id and in a
// shape the server never produces. The caller then sees a failure Noetive did not
// author and cannot act on, instead of the server's own 401 with its own
// explanation.
//
// So: send what arrived, including nothing, and let the service answer.

// WithAuthorization sets the Authorization header verbatim.
//
// Passed through untouched — not parsed, not normalised, not prefixed. Parsing a
// credential is the first step towards deciding about it, and that decision belongs
// to the service that issued it. The day a new key family or scheme appears, nothing
// here needs to change.
func WithAuthorization(header string) Option {
	return func(c *config) { c.authHeader = header }
}

// NewForwarding constructs a Client that carries whatever credential it is given,
// including none.
//
// For a relay handling one request. Pair it with [WithAuthorization] per call rather
// than building one client and keeping it: the credential belongs to the request
// being relayed, and a client that outlived the request would be a client one caller
// could be served another's through.
//
//	c, err := semantik.NewForwarding(
//	    semantik.WithAuthorization(header),
//	    semantik.WithBaseURL(base),
//	    semantik.WithHTTPClient(shared),
//	)
func NewForwarding(opts ...Option) (*Client, error) {
	cfg := config{baseURL: defaultBaseURL}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.httpClient == nil {
		cfg.httpClient = defaultHTTPClient()
	}
	if cfg.retry == nil {
		cfg.retry = TransientRetry(1)
	}
	cfg.baseURL = strings.TrimRight(cfg.baseURL, "/")

	return &Client{
		http:       cfg.httpClient,
		retry:      cfg.retry,
		baseURL:    cfg.baseURL,
		authHeader: cfg.authHeader,
	}, nil
}
