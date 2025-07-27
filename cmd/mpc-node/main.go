package main

import (
	"encoding/json"
	"mpc-node/api"
	"mpc-node/internal/config"
	"mpc-node/internal/logger"
	"mpc-node/internal/party"
	"mpc-node/internal/storage"
	"net"
	"strings"

	"github.com/bnb-chain/tss-lib/v2/tss"
)

func startTCPServer(listenAddr string) {
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
		go handleTCPConnection(conn)
	}
}

// WireMessage is the format of messages sent over the TCP connection.
type WireMessage struct {
	From        *tss.PartyID
	IsBroadcast bool
	Payload     []byte
}

func handleTCPConnection(conn net.Conn) {
	defer conn.Close()
	localAddr := conn.LocalAddr().String()
	// a bit of a hack to get the port, e.g. "127.0.0.1:8001" -> ":8001"
	port := ":" + strings.Split(localAddr, ":")[1]

	logger.Log.Infof("Accepted TCP connection on %s from %s", port, conn.RemoteAddr())

	decoder := json.NewDecoder(conn)
	var wireMsg WireMessage
	if err := decoder.Decode(&wireMsg); err != nil {
		logger.Log.Errorf("Failed to decode wire message on %s: %v", port, err)
		return
	}

	pMsg, err := tss.ParseWireMessage(wireMsg.Payload, wireMsg.From, wireMsg.IsBroadcast)
	if err != nil {
		logger.Log.Errorf("Failed to parse TSS message on %s: %v", port, err)
		return
	}

	partyCh, ok := party.DefaultRegistry.Get(port)
	if !ok {
		logger.Log.Errorf("No party found for address %s. Cannot route message.", port)
		return
	}

	logger.Log.Infof("Routing message from %s to party on %s", wireMsg.From.Id, port)
	partyCh <- pMsg
}

func main() {
	// Load configuration
	cfg, err := config.LoadConfig("config.json")
	if err != nil {
		logger.Log.Fatalf("Failed to load configuration: %v", err)
	}

	// Initialize logger
	if err := logger.InitLogger(cfg.Logger); err != nil {
		logger.Log.Fatalf("Failed to initialize logger: %v", err)
	}

	logger.Log.Info("Configuration loaded and logger initialized.")

	// Initialize database
	storage.InitDB(cfg.Database)
	logger.Log.Info("Database initialized.")

	// Start TCP servers for TSS nodes based on config
	for _, port := range cfg.NodePorts {
		go startTCPServer(port)
	}

	router := api.SetupRouter(cfg)
	logger.Log.Infof("Starting server on port %s", cfg.ServerPort)
	if err := router.Run(cfg.ServerPort); err != nil {
		logger.Log.Fatalf("Failed to start server: %v", err)
	}
}
