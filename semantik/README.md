# Semantik

The Go client for [Noetive Semantik](https://noetive.io) — the managed
semantic search and subscription service. Publish messages tagged with
embedding vectors, query them with the SemQL query language, and
subscribe to live match streams over Server-Sent Events.

```
go get go.noetive.io/noetive-sdk-go/semantik
```

Requires Go 1.25.2 or newer.

Full API documentation: https://pkg.go.dev/go.noetive.io/noetive-sdk-go/semantik

## Quick start

```go
import "context"
import "go.noetive.io/noetive-sdk-go/semantik"

c, err := semantik.New("keyu_...")   // or semantik.NewFromEnv()
if err != nil { log.Fatal(err) }

// Namespace, Model and Dimensions are required on every request.
res, err := c.Search(context.Background(), semantik.SearchRequest{
    Query:      `MATCH DISTANCE("machine learning research") WITHIN 0.4 LIMIT 10`,
    Namespace:  "global",
    Model:      "Qwen3-Embedding-4B",
    Dimensions: 1024,
})
```

## Targeting: namespace, model, dimensions are required

Every publish, search and subscribe **must** set `Namespace`, `Model`
and `Dimensions` explicitly. The SDK applies **no defaults** to these
fields: an empty `Namespace`, an empty `Model`, or a zero `Dimensions`
is rejected by client-side pre-flight validation before the request
reaches the wire.

This is a data-isolation guarantee. Defaulting `Namespace` to a shared
value (such as `"global"`) would let a caller who simply forgot the
field route sensitive data into a namespace they never intended. Making
the field required removes that hazard entirely — the SDK fails fast
rather than guessing.

The `"global"` namespace remains a valid target (provisioned for every
account with the `Qwen3-Embedding-4B` model at 1024 dimensions); it just
has to be named explicitly like any other.

## Authentication

Pass an API key from the Noetive dashboard. Keys start with `keyu_`
(user) or `keyt_` (tenant). The SDK refuses only an empty or
whitespace-only key; it does not inspect the prefix or contents, so the
server is the source of truth for everything else.

`semantik.NewFromEnv()` reads:

| Variable | Required | Purpose |
|---|---|---|
| `NOETIVE_KEY_SECRET` | yes | Your API key |
| `NOETIVE_BASE_URL` | no | Override endpoint (defaults to `https://semantik.noetive.io`) |

## Error handling

The SDK surfaces three distinct error classes.

| Class | How to detect |
|---|---|
| API error returned by the server | `errors.As(err, &semantik.Error{})` |
| Transport error (DNS / TCP / TLS) | `var ue *url.Error; errors.As(err, &ue)` |
| Context error (caller cancelled or timed out) | `errors.Is(err, context.Canceled)` |

Every API error carries a `RequestID` you can quote when contacting
support, plus per-class sentinels you can match with `errors.Is` for
finer-grained handling. See the package godoc for the full list.

Client-side pre-flight validation (oversized vectors, malformed
metadata, etc.) produces an `*Error` with `HTTPStatus == 0` — the
request never reached the wire. Use this to distinguish bugs in the
caller from server-side rejections.

## Retry semantics

By default the SDK rides out transient hiccups on your behalf —
brief server pushback, warm-up windows, momentary unavailability —
without surfacing them to your code. Each retry honours the
duration the server requests, falling back to a bounded schedule
that gives the service time to recover between attempts.
Errors that signal something the caller must change (bad input,
missing auth, billing problems, hard rate limits) are never
retried, so configuration mistakes still fail fast.

Pair Publish calls with an `IdempotencyKey` so a retry can never
produce a duplicate. Opt out for strict one-shot semantics:

```go
c, _ := semantik.New(key) // transient retries on by default

c, _ = semantik.New(key,
    semantik.WithRetry(semantik.NoRetry{}), // strict one-shot
)
```

The default bound is tuned for the survivorship most workloads
want: keep going through ordinary glitches, stop before piling on
when the server is asking for room. Callers who need a different
shape can pass their own bound or `RetryPolicy`.


## Subscribe: SSE match streams

`Subscribe` returns `*Subscription` after reading the initial
`subscribed` frame, so `sub.ID()` is populated before the first match
arrives.

```go
sub, err := c.Subscribe(ctx, semantik.SubscribeRequest{
    Query:      `MATCH DISTANCE("gpu shortage") WITHIN 0.5`,
    Namespace:  "global",
    Model:      "Qwen3-Embedding-4B",
    Dimensions: 1024,
})
if err != nil { log.Fatal(err) }
defer sub.Close()

log.Print(sub.ID())
for ev, err := range sub.Events(ctx) {
    if err != nil { break }
    handle(ev)
}
```

Notes:

- `Close` is idempotent and safe to call from any goroutine.
- The public API does **not** send heartbeats. If your network path
  has proxies that reap idle connections, wrap each `Next` call in
  `context.WithTimeout` or accept that the stream ends silently on
  reaper-induced TCP RST.
- The handshake (POST → first `subscribed` frame) is covered by the
  configured retry policy: short transient pushback on setup is
  ridden out automatically because each handshake either succeeds
  with a fresh `subscription_id` or fails before any state is
  committed. Once the stream is open, mid-stream failures surface as
  `*SubscribeStreamError` for you to handle — the SDK never silently
  reconnects, because that would drop matches between the old and new
  subscription. Wrap `Subscribe`+`Next` in your own loop and dedupe
  on `MessageID` if you need reconnect semantics.

## Examples

Every example under `examples/` is a self-contained `main` program
you can run against a live server with a real API key.

| Directory | Demonstrates |
|---|---|
| [`health`](examples/health/main.go) | Unauthenticated liveness probe |
| [`lint`](examples/lint/main.go) | Validate + auto-complete a SemQL query (no auth) |
| [`search`](examples/search/main.go) | SemQL text-anchor search |
| [`publish_text`](examples/publish_text/main.go) | Text publish, `ack=stored` |
| [`publish_vector`](examples/publish_vector/main.go) | Vector publish, `ack=durable`, idempotency key, retry policy |
| [`subscribe`](examples/subscribe/main.go) | Live SSE match stream using `iter.Seq2` |
| [`errors`](examples/errors/main.go) | Idiomatic error inspection (`errors.Is`, `errors.As`, pre-flight vs server) |

Run any of them from the module root with:

```
NOETIVE_KEY_SECRET=keyu_... go run ./semantik/examples/<name>
```

## Performance

The encode hot path is pooled via `sync.Pool`; the SSE scanner uses
`bufio.Scanner` with a pooled backing buffer. Alloc ceilings are
guarded by `TestZeroAllocHot` (fails CI on regression):

| Path | Allocs/op |
|---|---|
| `SearchRequest` encode | 3 |
| `PublishRequest` (vector, d=384) encode | 3 |
| One SSE match frame parse | 2 |

Track with benchmarks in `bench_test.go`:

```
go test -run=^$ -bench=. -benchmem -count=10 ./semantik
```

## Testing

```
# Unit + race
go test -race -count=1 ./...

# Fuzzers (short run; CI-friendly)
go test -run=^$ -fuzz=FuzzScanner             -fuzztime=30s ./internal/sse
go test -run=^$ -fuzz=FuzzErrorDecode         -fuzztime=30s ./semantik
go test -run=^$ -fuzz=FuzzRequestEncode       -fuzztime=30s ./semantik
go test -run=^$ -fuzz=FuzzMetadataValidation  -fuzztime=30s ./semantik

# Integration (hits https://semantik.noetive.io)
NOETIVE_KEY_SECRET=keyu_... go test -tags=integration -count=1 -v ./semantik/integration/...
```

The SDK uses `github.com/goccy/go-json` for both encode and decode.
Because that decoder performs unsafe pointer arithmetic, the SDK
wraps every untrusted decode in `recover()` to convert panics into
regular errors. Under `-race` mode, malformed input may still trigger
Go's `checkptr` detector (a `runtime.throw` that cannot be
recovered); avoid running fuzzers with `-race` if your CI treats this
as a failure. Unit tests are race-clean.

## Versioning

`semantik.Version` carries the SDK release. This version is embedded
in the `User-Agent` of every outgoing request:

```
noetive-sdk-go/<Version> (<go-runtime>; <goos>/<goarch>)
```

## Licence

Noetive Commercial. See https://noetive.io/terms for the full text.
