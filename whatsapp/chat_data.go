package whatsapp

import (
	"context"
	"time"

	"github.com/bots-go-framework/bots-fw-store/botsfwmodels"
)

// MaxDedupIDs is how many recent inbound message IDs are retained per chat for
// deduplication.
//
// Meta retries a failed delivery "immediately, then a few more times with
// decreasing frequency over the next 36 hours", and batches up to 1000 updates,
// so duplicates arrive close together rather than spread out. A small ring is
// enough; an unbounded set would grow forever for a chatty user.
const MaxDedupIDs = 20

// WaChatData is WhatsApp-specific chat data.
//
// It embeds botsfwmodels.ChatBaseData like bots-fw-telegram's TgChatBaseData does,
// and adds the two things WhatsApp needs and bots-fw does not provide.
type WaChatData struct {
	botsfwmodels.ChatBaseData

	// DtLastInbound is when the WhatsApp user last sent a message in this chat.
	//
	// This exists because ChatBaseData.DtLastInteraction cannot be used: the router
	// only stamps it when chat data changed, so a reply that mutates nothing leaves
	// it stale. The 24h customer service window would then refuse permitted sends.
	//
	// Stamped on EVERY inbound user message, unconditionally.
	DtLastInbound time.Time `firestore:"dtLastInbound,omitempty"`

	// RecentInboundIDs holds the most recent inbound wamids, newest first, for
	// deduplication. Capped at MaxDedupIDs.
	//
	// Meta states plainly: "Your server should handle deduplication in these
	// cases." bots-fw declares IsNewerThen/UpdateLastProcessed for this and
	// implements neither — Telegram's version returns true unconditionally and has
	// zero callers — so the adapter must do it.
	//
	// Telegram's update_id counter model does not port: wamids are opaque strings
	// with no ordering, so "is this newer" is unanswerable. Membership is the only
	// available test.
	RecentInboundIDs []string `firestore:"recentInboundIDs,omitempty"`
}

var _ botsfwmodels.BotChatData = (*WaChatData)(nil)

// Base implements botsfwmodels.BotChatData.
func (d *WaChatData) Base() *botsfwmodels.ChatBaseData {
	return &d.ChatBaseData
}

// IsInboundSeen reports whether messageID has already been processed.
func (d *WaChatData) IsInboundSeen(messageID string) bool {
	for _, id := range d.RecentInboundIDs {
		if id == messageID {
			return true
		}
	}
	return false
}

// RecordInbound marks messageID as processed and advances DtLastInbound.
//
// Returns false if the message was a duplicate, in which case nothing is changed
// and the caller must not process it again.
//
// sentAt only moves DtLastInbound forward. Meta batches updates and does not
// guarantee ordering, so an older message arriving late must not rewind the
// window and lock the bot out of a conversation that is in fact open.
func (d *WaChatData) RecordInbound(messageID string, sentAt time.Time) (isNew bool) {
	if messageID != "" && d.IsInboundSeen(messageID) {
		return false
	}
	if messageID != "" {
		d.RecentInboundIDs = append([]string{messageID}, d.RecentInboundIDs...)
		if len(d.RecentInboundIDs) > MaxDedupIDs {
			d.RecentInboundIDs = d.RecentInboundIDs[:MaxDedupIDs]
		}
	}
	if sentAt.After(d.DtLastInbound) {
		d.DtLastInbound = sentAt
	}
	return true
}

// ChatDataStore is the persistence this adapter needs.
//
// Deliberately narrow rather than reusing botsfw.BotChatStore: the window and
// dedup need exactly these two operations, and a small interface is trivial to
// implement over dalgo, over a test double, or over anything else.
type ChatDataStore interface {
	// GetChatData returns the chat's data, or a zero-valued WaChatData if the chat
	// is not yet known. A missing chat is not an error.
	GetChatData(ctx context.Context, botID, chatID string) (*WaChatData, error)

	// SaveChatData persists the chat's data.
	SaveChatData(ctx context.Context, botID, chatID string, data *WaChatData) error
}

// storeLastInbound implements LastInboundProvider over a ChatDataStore.
type storeLastInbound struct {
	store ChatDataStore
	botID string
}

var _ LastInboundProvider = (*storeLastInbound)(nil)

// NewStoreLastInboundProvider returns a LastInboundProvider backed by store.
//
// This is what makes the window gate usable: NewWindowGate needs a provider, and
// without one the adapter cannot tell an open conversation from a closed one.
func NewStoreLastInboundProvider(store ChatDataStore, botID string) LastInboundProvider {
	if store == nil {
		panic("store must not be nil")
	}
	return &storeLastInbound{store: store, botID: botID}
}

// LastInboundAt implements LastInboundProvider.
func (p *storeLastInbound) LastInboundAt(ctx context.Context, chatID string) (time.Time, error) {
	data, err := p.store.GetChatData(ctx, p.botID, chatID)
	if err != nil {
		return time.Time{}, err
	}
	if data == nil {
		return time.Time{}, nil
	}
	return data.DtLastInbound, nil
}
