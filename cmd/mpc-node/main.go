package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"mpc-node/api"
	"mpc-node/internal/config"
	"mpc-node/internal/logger"
	"mpc-node/internal/network"
	"mpc-node/internal/party"
	"mpc-node/internal/session"
	"mpc-node/internal/storage"
	"mpc-node/internal/tss"
	"net"
	"strings"

	"math/big"

	tsslib "github.com/bnb-chain/tss-lib/v2/tss"
	"github.com/google/uuid"
)

func handleTCPConnection(conn net.Conn, sessionManager *session.Manager, transport network.Transport, cfg *config.Config, nodeName string) {
	defer conn.Close()
	localAddr := conn.LocalAddr().String()
	port := ":" + strings.Split(localAddr, ":")[1]
	logger.Log.Infof("Accepted TCP connection on %s from %s", port, conn.RemoteAddr())

	decoder := json.NewDecoder(conn)
	var wireMsg network.WireMessage
	if err := decoder.Decode(&wireMsg); err != nil {
		logger.Log.Errorf("Failed to decode wire message on %s: %v", port, err)
		return
	}

	switch wireMsg.MessageType {
	case "TSS":
		handleTSSMessage(wireMsg, port)
	case "Coordination":
		handleCoordinationMessage(wireMsg, sessionManager, transport, cfg, nodeName)
	default:
		logger.Log.Errorf("Unknown message type received: %s", wireMsg.MessageType)
	}
}

func handleTSSMessage(wireMsg network.WireMessage, port string) {
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

func handleCoordinationMessage(wireMsg network.WireMessage, sm *session.Manager, transport network.Transport, cfg *config.Config, nodeName string) {
	payloadBytes, _ := json.Marshal(wireMsg.Payload)
	var coordMsg network.CoordinationMessage
	if err := json.Unmarshal(payloadBytes, &coordMsg); err != nil {
		logger.Log.Errorf("Failed to unmarshal coordination message: %v", err)
		return
	}

	logger.Log.Infof("Handling coordination message type %s for session %s", coordMsg.Type, coordMsg.SessionID)

	switch coordMsg.Type {
	case network.KeyIDBroadcast:
		var payload network.KeyIDBroadcastPayload
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
		sm.SetKeyID(coordMsg.SessionID, keyUUID)
		logger.Log.Infof("Node %s stored KeyID %s for session %s", nodeName, payload.KeyID, coordMsg.SessionID)

		// Find current party ID
		var currentPartyID *tsslib.PartyID
		// This is a simplification. In a real app, party info would be more accessible.
		for i, p := range cfg.Nodes {
			if p.Node == nodeName {
				currentPartyID = tsslib.NewPartyID(p.Node, p.Node, new(big.Int).SetInt64(int64(i)))
				break
			}
		}

		ackMsg := &network.CoordinationMessage{
			Type:      network.KeyIDAck,
			SessionID: coordMsg.SessionID,
			From:      currentPartyID,
			To:        []*tsslib.PartyID{coordMsg.From}, // Send ACK back to the coordinator
		}
		if err := transport.SendCoordinationMessage(ackMsg); err != nil {
			logger.Log.Errorf("Failed to send KeyID ACK: %v", err)
		}

	case network.KeyIDAck:
		// This is handled by the coordinator
		allAcksIn := sm.RecordAcknowledgement(coordMsg.SessionID, coordMsg.From.Id)
		logger.Log.Infof("Coordinator received ACK for session %s from %s. All ACKs received: %t", coordMsg.SessionID, coordMsg.From.Id, allAcksIn)
		if allAcksIn {
			logger.Log.Infof("All parties acknowledged. Broadcasting StartKeygen for session %s.", coordMsg.SessionID)
			session, ok := sm.GetSession(coordMsg.SessionID)
			if !ok {
				logger.Log.Errorf("Session %s not found for starting keygen", coordMsg.SessionID)
				return
			}

			// Find coordinator's party ID to set as the 'From' field
			var coordinatorPartyID *tsslib.PartyID
			for i, p := range cfg.Nodes {
				if p.Node == session.Coordinator {
					coordinatorPartyID = tsslib.NewPartyID(p.Node, p.Node, new(big.Int).SetInt64(int64(i)))
					break
				}
			}
			if coordinatorPartyID == nil {
				logger.Log.Errorf("Could not find party ID for coordinator %s", session.Coordinator)
				return
			}

			startKeygenMsg := &network.CoordinationMessage{
				Type:      network.StartKeygen,
				SessionID: coordMsg.SessionID,
				From:      coordinatorPartyID,
				To:        nil, // Broadcast to all parties
			}
			// The coordinator sends to all, and will also receive it and start the process.
			if err := transport.SendCoordinationMessage(startKeygenMsg); err != nil {
				logger.Log.Errorf("Coordinator failed to broadcast StartKeygen: %v", err)
			}
		}

	case network.StartKeygen:
		logger.Log.Infof("Received StartKeygen for session %s. Triggering TSS key generation.", coordMsg.SessionID)
		session, ok := sm.GetSession(coordMsg.SessionID)
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

			_, err := tss.GenerateAndSaveKey(cfg, nodeName, keyUUID)
			if err != nil {
				logger.Log.Errorf("TSS key generation failed for session %s: %v", coordMsg.SessionID, err)
				sm.UpdateStatus(coordMsg.SessionID, "Failed")
			} else {
				logger.Log.Infof("TSS key generation successful for session %s, KeyID %s", coordMsg.SessionID, keyUUID)
				sm.UpdateStatus(coordMsg.SessionID, "Finished")
			}
		}()
	}
}

func main() {
	nodeName := flag.String("node", "", "The name of the node to run (e.g., node1)")
	flag.Parse()

	if *nodeName == "" {
		logger.Log.Fatal("The -node flag is required")
	}

	// Load configuration
	cfg, err := config.LoadConfig("config.json")
	if err != nil {
		logger.Log.Fatalf("Failed to load configuration: %v", err)
	}

	// Initialize logger
	if err := logger.InitLogger(cfg.Logger); err != nil {
		logger.Log.Fatalf("Failed to initialize logger: %v", err)
	}

	logger.Log.Infof("Starting node: %s", *nodeName)

	// Find the config for the current node
	var currentNodeConfig *config.Node
	for i := range cfg.Nodes {
		if cfg.Nodes[i].Node == *nodeName {
			currentNodeConfig = &cfg.Nodes[i]
			break
		}
	}
	if currentNodeConfig == nil {
		logger.Log.Fatalf("Configuration for node %s not found", *nodeName)
	}

	// Initialize database
	storage.InitDB(cfg.Database)
	logger.Log.Info("Database initialized.")

	// --- Create services ---
	sessionManager := session.NewManager()

	// Create a map of all parties for the transport layer
	partyIDMap := make(map[string]string)
	for _, p := range cfg.Nodes {
		partyIDMap[p.Node] = fmt.Sprintf("%s:%d", p.Server, p.Port)
	}
	transport := network.NewTCPTransport(partyIDMap)

	// --- Start TCP server for P2P communication ---
	// This node needs to listen for messages from other nodes.
	listenAddr := fmt.Sprintf(":%d", currentNodeConfig.Port)
	go func() {
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
			go handleTCPConnection(conn, sessionManager, transport, cfg, *nodeName)
		}
	}()

	// --- Start API server for this node ---
	router := api.SetupRouter(cfg, *nodeName, sessionManager, transport)
	apiPort := fmt.Sprintf(":%d", currentNodeConfig.APIPort) // Assuming API port is different
	logger.Log.Infof("Starting API server on port %s for node %s", apiPort, *nodeName)
	if err := router.Run(apiPort); err != nil {
		logger.Log.Fatalf("Failed to start API server: %v", err)
	}
}
