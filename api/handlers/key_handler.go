package handlers

import (
	"mpc-node/internal/storage"
	"mpc-node/internal/storage/models"
	"mpc-node/internal/tss"
	"net/http"

	"github.com/gin-gonic/gin"
)

func GenerateKey(c *gin.Context) {
	keyRecord, err := tss.GenerateAndSaveKey()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to generate key: " + err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"key_id":     keyRecord.KeyID,
		"public_key": keyRecord.PublicKey,
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

func GetKeyByKeyID(c *gin.Context) {
	keyID := c.Param("key_id")
	var key models.KeyData
	result := storage.DB.First(&key, "key_id = ?", keyID)
	if result.Error != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "key not found"})
		return
	}
	c.JSON(http.StatusOK, key)
}
