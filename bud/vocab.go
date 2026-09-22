package bud

import "strings"

// The closed vocabularies, and the banner.
//
// Every value here is one the server chose and will keep choosing. They are
// constants rather than documentation because a caller branches on them, and a
// string literal at a branch is a typo nothing catches.

// The send states. State is the field to read on a SendOutput.
const (
	// StateQueued means the message is on its way out.
	StateQueued = "queued"

	// StateSent means the transport accepted it.
	StateSent = "sent"

	// StateHeld means a person has to approve it. Not a failure and not an
	// error: it is waiting, and sending again sends a second copy.
	StateHeld = "held"
)

// The object kinds a read can return, which is also what Kind names.
const (
	KindMe           = "me"
	KindHelp         = "help"
	KindMailbox      = "mailbox"
	KindFolder       = "folder"
	KindCorr         = "corr"
	KindMessage      = "message"
	KindPart         = "part"
	KindThread       = "thread"
	KindDraft        = "draft"
	KindHold         = "hold"
	KindCalendar     = "calendar"
	KindMonth        = "month"
	KindEvent        = "event"
	KindFreebusy     = "freebusy"
	KindBook         = "book"
	KindContact      = "contact"
	KindBlob         = "blob"
	KindSearch       = "search"
	KindCatalog      = "catalog"
	KindConversation = "thread"
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
	PrefixAgent    = "ag_"
	PrefixMessage  = "message_"
	PrefixThread   = "thread_"
	PrefixDraft    = "draft_"
	PrefixCalendar = "calendar_"
	PrefixBook     = "book_"
	PrefixContact  = "contact_"
	PrefixBlob     = "blob_"
	PrefixJournal  = "journal_"
	PrefixRequest  = "request_"
)

// The effect kinds a write reports.
const (
	EffectMailQueued   = "mail.queued"
	EffectMailHeld     = "mail.held"
	EffectCalInvited   = "cal.invited"
	EffectCalCancelled = "cal.cancelled"
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
	case KindMessage, KindThread, KindPart, KindHold:
		return true
	default:
		return false
	}
}
