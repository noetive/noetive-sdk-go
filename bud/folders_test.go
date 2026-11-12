package bud_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"go.noetive.io/noetive-sdk-go/bud"
)

// recorded is one request a fake server saw.
type recorded struct {
	path string
	body map[string]any
}

// recorder answers every request with reply and keeps what it was sent.
func recorder(t *testing.T, reply string) (*httptest.Server, func() []recorded) {
	t.Helper()
	var (
		mu   sync.Mutex
		seen []recorded
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("%s sent a body that is not a JSON object: %q", r.URL.Path, raw)
		}
		mu.Lock()
		seen = append(seen, recorded{path: r.URL.Path, body: body})
		mu.Unlock()
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []recorded {
		mu.Lock()
		defer mu.Unlock()
		return append([]recorded(nil), seen...)
	}
}

// TestFoldersAreMadeAndRemovedByOperation checks each folder call reaches its
// own operation with the mailbox and folder named as the document describes,
// and that the effect the server reports comes back to the caller.
func TestFoldersAreMadeAndRemovedByOperation(t *testing.T) {
	t.Parallel()
	srv, seen := recorder(t, `{"ref":"ag_01x/receipts","effects":[{"kind":"folder.created","count":1}]}`)
	c := mustClient(t, srv.URL)

	out, err := c.CreateFolder(t.Context(), bud.Folder{In: "ag_01x", Folder: "receipts", IdempotencyKey: "k1"})
	if err != nil || out.Error != nil {
		t.Fatalf("CreateFolder = %+v, %v", out.Error, err)
	}
	if len(out.Effects) != 1 || out.Effects[0].Kind != bud.EffectFolderCreated {
		t.Errorf("effects = %+v, want one folder.created", out.Effects)
	}
	if _, err := c.DeleteFolder(t.Context(), bud.Folder{In: "ag_01x", Folder: "receipts"}); err != nil {
		t.Fatalf("DeleteFolder: %v", err)
	}

	got := seen()
	if len(got) != 2 {
		t.Fatalf("the server saw %d requests, want 2", len(got))
	}
	for i, want := range []string{"/v1/folder.create", "/v1/folder.delete"} {
		if got[i].path != want {
			t.Errorf("request %d went to %s, want %s", i, got[i].path, want)
		}
		if got[i].body["in"] != "ag_01x" || got[i].body["folder"] != "receipts" {
			t.Errorf("request %d carried %v, want the mailbox and folder", i, got[i].body)
		}
	}
	if got[0].body["idempotency_key"] != "k1" {
		t.Errorf("the idempotency key was not sent: %v", got[0].body)
	}
}

// TestAMoveIsAnUpdateWithAFolder checks MoveMessage sends exactly the change the
// server documents for a move, and nothing that would also mark or label it.
func TestAMoveIsAnUpdateWithAFolder(t *testing.T) {
	t.Parallel()
	srv, seen := recorder(t, `{"ref":"message_01x","effects":[{"kind":"moved","count":1}]}`)
	c := mustClient(t, srv.URL)

	out, err := c.MoveMessage(t.Context(), bud.Move{ID: "message_01x", Folder: "receipts"})
	if err != nil || out.Error != nil {
		t.Fatalf("MoveMessage = %+v, %v", out.Error, err)
	}
	if len(out.Effects) != 1 || out.Effects[0].Kind != bud.EffectMoved {
		t.Errorf("effects = %+v, want one moved", out.Effects)
	}

	got := seen()
	if len(got) != 1 || got[0].path != "/v1/message.update" {
		t.Fatalf("requests = %+v, want one message.update", got)
	}
	changes, ok := got[0].body["changes"].(map[string]any)
	if !ok || len(changes) != 1 || changes["folder"] != "receipts" {
		t.Errorf("changes = %v, want only the folder", got[0].body["changes"])
	}
	if got[0].body["id"] != "message_01x" {
		t.Errorf("id = %v", got[0].body["id"])
	}
}

// TestFolderCallsAreCheckedBeforeSending checks the refusals this package can
// make without a round trip: a mailbox of the wrong kind, or no folder at all.
func TestFolderCallsAreCheckedBeforeSending(t *testing.T) {
	t.Parallel()
	srv, seen := recorder(t, `{}`)
	c := mustClient(t, srv.URL)
	ctx := t.Context()

	if _, err := c.CreateFolder(ctx, bud.Folder{In: "message_01x", Folder: "receipts"}); err == nil {
		t.Error("CreateFolder with a message id was sent")
	}
	if _, err := c.DeleteFolder(ctx, bud.Folder{In: "ag_01x"}); err == nil {
		t.Error("DeleteFolder with no folder was sent")
	}
	if _, err := c.MoveMessage(ctx, bud.Move{ID: "message_01x"}); err == nil {
		t.Error("MoveMessage with no folder was sent")
	}
	if _, err := c.MoveMessage(ctx, bud.Move{ID: "thread_01x", Folder: "receipts"}); err == nil {
		t.Error("MoveMessage with a thread id was sent")
	}
	if n := len(seen()); n != 0 {
		t.Errorf("%d refused calls reached the server", n)
	}
}
