//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.noetive.io/noetive-sdk-go/bud"
)

// TestAnUnknownKeyIsRefusedAsAValue needs no valid key: the refusal is the
// answer, carried on the output with its status and request id.
func TestAnUnknownKeyIsRefusedAsAValue(t *testing.T) {
	key(t)
	c, err := bud.New("keya_not_a_real_key_0000000000000000", bud.WithBaseURL(ProdBaseURL))
	if err != nil {
		t.Fatalf("bud.New: %v", err)
	}
	h, err := c.Health(callContext(t))
	refused(t, "Health", err, h.Error, bud.CodeUnauthorized)
	if h.Error.HTTPStatus != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", h.Error.HTTPStatus)
	}
}

// TestHealthNamesTheAgent: the probe says the key works, which agent it is,
// and which contract the service speaks.
func TestHealthNamesTheAgent(t *testing.T) {
	c, me := setup(t)
	h, err := c.Health(callContext(t))
	answered(t, "Health", err, h.Error)
	if h.Contract != bud.ContractVersion {
		t.Errorf("contract %q, this client implements %q", h.Contract, bud.ContractVersion)
	}
	if h.Agent != me.Agent || h.Tenant != me.Tenant {
		t.Errorf("health names %s in %s, me.describe %s in %s", h.Agent, h.Tenant, me.Agent, me.Tenant)
	}
}

// TestTheCatalogServesEveryOperationThisClientCalls catches an operation this
// client relies on that the deployment has stopped serving.
func TestTheCatalogServesEveryOperationThisClientCalls(t *testing.T) {
	c, _ := setup(t)
	cat, err := c.DescribeCatalog(callContext(t))
	answered(t, "DescribeCatalog", err, cat.Error)
	for _, op := range []string{
		"me.describe", "health", "catalog.describe", "folder.list", "message.describe",
		"thread.describe", "part.describe", "mailbox.describe", "correspondent.list",
		"message.update", "send", "watch",
	} {
		if !cat.Served(op) {
			t.Errorf("the catalog does not serve %q", op)
		}
	}
}

// TestTheMailboxDescribesItself reads the mailbox view and its sending status.
func TestTheMailboxDescribesItself(t *testing.T) {
	c, me := setup(t)
	out, err := c.DescribeMailbox(callContext(t), bud.ByID{ID: me.Mailbox})
	answered(t, "DescribeMailbox", err, out.Error)
	var mv bud.MailboxView
	if err := out.Into(&mv); err != nil {
		t.Fatalf("Into(MailboxView): %v", err)
	}
	if mv.Mailbox != me.Mailbox {
		t.Errorf("mailbox %q, want %q", mv.Mailbox, me.Mailbox)
	}
	if mv.Sending != nil {
		switch mv.Sending.Status {
		case bud.SendingReady, bud.SendingProvisioning, bud.SendingPaused:
		default:
			t.Errorf("sending status %q is not one this client knows", mv.Sending.Status)
		}
	}
}

// TestEveryFolderLists pages each folder once and checks the listing decodes.
func TestEveryFolderLists(t *testing.T) {
	c, me := setup(t)
	for _, folder := range []string{bud.FolderInbox, bud.FolderSent, bud.FolderQuarantine} {
		out, err := c.ListFolder(callContext(t), bud.In{In: me.Mailbox, Folder: folder, Limit: 5})
		answered(t, "ListFolder "+folder, err, out.Error)
		var l bud.Listing
		if err := out.Into(&l); err != nil {
			t.Fatalf("Into(Listing) %s: %v", folder, err)
		}
		for _, s := range l.Messages {
			if !strings.HasPrefix(s.Message, bud.PrefixMessage) {
				t.Errorf("%s lists %q, want a message identifier", folder, s.Message)
			}
		}
		if out.Cursor == "" {
			continue
		}
		next, err := c.ListFolder(callContext(t), bud.In{In: me.Mailbox, Folder: folder, Limit: 5, Cursor: out.Cursor})
		answered(t, "ListFolder "+folder+" page 2", err, next.Error)
	}
}

// TestCorrespondentsList reads the correspondents view.
func TestCorrespondentsList(t *testing.T) {
	c, me := setup(t)
	out, err := c.ListCorrespondents(callContext(t), bud.In{In: me.Mailbox, Limit: 5})
	answered(t, "ListCorrespondents", err, out.Error)
	var cv bud.Correspondents
	if err := out.Into(&cv); err != nil {
		t.Fatalf("Into(Correspondents): %v", err)
	}
	for _, cr := range cv.Correspondents {
		if cr.Addr == "" || cr.FirstSeen.IsZero() {
			t.Errorf("correspondent %+v is missing its address or first sighting", cr)
		}
	}
}

// TestAMessageReadsEveryWay reads one stored message as text, as parts, part
// by part, and its thread. It skips when the mailbox holds no mail.
func TestAMessageReadsEveryWay(t *testing.T) {
	c, me := setup(t)
	s := anyMessage(t, c, me)

	text, err := c.DescribeMessage(callContext(t), bud.ByID{ID: s.Message})
	answered(t, "DescribeMessage", err, text.Error)
	if !bud.HasBanner(text.Text) {
		t.Errorf("the rendering does not begin with its banner: %.60q", text.Text)
	}
	if text.Provenance == nil {
		t.Error("a message carries no provenance")
	}

	parts, err := c.DescribeMessage(callContext(t), bud.ByID{ID: s.Message, Render: bud.RenderParts})
	answered(t, "DescribeMessage parts", err, parts.Error)
	var pm bud.PartMap
	if err := parts.Into(&pm); err != nil {
		t.Fatalf("Into(PartMap): %v", err)
	}
	if len(pm.Parts) == 0 {
		t.Fatal("a stored message lists no parts")
	}
	if n := firstText(pm.Parts); n != "" {
		part, err := c.DescribePart(callContext(t), bud.ByID{ID: s.Message, Part: n, Mode: bud.PartModeBytes})
		answered(t, "DescribePart "+n, err, part.Error)
		var pv bud.PartView
		if err := part.Into(&pv); err != nil {
			t.Fatalf("Into(PartView): %v", err)
		}
		if pv.Part != n || int64(len(pv.Bytes)) != pv.Size {
			t.Errorf("part %s: got part %q with %d bytes of a declared %d", n, pv.Part, len(pv.Bytes), pv.Size)
		}
	}

	if s.Thread != "" {
		thread, err := c.DescribeThread(callContext(t), bud.ByID{ID: s.Thread, MaxMessages: 3})
		answered(t, "DescribeThread", err, thread.Error)
		var conv bud.Conversation
		if err := thread.Into(&conv); err != nil {
			t.Fatalf("Into(Conversation): %v", err)
		}
		if conv.Thread != s.Thread || len(conv.Messages) == 0 {
			t.Errorf("thread %q with %d messages, want %q", conv.Thread, len(conv.Messages), s.Thread)
		}
	}
}

// TestMarkingAMessageAsItIsChangesNothing exercises message.update by setting
// a message's read state to the one it already has.
func TestMarkingAMessageAsItIsChangesNothing(t *testing.T) {
	c, me := setup(t)
	s := anyMessage(t, c, me)

	changes, _ := json.Marshal(map[string]bool{"read": !s.Unread})
	out, err := c.UpdateMessage(callContext(t), bud.Change{ID: s.Message, Changes: changes})
	answered(t, "UpdateMessage", err, out.Error)

	after := findMessage(t, c, me, s.Message)
	if after.Unread != s.Unread {
		t.Errorf("unread went from %v to %v; the update was meant to change nothing", s.Unread, after.Unread)
	}
}

// TestAQuietWaitIsSuccess: a short window returns, with a cursor to resume
// from, and no error whether or not mail arrived.
func TestAQuietWaitIsSuccess(t *testing.T) {
	c, _ := setup(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	start := time.Now()
	out, err := c.Wait(ctx, bud.WaitInput{TimeoutSeconds: 2})
	answered(t, "Wait", err, out.Error)
	if out.Cursor == "" {
		t.Error("Wait returned no cursor to resume from")
	}
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Errorf("a two-second window took %v", elapsed)
	}

	again, err := c.Wait(ctx, bud.WaitInput{Cursor: out.Cursor, TimeoutSeconds: 1})
	answered(t, "Wait from the cursor", err, again.Error)
}

// TestAWatchOpensAtACursor: the stream opens and says where it starts.
func TestAWatchOpensAtACursor(t *testing.T) {
	c, _ := setup(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	st, err := c.Watch(ctx, bud.WaitInput{TimeoutSeconds: 1})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer func() { _ = st.Close() }()
	if st.RequestID() == "" {
		t.Error("the stream carries no request id")
	}
	if st.Cursor() == "" {
		t.Error("the stream opened without saying where it starts")
	}
}

// TestRefusalsCarryWhatToDoNext covers the refusals a caller acts on, none of
// which has an effect.
func TestRefusalsCarryWhatToDoNext(t *testing.T) {
	c, me := setup(t)

	missing, err := c.DescribeMessage(callContext(t), bud.ByID{ID: "message_01aaaaaaaaaaaaaaaaaaaaaaaaaa"})
	refused(t, "DescribeMessage of a message that does not exist", err, missing.Error, bud.CodeNotFound)

	folder, err := c.ListFolder(callContext(t), bud.In{In: me.Mailbox, Folder: "no-such-folder"})
	refused(t, "ListFolder of a folder that does not exist", err, folder.Error, bud.CodeInvalid)

	to := me.Addresses
	if len(to) == 0 {
		t.Skip("the agent has no address to address a refused send to")
	}
	// Refused before anything is queued: an attachment is not served.
	attach, err := c.Send(callContext(t), bud.SendInput{
		To: to[:1], Subject: "integration: refused", Text: "never sent",
		Attach: []string{"message_01aaaaaaaaaaaaaaaaaaaaaaaaaa/part/2"}, IdempotencyKey: "integration-refused-attach",
	})
	refused(t, "Send with an attachment", err, attach.Error, bud.CodeInvalid)
	if attach.Error.Field != "/attach" {
		t.Errorf("field %q, want /attach", attach.Error.Field)
	}
}

// anyMessage returns one stored message, or skips.
func anyMessage(t *testing.T, c *bud.Client, me bud.Me) bud.Summary {
	t.Helper()
	for _, folder := range []string{bud.FolderInbox, bud.FolderSent} {
		out, err := c.ListFolder(callContext(t), bud.In{In: me.Mailbox, Folder: folder, Limit: 20})
		answered(t, "ListFolder "+folder, err, out.Error)
		var l bud.Listing
		if err := out.Into(&l); err != nil {
			t.Fatalf("Into(Listing): %v", err)
		}
		if len(l.Messages) > 0 {
			return l.Messages[0]
		}
	}
	t.Skip("the mailbox holds no mail to read")
	return bud.Summary{}
}

// findMessage returns a message's current summary.
func findMessage(t *testing.T, c *bud.Client, me bud.Me, id string) bud.Summary {
	t.Helper()
	for _, folder := range []string{bud.FolderInbox, bud.FolderSent} {
		cursor := ""
		for page := 0; page < 20; page++ {
			out, err := c.ListFolder(callContext(t), bud.In{In: me.Mailbox, Folder: folder, Limit: 200, Cursor: cursor})
			answered(t, "ListFolder "+folder, err, out.Error)
			var l bud.Listing
			if err := out.Into(&l); err != nil {
				t.Fatalf("Into(Listing): %v", err)
			}
			for _, s := range l.Messages {
				if s.Message == id {
					return s
				}
			}
			if cursor = out.Cursor; cursor == "" {
				break
			}
		}
	}
	t.Fatalf("message %s is no longer listed", id)
	return bud.Summary{}
}

// firstText returns the number of the first leaf text part, or "".
func firstText(parts []bud.PartInfo) string {
	for _, p := range parts {
		if len(p.Children) > 0 {
			if n := firstText(p.Children); n != "" {
				return n
			}
			continue
		}
		if strings.HasPrefix(p.Type, "text/") && p.Size > 0 && p.Size < 1<<20 {
			return p.N
		}
	}
	return ""
}
