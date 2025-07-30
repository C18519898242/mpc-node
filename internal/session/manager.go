package session

import (
	"mpc-node/internal/dto"
	"sync"
	"time"

	"github.com/bnb-chain/tss-lib/v2/common"
	"github.com/bnb-chain/tss-lib/v2/ecdsa/keygen"
	"github.com/google/uuid"
)

// SessionState represents the state of a key generation or signing session.
type SessionState struct {
	SessionID    string
	KeyID        string
	Participants []string
	Coordinator  string
	Status       string // e.g., "Pending", "Ready", "Finished", "Failed"
	CreatedAt    time.Time

	// Keygen-specific fields
	Acknowledgements map[string]bool
	PublicDataShares map[string]*keygen.LocalPartySaveData
	Done             chan struct{} // Channel to signal completion of the keygen ceremony

	// Signing-specific fields
	MessageToSign   string
	SignatureShares map[string]*common.SignatureData
	SignatureResult chan *dto.SignatureResponsePayload // Channel to send the final signature back to the API handler
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
		Status:           "Pending",
		CreatedAt:        time.Now(),
		Acknowledgements: make(map[string]bool),
		PublicDataShares: make(map[string]*keygen.LocalPartySaveData),
		Done:             make(chan struct{}),
		SignatureShares:  make(map[string]*common.SignatureData),
		SignatureResult:  make(chan *dto.SignatureResponsePayload, 1), // Buffered channel
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
