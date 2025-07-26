package main

import (
	"io"
	"mpc-node/api"
	"mpc-node/internal/config"
	"mpc-node/internal/logger"
	"mpc-node/internal/storage"
	"net"
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

func handleTCPConnection(conn net.Conn) {
	defer conn.Close()
	logger.Log.Infof("Accepted TCP connection from %s", conn.RemoteAddr())
	buf := make([]byte, 1024)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			if err != io.EOF {
				logger.Log.Errorf("TCP read error: %v", err)
			}
			return
		}
		message := string(buf[:n])
		logger.Log.Infof("Received message from %s: %s", conn.RemoteAddr(), message)
	}
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

	go startTCPServer(":8001")
	go startTCPServer(":8002")
	go startTCPServer(":8003")

	router := api.SetupRouter()
	logger.Log.Infof("Starting server on port %s", cfg.ServerPort)
	if err := router.Run(cfg.ServerPort); err != nil {
		logger.Log.Fatalf("Failed to start server: %v", err)
	}
}
