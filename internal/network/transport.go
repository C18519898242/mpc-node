package network

import (
	"bufio"
	"fmt"
	"io"
	"net"

	"mpc-node/internal/logger"
)

// Message represents a message to be sent over the transport.
type Message struct {
	From        string
	Payload     []byte
	IsBroadcast bool
	RemoteAddr  string
}

// Transport handles the network communication between nodes.
type Transport interface {
	Listen() error
	Send(to string, payload []byte) error
	Broadcast(peers []string, payload []byte) error
	Consume() <-chan Message
	GetAddress() string
}

// TCPTransport implements the Transport interface using TCP.
type TCPTransport struct {
	listenAddress string
	listener      net.Listener
	msgCh         chan Message
}

// NewTCPTransport creates a new TCPTransport.
func NewTCPTransport(listenAddr string) *TCPTransport {
	return &TCPTransport{
		listenAddress: listenAddr,
		msgCh:         make(chan Message, 1024),
	}
}

// Listen starts listening for incoming connections.
func (t *TCPTransport) Listen() error {
	ln, err := net.Listen("tcp", t.listenAddress)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", t.listenAddress, err)
	}
	t.listener = ln
	logger.Log.Infof("TCP transport listening on %s", t.listenAddress)

	go t.acceptLoop()

	return nil
}

// acceptLoop accepts incoming connections and handles them.
func (t *TCPTransport) acceptLoop() {
	for {
		conn, err := t.listener.Accept()
		if err != nil {
			logger.Log.Errorf("TCP accept error: %v", err)
			continue
		}
		logger.Log.Infof("Accepted connection from %s", conn.RemoteAddr())
		go t.handleConn(conn)
	}
}

// handleConn reads data from the connection and puts it into the message channel.
func (t *TCPTransport) handleConn(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	for {
		// Protocol: [1-byte from_len][from_addr][1-byte broadcast_flag][payload]\n
		fromLenByte, err := reader.ReadByte()
		if err != nil {
			if err != io.EOF {
				logger.Log.Debugf("TCP read from_len error from %s: %v", conn.RemoteAddr(), err)
			}
			return
		}
		fromLen := int(fromLenByte)
		fromAddrBytes := make([]byte, fromLen)
		_, err = io.ReadFull(reader, fromAddrBytes)
		if err != nil {
			logger.Log.Errorf("TCP read from_addr error: %v", err)
			return
		}
		fromAddr := string(fromAddrBytes)

		broadcastByte, err := reader.ReadByte()
		if err != nil {
			logger.Log.Errorf("TCP read broadcast byte error: %v", err)
			return
		}
		isBroadcast := broadcastByte == 0x01

		payload, err := reader.ReadBytes('\n')
		if err != nil {
			if err != io.EOF {
				logger.Log.Errorf("TCP read payload error: %v", err)
			}
			return
		}

		t.msgCh <- Message{
			From:        fromAddr,
			Payload:     payload[:len(payload)-1], // remove newline
			IsBroadcast: isBroadcast,
			RemoteAddr:  conn.RemoteAddr().String(),
		}
	}
}

// Send sends a message to a specific address.
func (t *TCPTransport) Send(to string, payload []byte) error {
	return t.send(to, payload, false)
}

// Broadcast sends a message to multiple addresses.
func (t *TCPTransport) Broadcast(peers []string, payload []byte) error {
	for _, peer := range peers {
		if peer == t.listenAddress {
			continue
		}
		if err := t.send(peer, payload, true); err != nil {
			logger.Log.Warnf("Failed to broadcast to peer %s: %v", peer, err)
		}
	}
	return nil
}

// send is a helper to send a message with the broadcast flag.
func (t *TCPTransport) send(to string, payload []byte, isBroadcast bool) error {
	conn, err := net.Dial("tcp", to)
	if err != nil {
		return fmt.Errorf("failed to dial %s: %w", to, err)
	}
	defer conn.Close()

	broadcastByte := byte(0x00)
	if isBroadcast {
		broadcastByte = 0x01
	}

	fromAddr := t.listenAddress
	fromLen := byte(len(fromAddr))

	// Protocol: [1-byte from_len][from_addr][1-byte broadcast_flag][payload]\n
	fullPayload := []byte{fromLen}
	fullPayload = append(fullPayload, []byte(fromAddr)...)
	fullPayload = append(fullPayload, broadcastByte)
	fullPayload = append(fullPayload, payload...)
	fullPayload = append(fullPayload, '\n')

	_, err = conn.Write(fullPayload)
	return err
}

// Consume returns a channel to consume incoming messages.
func (t *TCPTransport) Consume() <-chan Message {
	return t.msgCh
}

// GetAddress returns the listen address of the transport.
func (t *TCPTransport) GetAddress() string {
	return t.listenAddress
}
