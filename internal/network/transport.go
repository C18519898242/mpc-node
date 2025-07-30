package network

import (
	"encoding/json"
	"fmt"
	"mpc-node/internal/logger"
	"net"
	"time"

	"github.com/bnb-chain/tss-lib/v2/ecdsa/keygen"
	tsslib "github.com/bnb-chain/tss-lib/v2/tss"
)

// CoordinationMessageType defines the type of a coordination message.
type CoordinationMessageType string

const (
	// Keygen
	KeyIDBroadcast        CoordinationMessageType = "KeyIDBroadcast"
	KeyIDAck              CoordinationMessageType = "KeyIDAck"
	StartKeygen           CoordinationMessageType = "StartKeygen"
	KeygenPublicDataShare CoordinationMessageType = "KeygenPublicDataShare"
	KeygenResultBroadcast CoordinationMessageType = "KeygenResultBroadcast"

	// Signing
	StartSigning      CoordinationMessageType = "StartSigning"
	SignatureShare    CoordinationMessageType = "SignatureShare"
	SigningResult     CoordinationMessageType = "SigningResult"
	RequestSignature  CoordinationMessageType = "RequestSignature"
	SignatureResponse CoordinationMessageType = "SignatureResponse"
)

// CoordinationMessage is a generic container for non-TSS coordination messages.
type CoordinationMessage struct {
	Type      CoordinationMessageType `json:"type"`
	SessionID string                  `json:"sessionId"`
	Payload   json.RawMessage         `json:"payload"`
	From      *tsslib.PartyID         `json:"from"`
	To        []*tsslib.PartyID       `json:"to"`
}

// KeyIDBroadcastPayload is the payload for a KeyIDBroadcast message.
type KeyIDBroadcastPayload struct {
	KeyID        string   `json:"keyId"`
	Participants []string `json:"participants"`
}

// KeyIDAckPayload is the payload for a KeyIDAck message.
type KeyIDAckPayload struct {
	// No extra payload needed for a simple ack
}

// KeygenPublicDataPayload is the payload for a KeygenPublicDataShare message.
type KeygenPublicDataPayload struct {
	SaveData *keygen.LocalPartySaveData `json:"saveData"`
}

// KeygenResultBroadcastPayload is the payload for the final broadcast from the coordinator.
type KeygenResultBroadcastPayload struct {
	PublicKey    string `json:"publicKey"`
	FullSaveData []byte `json:"fullSaveData"`
}

// RequestSignaturePayload is the payload for a RequestSignature message.
type RequestSignaturePayload struct {
	KeyID   string `json:"keyId"`
	Message string `json:"message"`
}

// Transport defines the interface for network communication.
type Transport interface {
	Send(msg tsslib.Message) error
	SendCoordinationMessage(msg *CoordinationMessage) error
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
func (t *TCPTransport) Send(msg tsslib.Message) error {
	dest := msg.GetTo()
	if dest == nil { // broadcast
		logger.Log.Infof("[TCPTransport] Broadcasting message type %s from %s", msg.Type(), msg.GetFrom().Id)
		for pID, addr := range t.partyIDMap {
			// Also send to self to ensure the local party's message processing is triggered
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

// SendCoordinationMessage sends a custom coordination message to the destination parties.
func (t *TCPTransport) SendCoordinationMessage(msg *CoordinationMessage) error {
	dest := msg.To
	if dest == nil { // broadcast
		logger.Log.Infof("[TCPTransport] Broadcasting coordination message type %s from %s", msg.Type, msg.From.Id)
		for pID, addr := range t.partyIDMap {
			// Also send to self to ensure the local party's message processing is triggered
			if err := t.sendJSON(addr, msg, "Coordination"); err != nil {
				logger.Log.Errorf("[TCPTransport] Failed to send coordination broadcast to %s (%s): %v", pID, addr, err)
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
		logger.Log.Infof("[TCPTransport] Sending P2P coordination message type %s from %s to %s (%s)", msg.Type, msg.From.Id, pID.Id, addr)
		if err := t.sendJSON(addr, msg, "Coordination"); err != nil {
			return fmt.Errorf("failed to send P2P coordination message to %s (%s): %v", pID.Id, addr, err)
		}
	}
	return nil
}

func (t *TCPTransport) sendMessage(addr string, msg tsslib.Message) error {
	bytes, _, err := msg.WireBytes()
	if err != nil {
		return fmt.Errorf("failed to get wire bytes for message: %v", err)
	}

	// This is a basic wire protocol.
	type TSSWireMessage struct {
		From        *tsslib.PartyID
		IsBroadcast bool
		Payload     []byte
	}

	wireMsg := TSSWireMessage{
		From:        msg.GetFrom(),
		IsBroadcast: msg.IsBroadcast(),
		Payload:     bytes,
	}
	return t.sendJSON(addr, wireMsg, "TSS")
}

// WireMessage is a wrapper for any message sent over the wire.
type WireMessage struct {
	MessageType string          `json:"messageType"` // "TSS" or "Coordination"
	Payload     json.RawMessage `json:"payload"`
}

func (t *TCPTransport) sendJSON(addr string, payload interface{}, messageType string) error {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return fmt.Errorf("failed to connect to %s: %v", addr, err)
	}
	defer conn.Close()

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload for wire message: %v", err)
	}

	wireMsg := WireMessage{
		MessageType: messageType,
		Payload:     payloadBytes,
	}

	encoder := json.NewEncoder(conn)
	if err := encoder.Encode(&wireMsg); err != nil {
		return fmt.Errorf("failed to encode and send message to %s: %v", addr, err)
	}
	return nil
}
