package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

// memStore is an in-memory ChatDataStore for tests.
type memStore struct {
	data    map[string]*WaChatData
	getErr  error
	saveErr error
	saves   int
}

func newMemStore() *memStore {
	return &memStore{data: map[string]*WaChatData{}}
}

func (s *memStore) GetChatData(_ context.Context, botID, chatID string) (*WaChatData, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if d, ok := s.data[botID+"/"+chatID]; ok {
		return d, nil
	}
	return nil, nil // a missing chat is not an error
}

func (s *memStore) SaveChatData(_ context.Context, botID, chatID string, d *WaChatData) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.saves++
	s.data[botID+"/"+chatID] = d
	return nil
}

func payloadWith(msgs ...InboundMessage) WebhookPayload {
	return WebhookPayload{Entry: []WebhookEntry{{
		Changes: []WebhookChange{{Value: WebhookValue{Messages: msgs}}},
	}}}
}

func TestProcessInbound_recordsAndAdvancesWindow(t *testing.T) {
	store := newMemStore()
	ctx := context.Background()
	at := time.Unix(1749416383, 0).UTC()

	p := payloadWith(InboundMessage{From: "16505551234", ID: "wamid.A", Timestamp: "1749416383", Type: "text"})
	res, err := ProcessInbound(ctx, store, "bot1", p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.New) != 1 || len(res.Duplicates) != 0 {
		t.Fatalf("new=%d dup=%d, want 1/0", len(res.New), len(res.Duplicates))
	}
	got := store.data["bot1/16505551234"]
	if !got.DtLastInbound.Equal(at) {
		t.Errorf("DtLastInbound = %v, want %v", got.DtLastInbound, at)
	}
}

// TestProcessInbound_dedup is the requirement Meta states outright: retries span 36
// hours and "your server should handle deduplication in these cases".
func TestProcessInbound_dedup(t *testing.T) {
	store := newMemStore()
	ctx := context.Background()
	m := InboundMessage{From: "16505551234", ID: "wamid.A", Timestamp: "1749416383", Type: "text"}

	first, err := ProcessInbound(ctx, store, "bot1", payloadWith(m))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(first.New) != 1 {
		t.Fatalf("first delivery must be new, got %d", len(first.New))
	}

	// Meta retries the identical payload.
	second, err := ProcessInbound(ctx, store, "bot1", payloadWith(m))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(second.New) != 0 {
		t.Errorf("a retried delivery must not be processed twice, got %d new", len(second.New))
	}
	if len(second.Duplicates) != 1 {
		t.Errorf("the duplicate must be reported, got %d", len(second.Duplicates))
	}
}

// TestProcessInbound_statusesDoNotAdvanceWindow is the load-bearing one.
//
// A status webhook is the bot's OWN outbound message coming back. If it counted as
// user activity the window would never close, every proactive send would be
// attempted, and every one would fail with 131047.
func TestProcessInbound_statusesDoNotAdvanceWindow(t *testing.T) {
	store := newMemStore()
	ctx := context.Background()

	p := WebhookPayload{Entry: []WebhookEntry{{Changes: []WebhookChange{{
		Value: WebhookValue{Statuses: []MessageStatus{
			{ID: "wamid.OUT", Status: "delivered", Timestamp: "1750263773", RecipientID: "16505551234"},
		}},
	}}}}}

	res, err := ProcessInbound(ctx, store, "bot1", p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.New) != 0 {
		t.Errorf("a status must never count as inbound, got %d", len(res.New))
	}
	if len(res.Statuses) != 1 {
		t.Errorf("the status should be surfaced, got %d", len(res.Statuses))
	}
	if store.saves != 0 {
		t.Error("a status webhook must not touch chat data — the window must not advance")
	}
}

// TestProcessInbound_batchGroupsByChat pins that a batched payload touches each
// chat's record once. Meta batches up to 1000 updates.
func TestProcessInbound_batchGroupsByChat(t *testing.T) {
	store := newMemStore()
	ctx := context.Background()

	p := payloadWith(
		InboundMessage{From: "111", ID: "a", Timestamp: "1749416383"},
		InboundMessage{From: "222", ID: "b", Timestamp: "1749416384"},
		InboundMessage{From: "111", ID: "c", Timestamp: "1749416385"},
	)
	res, err := ProcessInbound(ctx, store, "bot1", p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.New) != 3 {
		t.Errorf("got %d new, want 3", len(res.New))
	}
	if store.saves != 2 {
		t.Errorf("got %d saves, want 1 per chat (2)", store.saves)
	}
	// The later of chat 111's two messages must win.
	if want := time.Unix(1749416385, 0).UTC(); !store.data["bot1/111"].DtLastInbound.Equal(want) {
		t.Errorf("DtLastInbound = %v, want the latest %v", store.data["bot1/111"].DtLastInbound, want)
	}
}

// TestRecordInbound_neverRewindsWindow pins that a late-arriving older message
// cannot move DtLastInbound backwards. Meta does not guarantee batch ordering, and
// rewinding would lock the bot out of a conversation that is actually open.
func TestRecordInbound_neverRewindsWindow(t *testing.T) {
	d := &WaChatData{}
	newer := time.Unix(1749416385, 0).UTC()
	older := time.Unix(1749416000, 0).UTC()

	d.RecordInbound("wamid.NEW", newer)
	d.RecordInbound("wamid.OLD", older)

	if !d.DtLastInbound.Equal(newer) {
		t.Errorf("DtLastInbound = %v, want it to stay at the newer %v", d.DtLastInbound, newer)
	}
}

// TestRecordInbound_dedupRingIsBounded pins that the dedup set cannot grow forever
// for a chatty user.
func TestRecordInbound_dedupRingIsBounded(t *testing.T) {
	d := &WaChatData{}
	for i := 0; i < MaxDedupIDs*3; i++ {
		d.RecordInbound("wamid."+string(rune('a'+i%26))+string(rune('a'+i/26)), time.Unix(int64(1749416383+i), 0))
	}
	if len(d.RecentInboundIDs) > MaxDedupIDs {
		t.Errorf("dedup ring grew to %d, max is %d", len(d.RecentInboundIDs), MaxDedupIDs)
	}
	// The most recent must still be recognised.
	last := d.RecentInboundIDs[0]
	if !d.IsInboundSeen(last) {
		t.Error("the newest id must remain in the ring")
	}
}

// TestProcessInbound_saveFailureFailsTheWebhook pins that a save failure surfaces
// rather than being swallowed. Meta will retry, and dedup makes that safe — whereas
// proceeding would leave the window un-advanced and refuse later valid sends.
func TestProcessInbound_saveFailureFailsTheWebhook(t *testing.T) {
	store := newMemStore()
	store.saveErr = errors.New("datastore unavailable")

	_, err := ProcessInbound(context.Background(), store, "bot1",
		payloadWith(InboundMessage{From: "111", ID: "a", Timestamp: "1749416383"}))
	if err == nil {
		t.Fatal("a save failure must fail the webhook so Meta retries")
	}
}

func TestProcessInbound_getFailureSurfaces(t *testing.T) {
	store := newMemStore()
	store.getErr = errors.New("datastore unavailable")

	_, err := ProcessInbound(context.Background(), store, "bot1",
		payloadWith(InboundMessage{From: "111", ID: "a", Timestamp: "1749416383"}))
	if err == nil {
		t.Fatal("a load failure must surface")
	}
}

// TestStoreLastInboundProvider_feedsTheWindowGate pins the whole loop: an inbound
// message recorded here must make the gate permit a free-form reply.
func TestStoreLastInboundProvider_feedsTheWindowGate(t *testing.T) {
	store := newMemStore()
	ctx := context.Background()

	// Nothing recorded yet: the user has never written, so only a template may go.
	p := NewStoreLastInboundProvider(store, "bot1")
	at, err := p.LastInboundAt(ctx, "16505551234")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !at.IsZero() {
		t.Errorf("an unknown chat must report the zero time, got %v", at)
	}

	// The user writes.
	now := time.Now().UTC().Truncate(time.Second)
	if _, err = ProcessInbound(ctx, store, "bot1", payloadWith(InboundMessage{
		From: "16505551234", ID: "wamid.A",
		Timestamp: itoa(now.Unix()), Type: "text",
	})); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The window is now open.
	if at, err = p.LastInboundAt(ctx, "16505551234"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !IsWithinWindow(at, time.Now()) {
		t.Errorf("the window must be open right after the user wrote, lastInbound=%v", at)
	}
}

// TestProcessInbound_realMetaPayload pins the loop against Meta's verbatim example.
func TestProcessInbound_realMetaPayload(t *testing.T) {
	var p WebhookPayload
	if err := json.Unmarshal([]byte(metaInboundTextWebhook), &p); err != nil {
		t.Fatalf("failed to parse Meta's example: %v", err)
	}
	store := newMemStore()
	res, err := ProcessInbound(context.Background(), store, "bot1", p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.New) != 1 {
		t.Fatalf("got %d new, want 1", len(res.New))
	}
	if res.New[0].Text == nil || res.New[0].Text.Body != "Does it come in another color?" {
		t.Errorf("message body not preserved: %+v", res.New[0].Text)
	}
	if store.data["bot1/16505551234"].DtLastInbound.IsZero() {
		t.Error("the window must have advanced")
	}
}

func itoa(i int64) string {
	return fmt.Sprintf("%d", i)
}
