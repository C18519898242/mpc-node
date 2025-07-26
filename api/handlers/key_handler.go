package handlers

import (
	"mpc-node/internal/storage"
	"mpc-node/internal/storage/models"
	"mpc-node/internal/tss"
	"net/http"

	"github.com/bnb-chain/tss-lib/v2/common"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type SignRequest struct {
	KeyID   string `json:"key_id" binding:"required"`
	Message string `json:"message" binding:"required"`
}

type VerifyRequest struct {
	KeyID     string `json:"key_id"`
	PublicKey string `json:"public_key"`
	Message   string `json:"message" binding:"required"`
	Signature common.SignatureData
}

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

func ListKeys(c *gin.Context) {
	var keys []models.KeyData
	// We only need KeyID and PublicKey, no need to Preload Shares
	result := storage.DB.Find(&keys)
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to retrieve keys from database.",
		})
		return
	}

	// Create a custom response struct
	type KeyInfo struct {
		KeyID     string `json:"key_id"`
		PublicKey string `json:"public_key"`
	}

	var response []KeyInfo
	for _, key := range keys {
		response = append(response, KeyInfo{
			KeyID:     key.KeyID.String(),
			PublicKey: key.PublicKey,
		})
	}

	c.JSON(http.StatusOK, response)
}

func GetKeyByKeyID(c *gin.Context) {
	keyID := c.Param("key_id")
	var key models.KeyData
	result := storage.DB.Preload("Shares").First(&key, "key_id = ?", keyID)
	if result.Error != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "key not found"})
		return
	}
	c.JSON(http.StatusOK, key)
}

func SignMessage(c *gin.Context) {
	var req SignRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	keyUUID, err := uuid.Parse(req.KeyID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid KeyID format"})
		return
	}

	signature, err := tss.SignMessage(keyUUID, req.Message)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to sign message: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"signature": signature,
	})
}

func VerifySignature(c *gin.Context) {
	var req VerifyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	publicKey := req.PublicKey
	// If PublicKey is not provided, try to get it from the database using KeyID
	if publicKey == "" {
		if req.KeyID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Either key_id or public_key must be provided"})
			return
		}
		keyUUID, err := uuid.Parse(req.KeyID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid KeyID format"})
			return
		}
		var keyRecord models.KeyData
		if err := storage.DB.First(&keyRecord, "key_id = ?", keyUUID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Key not found"})
			return
		}
		publicKey = keyRecord.PublicKey
	}

	ok, err := tss.VerifySignature(publicKey, req.Message, req.Signature)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to verify signature: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"verified": ok,
	})
}
