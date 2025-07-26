package handlers

import (
	"mpc-node/internal/tss"
	"net/http"

	"github.com/gin-gonic/gin"
)

func GenerateKey(c *gin.Context) {
	// In a real app, you might pass parameters from the request to the service
	go tss.RunTssSimulation()
	c.JSON(http.StatusAccepted, gin.H{
		"message": "Key generation process started in the background.",
	})
}
