# noetive-sdk-go

Go SDKs for [Noetive](https://noetive.io) services. Each service has its
own subpackage with its own godoc and examples.

| Service | Import path | Docs |
|---|---|---|
| Semantik — semantic search, publish, subscribe | `go.noetive.io/noetive-sdk-go/semantik` | [godoc](https://pkg.go.dev/go.noetive.io/noetive-sdk-go/semantik) · [README](semantik/README.md) |
| Bud — mail for agents: read, watch, send | `go.noetive.io/noetive-sdk-go/bud` | [godoc](https://pkg.go.dev/go.noetive.io/noetive-sdk-go/bud) |

Requires Go 1.25.2 or newer.

```
go get go.noetive.io/noetive-sdk-go/semantik
go get go.noetive.io/noetive-sdk-go/bud
```

One Noetive key reaches every service: both clients read it from
`NOETIVE_KEY_SECRET`.

## Examples

<details>
<summary><strong>Semantik</strong> — publish, search and stream live matches</summary>

Init a client, then publish, search, and stream live matches — using
the response from each call. Every request names its namespace, model
and dimensions; nothing is defaulted, so data never lands somewhere you
did not choose.

```go
import "go.noetive.io/noetive-sdk-go/semantik"

ctx := context.Background()
c, _ := semantik.NewFromEnv() // reads NOETIVE_KEY_SECRET

const (
	ns    = "global" // every account has it
	model = "Qwen3-Embedding-4B"
	dims  = 1024
)

// Publish. Use the response: server-assigned ID + ordering tokens.
pub, _ := c.Publish(ctx, semantik.PublishRequest{
	Namespace: ns, Model: model, Dimensions: dims,
	Items:          []semantik.PublishItem{{Text: "Warnings of GPU supply constraints"}},
	Metadata:       map[string]string{"topic": "hardware"},
	IdempotencyKey: "newswire-0001", // a retry can never duplicate
	Ack:            semantik.AckDurable,
})
fmt.Println(pub.MessageID, pub.Epoch, pub.Seq)

// Search. Use the response: ranked matches with score + metadata.
res, _ := c.Search(ctx, semantik.SearchRequest{
	Namespace: ns, Model: model, Dimensions: dims,
	Query: `MATCH DISTANCE("gpu shortage") WITHIN 0.5 LIMIT 10`,
})
for _, m := range res.Results {
	fmt.Println(m.MessageID, m.Score, m.Metadata["topic"], m.Content)
}

// Subscribe. Use the response: stream matches as they arrive.
sub, _ := c.Subscribe(ctx, semantik.SubscribeRequest{
	Namespace: ns, Model: model, Dimensions: dims,
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

</details>

<details>
<summary><strong>Bud</strong> — wait for mail and answer it</summary>

Find out who the agent is, then wait for mail and answer it. A refusal
comes back as a value on the response, carrying its code and what to
do next; only a failed connection is a Go error.

```go
import (
	"slices"

	"go.noetive.io/noetive-sdk-go/bud"
)

ctx := context.Background()
c, _ := bud.NewFromEnv() // reads NOETIVE_KEY_SECRET

// Who am I? Every identifier starts here.
out, _ := c.DescribeMe(ctx)
var me bud.Me
_ = out.Into(&me)
fmt.Println(me.Agent, me.Addresses)

// Wait for mail and answer it.
cursor := ""
for {
	w, err := c.Wait(ctx, bud.WaitInput{Cursor: cursor})
	if err != nil {
		log.Fatal(err) // the connection failed; nothing to branch on
	}
	if w.Error != nil {
		log.Fatal(w.Error) // Code says what happened, Hint what to do next
	}
	cursor = w.Cursor // an empty window is success: keep waiting from here

	for _, ev := range w.Events {
		if ev.Type != bud.EventReceived || slices.Contains(me.Addresses, ev.Data.From) {
			continue // only new mail, and never our own
		}
		msg, _ := c.DescribeMessage(ctx, bud.ByID{ID: ev.Message})
		p := msg.Provenance
		if p == nil || !p.Aligned || p.ThreadJoin != bud.JoinNew {
			continue // unverified, or a reply: read it, do not answer it
		}
		sent, err := c.Send(ctx, bud.SendInput{
			InReplyTo:      ev.Message,
			Text:           "Got it, thanks.",
			IdempotencyKey: "reply-" + ev.ID, // a retried send goes out once
		})
		if err == nil && sent.Error != nil {
			log.Print(sent.Error) // refused: the guard or limit says why
		}
	}
}
```

A message's rendered text marks everything the sender wrote as data,
not instructions; check `Provenance` before acting on what it says. For
a long-running consumer, `Watch` holds one stream open instead of
waiting window by window. The [godoc](https://pkg.go.dev/go.noetive.io/noetive-sdk-go/bud)
covers every operation, the typed views `ReadOutput.Into` decodes, and
which refusals are worth retrying.

</details>

See [SECURITY.md](SECURITY.md) for the disclosure policy.

## License

Apache License 2.0. See [`LICENSE`](LICENSE).
