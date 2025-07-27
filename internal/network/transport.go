package network

import (
	"encoding/json"
	"fmt"
	"mpc-node/internal/logger"
	"net"
	"time"

	"github.com/bnb-chain/tss-lib/v2/tss"
)

// Transport defines the interface for network communication.
type Transport interface {
	Send(msg tss.Message) error
}

// TCPTransport implements the Transport interface using TCP.
type TCPTransport struct {
	partyIDMap map[string]string // Maps PartyID.Id to a network address like "localhost:8001"
}

// NewTCPTransport creates a new TCPTransport.
func NewTCPTransport(partyIDMap map[string]string) *TCPTransport {
	return &TCPTransport{
		partyIDMap: partyIDMap,
	}
}

// Send marshals the message and sends it to the destination parties.
func (t *TCPTransport) Send(msg tss.Message) error {
	dest := msg.GetTo()
	if dest == nil { // broadcast
		logger.Log.Infof("[TCPTransport] Broadcasting message type %s from %s", msg.Type(), msg.GetFrom().Id)
		for pID, addr := range t.partyIDMap {
			if pID == msg.GetFrom().Id {
				continue
			}
			if err := t.sendMessage(addr, msg); err != nil {
				logger.Log.Errorf("[TCPTransport] Failed to send broadcast to %s (%s): %v", pID, addr, err)
				// In a real system, you might want to handle this more gracefully
			}
		}
		return nil
	}

	// P2P message
	for _, pID := range dest {
		addr, ok := t.partyIDMap[pID.Id]
		if !ok {
			return fmt.Errorf("no address found for party %s", pID.Id)
		}
		logger.Log.Infof("[TCPTransport] Sending P2P message type %s from %s to %s (%s)", msg.Type(), msg.GetFrom().Id, pID.Id, addr)
		if err := t.sendMessage(addr, msg); err != nil {
			return fmt.Errorf("failed to send P2P message to %s (%s): %v", pID.Id, addr, err)
		}
	}
	return nil
}

func (t *TCPTransport) sendMessage(addr string, msg tss.Message) error {
	// Use a timeout to avoid blocking indefinitely
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return fmt.Errorf("failed to connect to %s: %v", addr, err)
	}
	defer conn.Close()

	// We need to send the raw bytes of the message.
	// The receiving end will need to parse it.
	bytes, _, err := msg.WireBytes()
	if err != nil {
		return fmt.Errorf("failed to get wire bytes for message: %v", err)
	}

	// To help the receiver parse, we can wrap the message in a simple struct
	// This is a basic wire protocol.
	type WireMessage struct {
		From        *tss.PartyID
		IsBroadcast bool
		Payload     []byte
	}

	wireMsg := WireMessage{
		From:        msg.GetFrom(),
		IsBroadcast: msg.IsBroadcast(),
		Payload:     bytes,
	}

	encoder := json.NewEncoder(conn)
	if err := encoder.Encode(&wireMsg); err != nil {
		return fmt.Errorf("failed to encode and send message to %s: %v", addr, err)
	}

	return nil
}
