package bud

import "strings"

// The closed vocabularies, and the banner.
//
// Every value here is one the server chose and will keep choosing. They are
// constants rather than documentation because a caller branches on them, and a
// string literal at a branch is a typo nothing catches.

// The send states. State is the field to read on a SendOutput: a new send is
// StateQueued, and a repeat with the same idempotency key reports any of these.
const (
	// StateQueued means the message is durably queued and not yet handed on.
	StateQueued = "queued"

	// StateSending means a worker is handing it on.
	StateSending = "sending"

	// StateSent means the provider accepted it.
	StateSent = "sent"

	// StateDelivered means the receiving side accepted it.
	StateDelivered = "delivered"

	// StateBounced means the receiving side refused it for good.
	StateBounced = "bounced"

	// StateFailed means it could not be handed on. Reason says why.
	StateFailed = "failed"

	// StateHeld and StateRejected appear only on messages from before holds
	// were removed. Nothing is held now.
	StateHeld     = "held"
	StateRejected = "rejected"
)

// The event types, which JournalEvent.Type names and WaitInput.Types filters on.
const (
	// EventReceived is mail arriving; EventQuarantined is mail arriving and
	// filed in quarantine.
	EventReceived    = "mail.received"
	EventQuarantined = "mail.quarantined"

	// These follow a send: accepted, handed to the provider, accepted by the
	// receiving side, refused for good, reported as unwanted.
	EventQueued     = "mail.queued"
	EventSent       = "mail.sent"
	EventDelivered  = "mail.delivered"
	EventBounced    = "mail.bounced"
	EventComplained = "mail.complained"

	// EventRead is a message marked read or unread.
	EventRead = "mail.read"

	// EventRenderFailed means a rendering was withheld as unsafe and a degraded
	// one shown instead.
	EventRenderFailed = "render.failed"
)

// The actor kinds, which Actor.Kind names.
const (
	ActorSystem   = "system"
	ActorAgent    = "agent"
	ActorOperator = "operator"
)

// The folders, which In.Folder names. FolderInbox is the default.
const (
	FolderInbox      = "inbox"
	FolderSent       = "sent"
	FolderQuarantine = "quarantine"
)

// How a message joined its conversation, which Provenance.ThreadJoin names.
const (
	// JoinNew means it started one.
	JoinNew = "new"

	// JoinVerified means it is a reply from someone the conversation already
	// involves.
	JoinVerified = "verified"

	// JoinClaimed means it only claims to be a reply.
	JoinClaimed = "claimed"
)

// The sending statuses, which SendingStatus.Status names.
const (
	// SendingReady means mail is handed to the transport on the next pass.
	SendingReady = "ready"

	// SendingProvisioning means the sending domain is not ready yet. Mail is
	// accepted and queued, and goes out once it is.
	SendingProvisioning = "provisioning"

	// SendingPaused means an operator stopped sending. A send is refused until
	// it is released.
	SendingPaused = "paused"
)

// The modes DescribePart reads a part in.
const (
	// PartModeText renders a part that is already text into ReadOutput.Text.
	// The default. Any other type is refused as unavailable in this mode.
	PartModeText = "text"

	// PartModeBytes returns the part's content in PartView.Bytes. A part larger
	// than the server's cap is refused with the guard that fired.
	PartModeBytes = "bytes"
)

// The renderings DescribeMessage offers, which ByID.Render names.
const (
	// RenderText is the safe rendering, under a banner. The default.
	RenderText = "text"

	// RenderRaw is the original message as it arrived.
	RenderRaw = "raw"

	// RenderParts lists the message's structure, with the part numbers
	// DescribePart takes.
	RenderParts = "parts"
)

// The object kinds a read can return, which is also what Kind names.
const (
	KindMe      = "me"
	KindHelp    = "help"
	KindMailbox = "mailbox"
	KindFolder  = "folder"
	KindCorr    = "corr"
	KindMessage = "message"
	KindPart    = "part"
	KindThread  = "thread"
)

// The identifier prefixes.
//
// An identifier is self-describing: the prefix names the kind, so one opaque
// string is the only argument a read or a write needs, and a message identifier
// can never be mistaken for a mailbox one. Exposed so a caller can check a string
// it was given before spending a round trip on it.
const (
	PrefixTenant = "tenant_"

	// PrefixAgent is an agent's identifier, and its mailbox's: an agent and the
	// mailbox it reads and sends from are one identifier, not two, so there is
	// one prefix rather than a pair that could disagree.
	PrefixAgent   = "ag_"
	PrefixMessage = "message_"
	PrefixThread  = "thread_"
	PrefixJournal = "journal_"
	PrefixRequest = "request_"
)

// The effect kinds a write reports.
const (
	// EffectMailRead is a message marked read or unread.
	EffectMailRead = "mail.read"

	// EffectLabelled is a message whose labels were set.
	EffectLabelled = "labelled"
)

// BannerPrefix begins the rendering of anything carrying content somebody else
// wrote.
//
// The full line names the object and then says, in the server's own voice, that
// what follows is data rather than instructions. It exists because the one thing
// a mail server cannot do is stop a message containing "ignore your previous
// instructions" — so the defence is to make the provenance impossible to miss
// rather than to pretend the sentence is not there.
const BannerPrefix = "[bud message "

// HasBanner reports whether a rendering carries its provenance banner.
//
// Worth checking rather than assuming. The banner is what makes [ReadOutput.Text]
// safe to put in a model's context, and a relay that passed the text through
// without it would be handing a stranger's words to a model with nothing marking
// them as a stranger's. A caller that renders Text into a prompt should gate on
// this and fall back to the structured content, which cannot be mistaken for the
// server talking.
func HasBanner(text string) bool {
	return strings.HasPrefix(text, BannerPrefix)
}

// CarriesSenderContent reports whether a kind's rendering can contain text
// somebody else wrote, and therefore must carry a banner.
//
// A listing of subjects does not: it is the server's own table, and its cells are
// escaped into structure rather than prose. A message, a thread and a message part
// do.
func CarriesSenderContent(kind string) bool {
	switch kind {
	case KindMessage, KindThread, KindPart:
		return true
	default:
		return false
	}
}
