package main

import (
	"mpc-node/api"
	"mpc-node/internal/storage"
)

func main() {
	// Initialize database
	storage.InitDB()

	router := api.SetupRouter()
	router.Run(":8080") // listen and serve on 0.0.0.0:8080
}
