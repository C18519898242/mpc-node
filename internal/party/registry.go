package party

import (
	"sync"

	"github.com/bnb-chain/tss-lib/v2/tss"
)

// Registry manages active TSS party instances.
// It maps a party's network address to a channel where it receives parsed messages.
type Registry struct {
	mu      sync.RWMutex
	parties map[string]chan<- tss.ParsedMessage
}

var (
	// Global registry instance
	DefaultRegistry = NewRegistry()
)

// NewRegistry creates a new party registry.
func NewRegistry() *Registry {
	return &Registry{
		parties: make(map[string]chan<- tss.ParsedMessage),
	}
}

// Register creates and registers a new message channel for a party at a given address.
func (r *Registry) Register(address string, partyChannel chan<- tss.ParsedMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.parties[address] = partyChannel
}

// Deregister removes a party from the registry.
func (r *Registry) Deregister(address string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.parties, address)
}

// Get retrieves the message channel for a party at a given address.
func (r *Registry) Get(address string) (chan<- tss.ParsedMessage, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	partyChannel, ok := r.parties[address]
	return partyChannel, ok
}
