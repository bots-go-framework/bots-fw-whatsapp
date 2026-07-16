package whatsapp

import (
	"context"

	"github.com/dal-go/dalgo/dal"
)

const waChatDataCollection = "waChatData"

// dalgo ChatDataStore implementation.
type dalgoChatDataStore struct {
	db dal.DB
}

// NewDalgoChatDataStore returns a ChatDataStore backed by db.
// Collection: "waChatData"
// Key: botID + ":" + chatID (string)
func NewDalgoChatDataStore(db dal.DB) ChatDataStore {
	return &dalgoChatDataStore{db: db}
}

func chatDataKey(botID, chatID string) string {
	return botID + ":" + chatID
}

// GetChatData implements ChatDataStore.
// Returns a zero-valued WaChatData when the chat is not yet known; that is not an error.
func (s *dalgoChatDataStore) GetChatData(ctx context.Context, botID, chatID string) (*WaChatData, error) {
	key := dal.NewKeyWithID(waChatDataCollection, chatDataKey(botID, chatID))
	data := &WaChatData{}
	record := dal.NewRecordWithData(key, data)
	if err := s.db.Get(ctx, record); err != nil {
		if dal.IsNotFound(err) {
			return &WaChatData{}, nil
		}
		return nil, err
	}
	return data, nil
}

// SaveChatData implements ChatDataStore.
func (s *dalgoChatDataStore) SaveChatData(ctx context.Context, botID, chatID string, data *WaChatData) error {
	key := dal.NewKeyWithID(waChatDataCollection, chatDataKey(botID, chatID))
	record := dal.NewRecordWithData(key, data)
	return s.db.RunReadwriteTransaction(ctx, func(ctx context.Context, tx dal.ReadwriteTransaction) error {
		return tx.Set(ctx, record)
	})
}
