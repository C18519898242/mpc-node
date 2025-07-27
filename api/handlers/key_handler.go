package handlers

import (
	"encoding/base64"
	"mpc-node/internal/config"
	"mpc-node/internal/logger"
	"mpc-node/internal/storage"
	"mpc-node/internal/storage/models"
	"mpc-node/internal/tss"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// SignatureData mirrors tss-lib's SignatureData but with camelCase JSON tags.
type SignatureData struct {
	Signature         []byte `json:"signature,omitempty"`
	SignatureRecovery []byte `json:"signatureRecovery,omitempty"`
	R                 []byte `json:"r,omitempty"`
	S                 []byte `json:"s,omitempty"`
	M                 []byte `json:"m,omitempty"`
}

type SignRequest struct {
	KeyID   string `json:"keyId" binding:"required"`
	Message string `json:"message" binding:"required"`
}

type VerifyRequest struct {
	KeyID     string `json:"keyId"`
	PublicKey string `json:"publicKey"`
	Message   string `json:"message" binding:"required"`
	Signature string `json:"signature" binding:"required"`
}

func GenerateKey(c *gin.Context) {
	// Retrieve the config from the context
	cfg, exists := c.Get("config")
	if !exists {
		logger.Log.Error("Config not found in context")
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Server configuration error",
		})
		return
	}
	appConfig, ok := cfg.(*config.Config)
	if !ok {
		logger.Log.Error("Invalid config type in context")
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Invalid server configuration",
		})
		return
	}

	keyRecord, err := tss.GenerateAndSaveKey(appConfig)
	if err != nil {
		logger.Log.Errorf("Failed to generate key: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to generate key: " + err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"keyId":     keyRecord.KeyID,
		"publicKey": keyRecord.PublicKey,
	})
}

func ListKeys(c *gin.Context) {
	var keys []models.KeyData
	// We only need KeyID and PublicKey, no need to Preload Shares
	result := storage.DB.Find(&keys)
	if result.Error != nil {
		logger.Log.Errorf("Failed to retrieve keys from database: %v", result.Error)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to retrieve keys from database.",
		})
		return
	}

	// Create a custom response struct
	type KeyInfo struct {
		KeyID     string `json:"keyId"`
		PublicKey string `json:"publicKey"`
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
		logger.Log.Warnf("Key not found for ID %s: %v", keyID, result.Error)
		c.JSON(http.StatusNotFound, gin.H{"error": "key not found"})
		return
	}
	c.JSON(http.StatusOK, key)
}

func SignMessage(c *gin.Context) {
	var req SignRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		logger.Log.Errorf("SignMessage: Bad request format: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Retrieve the config from the context
	cfg, exists := c.Get("config")
	if !exists {
		logger.Log.Error("Config not found in context")
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Server configuration error",
		})
		return
	}
	appConfig, ok := cfg.(*config.Config)
	if !ok {
		logger.Log.Error("Invalid config type in context")
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Invalid server configuration",
		})
		return
	}

	keyUUID, err := uuid.Parse(req.KeyID)
	if err != nil {
		logger.Log.Errorf("Invalid KeyID format: %s", req.KeyID)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid KeyID format"})
		return
	}

	signature, err := tss.SignMessage(appConfig, keyUUID, req.Message)
	if err != nil {
		logger.Log.Errorf("Failed to sign message for key %s: %v", req.KeyID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to sign message: " + err.Error()})
		return
	}

	// Map to our custom SignatureData struct for camelCase response
	response := SignatureData{
		Signature:         signature.Signature,
		SignatureRecovery: signature.SignatureRecovery,
		R:                 signature.R,
		S:                 signature.S,
		M:                 signature.M,
	}

	c.JSON(http.StatusOK, gin.H{
		"signature": response,
	})
}

func VerifySignature(c *gin.Context) {
	var req VerifyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		logger.Log.Errorf("VerifySignature: Bad request format: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	publicKey := req.PublicKey
	// If PublicKey is not provided, try to get it from the database using KeyID
	if publicKey == "" {
		if req.KeyID == "" {
			logger.Log.Error("Verification request missing keyId and publicKey")
			c.JSON(http.StatusBadRequest, gin.H{"error": "Either keyId or publicKey must be provided"})
			return
		}
		keyUUID, err := uuid.Parse(req.KeyID)
		if err != nil {
			logger.Log.Errorf("Invalid KeyID format for verification: %s", req.KeyID)
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid KeyID format"})
			return
		}
		var keyRecord models.KeyData
		if err := storage.DB.First(&keyRecord, "key_id = ?", keyUUID).Error; err != nil {
			logger.Log.Warnf("Key not found for verification, ID %s: %v", req.KeyID, err)
			c.JSON(http.StatusNotFound, gin.H{"error": "Key not found"})
			return
		}
		publicKey = keyRecord.PublicKey
	}

	// Decode the signature from base64 and split it into R and S
	sigBytes, err := base64.StdEncoding.DecodeString(req.Signature)
	if err != nil {
		logger.Log.Errorf("Failed to decode base64 signature: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid signature format"})
		return
	}
	if len(sigBytes) != 64 {
		logger.Log.Errorf("Invalid signature length: got %d, want 64", len(sigBytes))
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid signature length"})
		return
	}
	rBytes := sigBytes[:32]
	sBytes := sigBytes[32:]

	ok, err := tss.VerifySignature(publicKey, req.Message, rBytes, sBytes)
	if err != nil {
		logger.Log.Errorf("Failed to verify signature: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to verify signature: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"verified": ok,
	})
}
