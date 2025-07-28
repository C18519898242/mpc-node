package session

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// SessionState represents the state of a key generation session.
type SessionState struct {
	SessionID        string
	KeyID            string
	Participants     []string
	Coordinator      string
	Acknowledgements map[string]bool // Tracks which participants have acknowledged the keyID
	Status           string          // e.g., "Pending", "Ready", "Finished", "Failed"
	CreatedAt        time.Time
	Done             chan struct{} // Channel to signal completion of the ceremony
}

// Manager handles the lifecycle of TSS sessions.
type Manager struct {
	sessions map[string]*SessionState
	mu       sync.RWMutex
}

// NewManager creates a new session manager.
func NewManager() *Manager {
	return &Manager{
		sessions: make(map[string]*SessionState),
	}
}

// GetOrCreateSession retrieves an existing session or creates a new one.
func (m *Manager) GetOrCreateSession(sessionID string, participants []string, coordinator string) *SessionState {
	m.mu.Lock()
	defer m.mu.Unlock()

	if session, exists := m.sessions[sessionID]; exists {
		return session
	}

	session := &SessionState{
		SessionID:        sessionID,
		Participants:     participants,
		Coordinator:      coordinator,
		Acknowledgements: make(map[string]bool),
		Status:           "Pending",
		CreatedAt:        time.Now(),
		Done:             make(chan struct{}),
	}

	m.sessions[sessionID] = session
	return session
}

// GetSession retrieves a session by its ID.
func (m *Manager) GetSession(sessionID string) (*SessionState, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	session, exists := m.sessions[sessionID]
	return session, exists
}

// SetKeyID sets the KeyID for a session. This should only be done by the coordinator.
func (m *Manager) SetKeyID(sessionID string, keyID uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if session, exists := m.sessions[sessionID]; exists {
		session.KeyID = keyID.String()
	}
}

// RecordAcknowledgement records an acknowledgement from a participant.
func (m *Manager) RecordAcknowledgement(sessionID, partyID string) (allAcksReceived bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, exists := m.sessions[sessionID]
	if !exists {
		return false
	}

	session.Acknowledgements[partyID] = true

	// Check if all participants (excluding the coordinator) have acknowledged.
	ackCount := 0
	for _, p := range session.Participants {
		if session.Acknowledgements[p] {
			ackCount++
		}
	}

	// All participants must acknowledge
	return ackCount == len(session.Participants)
}

// UpdateStatus updates the status of a session.
func (m *Manager) UpdateStatus(sessionID, status string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if session, exists := m.sessions[sessionID]; exists {
		session.Status = status
	}
}
