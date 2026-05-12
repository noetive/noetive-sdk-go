# Security Policy

## Scope

This document covers vulnerabilities in the `noetive-sdk-go` source
tree: the Go packages under
`github.com/noetive/noetive-sdk-go/...`, their direct
dependencies as pinned in `go.mod`, and the example programs under
each service subpackage (e.g. `semantik/examples/`).

Out of scope:

- The Semantik service itself (different repository, different
  operational boundary). Report service vulnerabilities through the
  channels on <https://noetive.io>.
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

Preferred channels, in order:

1. **GitHub Security Advisory** — submit at
   <https://github.com/noetive/noetive-sdk-go/security/advisories/new>.
   This keeps the discussion embargoed while we coordinate a fix.
2. **Email** — <support@noetive.io>. Use subject line
   `[noetive-sdk-go security] <short description>`.

Include, where possible:

- SDK version (`semantik.Version`, the equivalent symbol on another
  service subpackage, or `go.mod` pin).
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
- **CVE assignment**: we request a CVE through the GitHub Security
  Advisory workflow for anything Medium or above.

## Known security-relevant behaviour

This section documents load-bearing safety decisions so that
third-party auditors can confirm them rather than having to infer
from code.

- **Credential handling.** API keys are held in the `Client` struct
  and sent as `Authorization: Bearer <key>`. The SDK never logs the
  key and never writes it to disk. The User-Agent does not include
  any secret material. `New` validates the key's prefix only
  (`keyu_` / `keyt_`) — deeper validation is the server's job.
- **TLS.** The default HTTP transport is the stdlib's
  `http.DefaultTransport.Clone()` with `ResponseHeaderTimeout`. TLS
  verification is enabled by default; users who need a custom root
  CA set must supply their own `*http.Client` via `WithHTTPClient`.
- **JSON decoder.** The SDK uses `github.com/goccy/go-json` for both
  encode and decode. Because that decoder performs unsafe pointer
  arithmetic, the SDK wraps every untrusted decode in `recover()`
  (`semantik/safejson.go`) so regular panics surface as errors instead of
  crashing the caller. Under `-race` mode, Go's `checkptr` detector
  may raise a non-recoverable `runtime.throw` on crafted malformed
  input; this is tracked upstream and the SDK's own unit tests are
  race-clean.
- **SSE stream.** Frames are capped at 64 KiB (`internal/sse.MaxFrameBytes`).
  Response body size for structured responses is capped at 1 MiB
  (`semantik/safejson.go:maxResponseBytes`). The Content-Type of
  `/v1/subscribe` responses is validated to be `text/event-stream`
  before any body bytes are consumed.
- **Fuzz coverage.** `FuzzScanner` (in `./internal/sse`),
  `FuzzErrorDecode`, `FuzzRequestEncode`, and `FuzzMetadataValidation`
  (in `./semantik`) exercise every untrusted-input path the SDK
  exposes. Run with
  `go test -run=^$ -fuzz=<name> -fuzztime=30s <package>` to reproduce.
- **Retry safety.** By default the SDK absorbs a single transient
  server hiccup per call. It honours the server's wait hint when one
  is offered and otherwise falls back to a bounded delay so a missing
  hint cannot turn a transient failure into a terminal one. Errors
  that signal a caller-side problem — bad input, missing or invalid
  auth, billing not in good standing, hard rate limits — are never
  retried automatically; they fail fast. Callers needing strict
  one-shot semantics can pass `WithRetry(NoRetry{})`. Publish without
  an `IdempotencyKey` is the caller's responsibility: pair every
  retry-eligible publish with a key so a retry cannot duplicate.

## Hall of fame

Reporters who submit valid vulnerabilities will be credited here
(with their consent) once a fix is released.
