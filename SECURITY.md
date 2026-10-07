# Security Policy

## Scope

This document covers vulnerabilities in the `noetive-sdk-go` source
tree: the Go packages under
`go.noetive.io/noetive-sdk-go/...`, their direct
dependencies as pinned in `go.mod`, and the example programs under
each service subpackage (e.g. `semantik/examples/`). Today that is two
clients: `semantik` and `bud`.

Out of scope:

- The Noetive services themselves, Semantik and Bud (different
  repositories, different operational boundary). Report service
  vulnerabilities through the channels on <https://noetive.io>.
- Issues in transitive dependencies that are not reachable from SDK
  code.
- API keys or other credentials leaked outside the SDK (e.g. in your
  application logs, environment, or shell history).

## Supported versions

Only the latest released minor line receives security fixes. The SDK
follows Go module semantic versioning; once 1.0 is cut, the previous
stable minor line will be maintained until the next release and a
deprecation note will land here.

| Version | Status |
|---|---|
| 0.2.x | Supported |
| < 0.2 | Unsupported |

## Reporting a vulnerability

Please report vulnerabilities privately. Do **not** open a public
GitHub issue or pull request for a suspected security bug.

Report by **email** to <security@noetive.eu>, using subject line
`[noetive-sdk-go security] <short description>`.

Include, where possible:

- SDK version (`semantik.Version`, `bud.Version`, or the `go.mod` pin).
- Go toolchain and OS/architecture.
- A minimal reproduction (fewer lines and fewer dependencies is
  better; an `httptest.Server` is ideal for decoder or scanner bugs).
- Impact assessment (crash, hang, memory disclosure, credential leak,
  request forgery, etc.).
- Any known workaround.

## What happens next

- **Acknowledgement** within 3 business days.
- **Triage and severity assessment** using CVSS v3.1.
- **Fix window** proportional to severity:
  - Critical/High: aim for a patched release within 30 days.
  - Medium/Low: bundled into the next scheduled release.
- **Coordinated disclosure**: we publish the advisory and credit the
  reporter (if they consent) after a fix is released. Embargo length
  is negotiated with the reporter.
- **CVE assignment**: we request a CVE for anything Medium or above.

## Known security-relevant behaviour

This section documents load-bearing safety decisions so that
third-party auditors can confirm them rather than having to infer
from code.

- **Credential handling.** API keys are held in the `Client` struct
  and sent as `Authorization: Bearer <key>`. The SDK never logs the
  key and never writes it to disk; printing a `Client` with `%v` or
  `%#v` shows it redacted. The User-Agent does not include any secret
  material. Neither client follows a redirect, so a 3xx cannot carry
  the credential to a host the caller did not name. A `bud` client
  built with `Forwarding` holds no key at all and sends only the one
  carried on each call's context. `New` rejects only empty /
  whitespace-only keys; it does not inspect the key's prefix or contents. Deeper
  validation is the server's job, and prefix-locking the client would
  break the moment Noetive introduces a new key family.
- **TLS.** The default HTTP transport is the stdlib's
  `http.DefaultTransport.Clone()` with `ResponseHeaderTimeout`. TLS
  verification is enabled by default; users who need a custom root
  CA set must supply their own `*http.Client` via `WithHTTPClient`.
- **JSON decoder.** `semantik` uses `github.com/goccy/go-json` for both
  encode and decode. Because that decoder performs unsafe pointer
  arithmetic, the SDK wraps every untrusted decode in `recover()`
  (`semantik/safejson.go`) so regular panics surface as errors instead of
  crashing the caller. Under `-race` mode, Go's `checkptr` detector
  may raise a non-recoverable `runtime.throw` on crafted malformed
  input; this is tracked upstream and the SDK's own unit tests are
  race-clean.
  `bud` decodes every response with the standard library's
  `encoding/json`, because its responses carry text written by
  strangers — subjects, display names, filenames — and a decoder
  that cannot be made to crash matters more there than a faster one.
- **Size bounds.** Every stream frame and response body is bounded, so
  a misbehaving server or proxy cannot decide how much memory a client
  holds. `semantik` caps stream frames at 64 KiB
  (`internal/sse.MaxFrameBytes`) and responses at 1 MiB; `bud` caps
  stream frames at 4 MiB and responses at 32 MiB. The Content-Type of
  `semantik`'s `/v1/subscribe` and `bud`'s `/v1/watch` is checked to be
  `text/event-stream` before any body bytes are consumed.
- **Fuzz coverage.** `FuzzScanner` (in `./internal/sse`),
  `FuzzErrorDecode`, `FuzzRequestEncode`, and `FuzzMetadataValidation`
  (in `./semantik`) exercise the stream scanner both clients share and
  every untrusted-input path `semantik` exposes. `bud`'s response and
  stream handling is covered by unit tests, not fuzzers. Run with
  `go test -run=^$ -fuzz=<name> -fuzztime=30s <package>` to reproduce.
- **Retry safety.** By default `semantik` absorbs a single transient
  server hiccup per call. It honours the server's wait hint when one
  is offered and otherwise falls back to a bounded delay so a missing
  hint cannot turn a transient failure into a terminal one. Errors
  that signal a caller-side problem — bad input, missing or invalid
  auth, billing not in good standing, hard rate limits — are never
  retried automatically; they fail fast. Callers needing strict
  one-shot semantics can pass `WithRetry(NoRetry{})`. Publish without
  an `IdempotencyKey` is the caller's responsibility: pair every
  retry-eligible publish with a key so a retry cannot duplicate.
  `bud` retries only a connection that failed before any response
  arrived; a refusal is returned to the caller, never retried. A send
  is retried only when it carries an `IdempotencyKey`, and no retry
  policy can widen that.

## Hall of fame

Reporters who submit valid vulnerabilities will be credited here
(with their consent) once a fix is released.
