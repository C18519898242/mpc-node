package main

import (
	"log"
	"mpc-node/api"
	"mpc-node/internal/config"
	"mpc-node/internal/storage"
)

func main() {
	// Load configuration
	cfg, err := config.LoadConfig("config.json")
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Initialize database
	storage.InitDB(cfg.Database)

	router := api.SetupRouter()
	router.Run(cfg.ServerPort)
}
