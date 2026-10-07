// Package semantik is the official Go client for the Noetive Semantik
// managed semantic search and subscription service.
//
// # Quick start
//
//	c, err := semantik.New("keya_...")
//	if err != nil { log.Fatal(err) }
//
//	res, err := c.Search(ctx, semantik.SearchRequest{
//	    Query:      `MATCH DISTANCE("machine learning") WITHIN 0.4 LIMIT 10`,
//	    Namespace:  "global",
//	    Model:      "Qwen3-Embedding-4B",
//	    Dimensions: 1024,
//	})
//
// Namespace, Model and Dimensions are REQUIRED on every publish, search
// and subscribe — the SDK applies no defaults and rejects an unset field
// at preflight. Defaulting Namespace to a shared value would let a
// forgotten field route sensitive data into a namespace the caller never
// intended, so the SDK fails fast instead. See [SearchRequest],
// [PublishRequest] and [SubscribeRequest].
//
// # Authentication
//
// Requests carry an API key as Authorization: Bearer <key>. Keys are
// issued by the Noetive dashboard, and one key reaches every Noetive
// service. The SDK checks only that the key is non-empty; it does not inspect the prefix or
// contents, so deeper validation is left to the server.
//
// Environment constructor: [NewFromEnv] reads NOETIVE_KEY_SECRET (required)
// and NOETIVE_SEMANTIK_BASE_URL (optional, defaults to https://semantik.noetive.io).
//
// # Error model
//
//   - API errors surface as *[Error]. Use [errors.As] for full detail
//     (including [Error.RequestID] for support correlation), or
//     [errors.Is] against the package sentinels for code-specific
//     branching when you need it.
//   - Transport errors (DNS failure, TCP reset, TLS handshake) are returned
//     raw from [net/http] and are NOT wrapped in *[Error].
//   - Context errors pass through raw: errors.Is(err, context.Canceled)
//     and errors.Is(err, context.DeadlineExceeded) continue to work.
//   - An *[Error] with HTTPStatus == 0 was produced by client-side pre-flight
//     validation before the request was sent.
//
// # Concurrency
//
// *[Client] is safe for concurrent use. It holds no mutable state after
// construction. Subscriptions are single-reader: only one goroutine may
// call [Subscription.Next] at a time, but [Subscription.Close] is safe
// to call from any goroutine and is idempotent.
//
// # SSE streaming
//
// [Client.Subscribe] opens a Server-Sent Events stream. The initial
// "subscribed" frame is consumed inside Subscribe so that
// [Subscription.ID] is populated on return; subsequent "match" frames
// surface through [Subscription.Next] or the range-over-func adapter
// [Subscription.Events]. The public API does not send heartbeats, so
// callers behind idle-connection-reaping proxies should bound each read
// with [context.WithTimeout].
package semantik
