# noetive-sdk-go

Go SDKs for [Noetive](https://noetive.io) services. Each service has its
own subpackage with its own godoc, examples, and integration tests.

| Service | Import path | Docs |
|---|---|---|
| Semantik — semantic search, publish, subscribe | `go.noetive.io/noetive-sdk-go/semantik` | [godoc](https://pkg.go.dev/go.noetive.io/noetive-sdk-go/semantik) · [README](semantik/README.md) |

Requires Go 1.25.2 or newer.

```
go get go.noetive.io/noetive-sdk-go/semantik
```

## Example

Init a client, then publish, search, and stream live matches — using
the response from each call. Defaults target the `global` namespace.

```go
import "go.noetive.io/noetive-sdk-go/semantik"

ctx := context.Background()
c, _ := semantik.NewFromEnv() // reads NOETIVE_KEY_SECRET

// Publish. Use the response: server-assigned ID + ordering tokens.
pub, _ := c.Publish(ctx, semantik.PublishRequest{
	Items:          []semantik.PublishItem{{Text: "Warnings of GPU supply constraints"}},
	Metadata:       map[string]string{"topic": "hardware"},
	IdempotencyKey: "newswire-0001", // a retry can never duplicate
	Ack:            semantik.AckDurable,
})
fmt.Println(pub.MessageID, pub.Epoch, pub.Seq)

// Search. Use the response: ranked matches with score + metadata.
res, _ := c.Search(ctx, semantik.SearchRequest{
	Query: `MATCH DISTANCE("gpu shortage") WITHIN 0.5 LIMIT 10`,
})
for _, m := range res.Results {
	fmt.Println(m.MessageID, m.Score, m.Metadata["topic"], m.Content)
}

// Subscribe. Use the response: stream matches as they arrive.
sub, _ := c.Subscribe(ctx, semantik.SubscribeRequest{
	Query: `MATCH DISTANCE("gpu shortage") WITHIN 0.5`,
})
defer sub.Close()
fmt.Println(sub.ID())
for ev := range sub.Events(ctx) {
	fmt.Println(ev.MessageID, ev.Score)
}
```

For error handling, retry tuning and runnable single-purpose programs,
see the [Semantik package README](semantik/README.md).

See [SECURITY.md](SECURITY.md) for the disclosure policy.
