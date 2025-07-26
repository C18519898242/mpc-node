package party

import (
	"fmt"
	"math/big"
	"mpc-node/internal/config"
	"mpc-node/internal/logger"
	"mpc-node/internal/network"
	"sync"

	"github.com/bnb-chain/tss-lib/v2/ecdsa/keygen"
	"github.com/bnb-chain/tss-lib/v2/tss"
)

var (
	GlobalPartyServer *Server
)

// Server manages all the TSS parties.
type Server struct {
	parties    map[string]*Party
	partyIDs   tss.SortedPartyIDs
	config     []config.PartyConfig
	wg         sync.WaitGroup
	partyIDMap map[string]*tss.PartyID
}

// NewServer creates a new party server and sets the global instance.
func NewServer(cfg []config.PartyConfig) *Server {
	s := &Server{
		parties:    make(map[string]*Party, len(cfg)),
		config:     cfg,
		partyIDMap: make(map[string]*tss.PartyID, len(cfg)),
	}

	partyIDs := make(tss.UnSortedPartyIDs, len(cfg))
	for i, pCfg := range cfg {
		moniker := fmt.Sprintf("party-%d", i+1)
		pID := tss.NewPartyID(pCfg.Address, moniker, new(big.Int).SetInt64(int64(i+1)))
		partyIDs[i] = pID
		s.partyIDMap[pID.Id] = pID
	}
	s.partyIDs = tss.SortPartyIDs(partyIDs)
	GlobalPartyServer = s
	return s
}

// TriggerKeygen starts the key generation process across all parties.
func (s *Server) TriggerKeygen() ([]*keygen.LocalPartySaveData, error) {
	logger.Log.Info("Key generation triggered")

	// 1. Create channels for communication
	outCh := make(chan tss.Message, len(s.partyIDs))
	endCh := make(chan *keygen.LocalPartySaveData, len(s.partyIDs))
	errCh := make(chan *tss.Error, len(s.partyIDs))

	// 2. Create and start each party
	for _, pID := range s.partyIDs {
		params := tss.NewParameters(tss.S256(), tss.NewPeerContext(s.partyIDs), pID, len(s.partyIDs), len(s.partyIDs)-1)
		p := keygen.NewLocalParty(params, outCh, endCh)
		s.parties[pID.Id].SetTssParty(p)
	}

	s.wg.Add(len(s.partyIDs))
	for _, pID := range s.partyIDs {
		go func(p *Party) {
			defer s.wg.Done()
			if err := p.tssParty.Start(); err != nil {
				errCh <- err
			}
		}(s.parties[pID.Id])
	}

	// 4. Goroutine to handle outgoing messages
	go func() {
		for msg := range outCh {
			bytes, _, err := msg.WireBytes()
			if err != nil {
				logger.Log.Errorf("Failed to get wire bytes from message: %v", err)
				continue
			}
			if msg.IsBroadcast() {
				var peers []string
				for _, pCfg := range s.config {
					if pCfg.Address == msg.GetFrom().Id {
						continue
					}
					peers = append(peers, pCfg.Address)
				}
				s.parties[msg.GetFrom().Id].transport.Broadcast(peers, bytes)
			} else {
				for _, dest := range msg.GetTo() {
					if dest.Id == msg.GetFrom().Id {
						continue
					}
					s.parties[msg.GetFrom().Id].transport.Send(dest.Id, bytes)
				}
			}
		}
	}()

	// 5. Wait for results
	var savedData []*keygen.LocalPartySaveData
	for i := 0; i < len(s.partyIDs); i++ {
		select {
		case result := <-endCh:
			savedData = append(savedData, result)
		case err := <-errCh:
			return nil, err
		}
	}
	s.wg.Wait()

	return savedData, nil
}

// Start starts all the party servers.
func (s *Server) Start() error {
	for _, pID := range s.partyIDs {
		transport := network.NewTCPTransport(pID.Id)
		party := NewParty(pID, nil, transport) // pID is now set at creation
		s.parties[pID.Id] = party
		if err := party.Start(); err != nil {
			return fmt.Errorf("failed to start party at %s: %w", pID.Id, err)
		}
		go party.processMessages()
	}
	logger.Log.Infof("All %d parties started", len(s.parties))
	return nil
}

// Party represents a single TSS participant.
type Party struct {
	pID       *tss.PartyID
	tssParty  tss.Party
	transport network.Transport
}

// NewParty creates a new party.
func NewParty(pID *tss.PartyID, tssParty tss.Party, transport network.Transport) *Party {
	return &Party{
		pID:       pID,
		tssParty:  tssParty,
		transport: transport,
	}
}

// SetTssParty sets the tss.Party for a Party.
func (p *Party) SetTssParty(tssParty tss.Party) {
	p.tssParty = tssParty
	p.pID = tssParty.PartyID()
}

// Start starts the party's network listener and message processing loop.
func (p *Party) Start() error {
	return p.transport.Listen()
}

// processMessages is the main event loop for a party.
func (p *Party) processMessages() {
	for netMsg := range p.transport.Consume() {
		if p.tssParty == nil {
			logger.Log.Warnf("TSS party not initialized for %s, discarding message from %s", p.transport.GetAddress(), netMsg.From)
			continue
		}
		go func(msg network.Message) {
			fromP := findPartyByID(msg.From)
			if fromP == nil {
				// Log the entire map for debugging, this might be verbose
				var knownParties []string
				for id := range GlobalPartyServer.partyIDMap {
					knownParties = append(knownParties, id)
				}
				logger.Log.Warnf("Received message from unknown party '%s' (remote: %s). Known parties: %v", msg.From, msg.RemoteAddr, knownParties)
				return
			}

			parsedMsg, err := tss.ParseWireMessage(msg.Payload, fromP, msg.IsBroadcast)
			if err != nil {
				logger.Log.Errorf("Failed to parse wire message: %v", err)
				return
			}
			if _, err := p.tssParty.Update(parsedMsg); err != nil {
				logger.Log.Errorf("Party %s failed to update TSS party: %v", p.pID.Id, err)
			}
		}(netMsg)
	}
}

func findPartyByID(id string) *tss.PartyID {
	return GlobalPartyServer.partyIDMap[id]
}
