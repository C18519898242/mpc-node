package handlers

import (
	"mpc-node/internal/storage"
	"mpc-node/internal/storage/models"
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

func GetKeys(c *gin.Context) {
	var keys []models.KeyData
	result := storage.DB.Find(&keys)
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to retrieve keys from database.",
		})
		return
	}
	c.JSON(http.StatusOK, keys)
}
