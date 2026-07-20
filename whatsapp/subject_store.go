package whatsapp

import (
	"context"
	"sync"
	"time"
)

// SubjectStore maps a wamid to a subject string for correlating template
// quick-reply taps back to what they were about.
//
// Rationale: outside the 24-hour customer service window a template quick-reply
// tap returns only the button label, never developer-supplied state. The tap's
// context.id (the wamid of the template message) is the only available link
// back to the original subject. The mapping must therefore be written at send
// time with an expiry long enough to outlive realistic user response delays.
// A TTL of 35 days is recommended (≈ 5 weeks), which covers monthly recurring
// reminders plus a few days of slack.
type SubjectStore interface {
	// PutSubject stores the wamid→subject mapping for the given bot and chat.
	// expiresAt is the wall-clock instant after which the record is no longer valid.
	PutSubject(ctx context.Context, botID, wamid, subject string, expiresAt time.Time) error

	// GetSubject retrieves the subject for a wamid. It returns found=false for
	// expired records (lazy TTL: no background reaper; expiry is checked on
	// read). A missing key also returns found=false.
	GetSubject(ctx context.Context, botID, wamid string) (subject string, found bool, err error)
}

// memorySubjectEntry is a single in-memory subject entry.
type memorySubjectEntry struct {
	subject   string
	expiresAt time.Time
}

// memorySubjectStore is a mutex-protected in-memory implementation of SubjectStore.
type memorySubjectStore struct {
	mu      sync.Mutex
	entries map[string]memorySubjectEntry
}

// NewMemorySubjectStore returns a thread-safe in-memory SubjectStore with TTL-on-read.
func NewMemorySubjectStore() SubjectStore {
	return &memorySubjectStore{
		entries: make(map[string]memorySubjectEntry),
	}
}

func subjectKey(botID, wamid string) string {
	return botID + ":" + wamid
}

// PutSubject implements SubjectStore.
func (s *memorySubjectStore) PutSubject(_ context.Context, botID, wamid, subject string, expiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[subjectKey(botID, wamid)] = memorySubjectEntry{subject: subject, expiresAt: expiresAt}
	return nil
}

// GetSubject implements SubjectStore.
func (s *memorySubjectStore) GetSubject(_ context.Context, botID, wamid string) (subject string, found bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[subjectKey(botID, wamid)]
	if !ok {
		return "", false, nil
	}
	if time.Now().After(entry.expiresAt) {
		return "", false, nil
	}
	return entry.subject, true, nil
}
