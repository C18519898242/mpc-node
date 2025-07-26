package handlers

import (
	"mpc-node/internal/logger"
	"mpc-node/internal/storage"
	"mpc-node/internal/storage/models"
	"mpc-node/internal/tss"
	"net/http"

	"github.com/bnb-chain/tss-lib/v2/common"
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
	KeyID     string        `json:"keyId"`
	PublicKey string        `json:"publicKey"`
	Message   string        `json:"message" binding:"required"`
	Signature SignatureData `json:"signature"`
}

func GenerateKey(c *gin.Context) {
	keyRecord, err := tss.GenerateAndSaveKey()
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

	keyUUID, err := uuid.Parse(req.KeyID)
	if err != nil {
		logger.Log.Errorf("Invalid KeyID format: %s", req.KeyID)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid KeyID format"})
		return
	}

	signature, err := tss.SignMessage(keyUUID, req.Message)
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

	// Map our custom SignatureData to the tss-lib struct
	tssSignature := common.SignatureData{
		Signature:         req.Signature.Signature,
		SignatureRecovery: req.Signature.SignatureRecovery,
		R:                 req.Signature.R,
		S:                 req.Signature.S,
		M:                 req.Signature.M,
	}

	ok, err := tss.VerifySignature(publicKey, req.Message, tssSignature)
	if err != nil {
		logger.Log.Errorf("Failed to verify signature: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to verify signature: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"verified": ok,
	})
}
