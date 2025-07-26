package main

import (
	"mpc-node/api"
	"mpc-node/internal/config"
	"mpc-node/internal/logger"
	"mpc-node/internal/storage"
)

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

	router := api.SetupRouter()
	logger.Log.Infof("Starting server on port %s", cfg.ServerPort)
	if err := router.Run(cfg.ServerPort); err != nil {
		logger.Log.Fatalf("Failed to start server: %v", err)
	}
}
