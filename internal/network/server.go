package network

import (
	"encoding/json"
	"math/big"
	"mpc-node/internal/config"
	"mpc-node/internal/logger"
	"mpc-node/internal/party"
	"mpc-node/internal/session"
	"mpc-node/internal/tss"
	"net"
	"strings"

	tsslib "github.com/bnb-chain/tss-lib/v2/tss"
	"github.com/google/uuid"
)

// Server manages the TCP connections and message handling.
type Server struct {
	cfg            *config.Config
	nodeName       string
	sessionManager *session.Manager
	transport      Transport
}

// NewServer creates a new TCP server instance.
func NewServer(cfg *config.Config, nodeName string, sm *session.Manager, transport Transport) *Server {
	return &Server{
		cfg:            cfg,
		nodeName:       nodeName,
		sessionManager: sm,
		transport:      transport,
	}
}

// Start listens for incoming TCP connections and handles them.
func (s *Server) Start(listenAddr string) {
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		logger.Log.Fatalf("Failed to listen on %s: %v", listenAddr, err)
	}
	defer ln.Close()
	logger.Log.Infof("TCP server listening on %s", listenAddr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			logger.Log.Errorf("TCP accept error: %v", err)
			continue
		}
		go s.handleTCPConnection(conn)
	}
}

func (s *Server) handleTCPConnection(conn net.Conn) {
	defer conn.Close()
	localAddr := conn.LocalAddr().String()
	port := ":" + strings.Split(localAddr, ":")[1]
	logger.Log.Infof("Accepted TCP connection on %s from %s", port, conn.RemoteAddr())

	decoder := json.NewDecoder(conn)
	var wireMsg WireMessage
	if err := decoder.Decode(&wireMsg); err != nil {
		logger.Log.Errorf("Failed to decode wire message on %s: %v", port, err)
		return
	}

	switch wireMsg.MessageType {
	case "TSS":
		handleTSSMessage(wireMsg, port)
	case "Coordination":
		s.handleCoordinationMessage(wireMsg)
	default:
		logger.Log.Errorf("Unknown message type received: %s", wireMsg.MessageType)
	}
}

func handleTSSMessage(wireMsg WireMessage, port string) {
	payloadBytes, _ := json.Marshal(wireMsg.Payload)
	var tssMsg struct {
		From        *tsslib.PartyID
		IsBroadcast bool
		Payload     []byte
	}
	if err := json.Unmarshal(payloadBytes, &tssMsg); err != nil {
		logger.Log.Errorf("Failed to unmarshal TSS message payload: %v", err)
		return
	}

	pMsg, err := tsslib.ParseWireMessage(tssMsg.Payload, tssMsg.From, tssMsg.IsBroadcast)
	if err != nil {
		logger.Log.Errorf("Failed to parse TSS message on %s: %v", port, err)
		return
	}

	partyCh, ok := party.DefaultRegistry.Get(port)
	if !ok {
		logger.Log.Warnf("No active party ceremony for address %s. Dropping message.", port)
		return
	}

	logger.Log.Infof("Routing TSS message from %s to party on %s", tssMsg.From.Id, port)
	partyCh <- pMsg
}

func (s *Server) handleCoordinationMessage(wireMsg WireMessage) {
	payloadBytes, _ := json.Marshal(wireMsg.Payload)
	var coordMsg CoordinationMessage
	if err := json.Unmarshal(payloadBytes, &coordMsg); err != nil {
		logger.Log.Errorf("Failed to unmarshal coordination message: %v", err)
		return
	}

	logger.Log.Infof("Handling coordination message type %s for session %s", coordMsg.Type, coordMsg.SessionID)

	switch coordMsg.Type {
	case KeyIDBroadcast:
		var payload KeyIDBroadcastPayload
		if err := json.Unmarshal(coordMsg.Payload, &payload); err != nil {
			logger.Log.Errorf("Failed to unmarshal KeyIDBroadcast payload: %v", err)
			return
		}
		keyUUID, err := uuid.Parse(payload.KeyID)
		if err != nil {
			logger.Log.Errorf("Invalid KeyID in broadcast: %v", err)
			return
		}

		// Store the KeyID and send an ACK
		s.sessionManager.SetKeyID(coordMsg.SessionID, keyUUID)
		logger.Log.Infof("Node %s stored KeyID %s for session %s", s.nodeName, payload.KeyID, coordMsg.SessionID)

		// Find current party ID
		var currentPartyID *tsslib.PartyID
		for i, p := range s.cfg.Nodes {
			if p.Node == s.nodeName {
				currentPartyID = tsslib.NewPartyID(p.Node, p.Node, new(big.Int).SetInt64(int64(i)))
				break
			}
		}

		ackMsg := &CoordinationMessage{
			Type:      KeyIDAck,
			SessionID: coordMsg.SessionID,
			From:      currentPartyID,
			To:        []*tsslib.PartyID{coordMsg.From}, // Send ACK back to the coordinator
		}
		if err := s.transport.SendCoordinationMessage(ackMsg); err != nil {
			logger.Log.Errorf("Failed to send KeyID ACK: %v", err)
		}

	case KeyIDAck:
		// This is handled by the coordinator
		allAcksIn := s.sessionManager.RecordAcknowledgement(coordMsg.SessionID, coordMsg.From.Id)
		logger.Log.Infof("Coordinator received ACK for session %s from %s. All ACKs received: %t", coordMsg.SessionID, coordMsg.From.Id, allAcksIn)
		if allAcksIn {
			logger.Log.Infof("All parties acknowledged. Broadcasting StartKeygen for session %s.", coordMsg.SessionID)
			session, ok := s.sessionManager.GetSession(coordMsg.SessionID)
			if !ok {
				logger.Log.Errorf("Session %s not found for starting keygen", coordMsg.SessionID)
				return
			}

			// Find coordinator's party ID to set as the 'From' field
			var coordinatorPartyID *tsslib.PartyID
			for i, p := range s.cfg.Nodes {
				if p.Node == session.Coordinator {
					coordinatorPartyID = tsslib.NewPartyID(p.Node, p.Node, new(big.Int).SetInt64(int64(i)))
					break
				}
			}
			if coordinatorPartyID == nil {
				logger.Log.Errorf("Could not find party ID for coordinator %s", session.Coordinator)
				return
			}

			startKeygenMsg := &CoordinationMessage{
				Type:      StartKeygen,
				SessionID: coordMsg.SessionID,
				From:      coordinatorPartyID,
				To:        nil, // Broadcast to all parties
			}
			// The coordinator sends to all, and will also receive it and start the process.
			if err := s.transport.SendCoordinationMessage(startKeygenMsg); err != nil {
				logger.Log.Errorf("Coordinator failed to broadcast StartKeygen: %v", err)
			}
		}

	case StartKeygen:
		logger.Log.Infof("Received StartKeygen for session %s. Triggering TSS key generation.", coordMsg.SessionID)
		session, ok := s.sessionManager.GetSession(coordMsg.SessionID)
		if !ok {
			logger.Log.Errorf("Session %s not found for keygen", coordMsg.SessionID)
			return
		}
		keyUUID, err := uuid.Parse(session.KeyID)
		if err != nil {
			logger.Log.Errorf("Invalid KeyID in session %s: %v", coordMsg.SessionID, err)
			return
		}

		// This needs to run in a goroutine so it doesn't block the network handler
		go func() {
			// When the goroutine finishes, signal completion by closing the Done channel.
			defer close(session.Done)

			_, err := tss.GenerateAndSaveKey(s.cfg, s.nodeName, keyUUID, s.transport)
			if err != nil {
				logger.Log.Errorf("TSS key generation failed for session %s: %v", coordMsg.SessionID, err)
				s.sessionManager.UpdateStatus(coordMsg.SessionID, "Failed")
			} else {
				logger.Log.Infof("TSS key generation successful for session %s, KeyID %s", coordMsg.SessionID, keyUUID)
				s.sessionManager.UpdateStatus(coordMsg.SessionID, "Finished")
			}
		}()
	}
}
