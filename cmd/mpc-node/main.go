package main

import (
	"flag"
	"fmt"
	"mpc-node/api"
	"mpc-node/internal/config"
	"mpc-node/internal/logger"
	"mpc-node/internal/network"
	"mpc-node/internal/session"
	"mpc-node/internal/storage"
)

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
	server := network.NewServer(cfg, *nodeName, sessionManager, transport)
	listenAddr := fmt.Sprintf(":%d", currentNodeConfig.Port)
	go server.Start(listenAddr)

	// --- Start API server for this node ---
	router := api.SetupRouter(cfg, *nodeName, sessionManager, transport)
	apiPort := fmt.Sprintf(":%d", currentNodeConfig.APIPort) // Assuming API port is different
	logger.Log.Infof("Starting API server on port %s for node %s", apiPort, *nodeName)
	if err := router.Run(apiPort); err != nil {
		logger.Log.Fatalf("Failed to start API server: %v", err)
	}
}
