package whatsapp

import (
	"context"
	"fmt"
)

// InboundResult reports what a webhook payload yielded.
type InboundResult struct {
	// New are messages not seen before, in payload order. Process these.
	New []InboundMessage

	// Duplicates are messages already processed and skipped.
	Duplicates []InboundMessage

	// Statuses are outbound-message status updates. These do NOT advance the
	// customer service window.
	Statuses []MessageStatus
}

// ProcessInbound records every inbound user message against chat data, advancing
// the customer service window and filtering duplicates.
//
// This is the counterpart to the window gate: the gate asks "when did the user
// last reply?", and this is what answers it. It must run for EVERY inbound webhook,
// regardless of whether any command changes chat data — the mistake bots-fw makes
// with DtLastInteraction.
//
// Statuses are separated, never recorded as inbound. A status webhook reports the
// fate of the business's OWN outbound message (up to three per message: sent,
// delivered, read). Counting them as user activity would hold the window open
// forever on the bot's own traffic, so every proactive send would be attempted and
// every one would fail with 131047.
//
// Deduplication is required, not optional: Meta retries failed deliveries over 36
// hours and states "your server should handle deduplication in these cases".
func ProcessInbound(
	ctx context.Context,
	store ChatDataStore,
	botID string,
	payload WebhookPayload,
) (InboundResult, error) {
	result := InboundResult{Statuses: payload.Statuses()}

	messages := payload.InboundMessages()
	if len(messages) == 0 {
		return result, nil
	}

	// Group by sender so a batch touches each chat's record once rather than
	// re-reading it per message. Meta batches up to 1000 updates.
	order := make([]string, 0, len(messages))
	byChat := make(map[string][]InboundMessage, len(messages))
	for _, m := range messages {
		if m.From == "" {
			continue
		}
		if _, seen := byChat[m.From]; !seen {
			order = append(order, m.From)
		}
		byChat[m.From] = append(byChat[m.From], m)
	}

	for _, chatID := range order {
		data, err := store.GetChatData(ctx, botID, chatID)
		if err != nil {
			return result, fmt.Errorf("failed to load chat data for %s: %w", chatID, err)
		}
		if data == nil {
			data = &WaChatData{}
		}

		var changed bool
		for _, m := range byChat[chatID] {
			if data.RecordInbound(m.ID, m.SentAt()) {
				result.New = append(result.New, m)
				changed = true
			} else {
				result.Duplicates = append(result.Duplicates, m)
			}
		}
		if !changed {
			continue
		}
		if err = store.SaveChatData(ctx, botID, chatID, data); err != nil {
			// Deliberately fail the webhook rather than proceed. If the window
			// cannot be advanced, a later proactive send would be wrongly refused
			// — and Meta will retry this delivery, which dedup makes safe.
			return result, fmt.Errorf("failed to save chat data for %s: %w", chatID, err)
		}
	}
	return result, nil
}
