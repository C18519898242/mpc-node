package handlers

import (
	"encoding/base64"
	"encoding/json"
	"math/big"
	"mpc-node/internal/config"
	"mpc-node/internal/logger"
	"mpc-node/internal/network"
	"mpc-node/internal/party"
	"mpc-node/internal/session"
	"mpc-node/internal/storage"
	"mpc-node/internal/storage/models"
	"mpc-node/internal/tss"
	"net/http"
	"time"

	tsslib "github.com/bnb-chain/tss-lib/v2/tss"
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

type GenerateKeyRequest struct {
	SessionID    string   `json:"sessionId" binding:"required"`
	Participants []string `json:"participants" binding:"required"`
}

func GenerateKey(c *gin.Context) {
	var req GenerateKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request: " + err.Error()})
		return
	}

	// --- Retrieve services from context ---
	nodeNameVal, _ := c.Get("nodeName")
	nodeName, _ := nodeNameVal.(string)

	smVal, _ := c.Get("sessionManager")
	sm, _ := smVal.(*session.Manager)

	transportVal, _ := c.Get("transport")
	transport, _ := transportVal.(network.Transport)

	// --- Coordination Logic ---
	// Create PartyID objects for all participants
	partyIDs := make([]*tsslib.PartyID, len(req.Participants))
	for i, pName := range req.Participants {
		// The key for PartyID must be a non-nil big.Int. We can use the index.
		partyIDs[i] = tsslib.NewPartyID(pName, pName, new(big.Int).SetInt64(int64(i)))
	}

	// Sort them to ensure deterministic coordinator election
	sortedPartyIDs := tsslib.SortPartyIDs(partyIDs)
	coordinatorID := party.ElectCoordinator(req.SessionID, sortedPartyIDs)

	// Create or get the session
	session := sm.GetOrCreateSession(req.SessionID, req.Participants, coordinatorID.Id)
	logger.Log.Infof("Session %s: Current node is %s, Coordinator is %s", req.SessionID, nodeName, coordinatorID.Id)

	// Check if the current node is the coordinator
	if nodeName == coordinatorID.Id {
		// I am the coordinator
		keyID := uuid.New()
		session.KeyID = keyID.String() // Set KeyID in the local session state

		logger.Log.Infof("Coordinator %s: Generated KeyID %s for session %s. Broadcasting...", nodeName, keyID, req.SessionID)

		// Prepare broadcast message
		payload, _ := json.Marshal(network.KeyIDBroadcastPayload{KeyID: keyID.String()})
		broadcastMsg := &network.CoordinationMessage{
			Type:      network.KeyIDBroadcast,
			SessionID: req.SessionID,
			Payload:   payload,
			From:      coordinatorID,
			To:        nil, // Broadcast to all
		}

		// Broadcast the KeyID to all other parties
		if err := transport.SendCoordinationMessage(broadcastMsg); err != nil {
			logger.Log.Errorf("Coordinator failed to broadcast KeyID: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to start coordination"})
			return
		}

		// The coordinator acknowledges itself immediately.
		sm.RecordAcknowledgement(req.SessionID, nodeName)

		// Wait for the key generation to complete or timeout
		select {
		case <-session.Done:
			// The process is finished (successfully or not).
			// We can now check the final status.
			finalSession, _ := sm.GetSession(req.SessionID)
			if finalSession.Status == "Finished" {
				c.JSON(http.StatusOK, gin.H{
					"status":    "Success",
					"sessionId": finalSession.SessionID,
					"keyId":     finalSession.KeyID,
				})
			} else {
				c.JSON(http.StatusInternalServerError, gin.H{
					"status":    "Failed",
					"sessionId": finalSession.SessionID,
					"error":     "Key generation ceremony failed.",
				})
			}
		case <-time.After(60 * time.Second): // 60-second timeout
			c.JSON(http.StatusRequestTimeout, gin.H{
				"status":    "Timeout",
				"sessionId": req.SessionID,
				"error":     "Key generation timed out.",
			})
		}

	} else {
		// I am a follower
		logger.Log.Infof("Follower %s: Waiting for KeyID broadcast for session %s", nodeName, req.SessionID)
		c.JSON(http.StatusAccepted, gin.H{
			"status":    "Waiting for coordinator",
			"sessionId": req.SessionID,
		})
	}

	// The actual TSS key generation will be triggered from the network handler
	// when the coordinator receives all ACKs.
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

	// Retrieve the nodeName from the context
	nodeName, exists := c.Get("nodeName")
	if !exists {
		logger.Log.Error("Node name not found in context")
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Server configuration error: node name missing",
		})
		return
	}
	nodeNameStr, ok := nodeName.(string)
	if !ok {
		logger.Log.Error("Invalid node name type in context")
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Invalid server configuration: node name type",
		})
		return
	}

	keyUUID, err := uuid.Parse(req.KeyID)
	if err != nil {
		logger.Log.Errorf("Invalid KeyID format: %s", req.KeyID)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid KeyID format"})
		return
	}

	signature, err := tss.SignMessage(appConfig, nodeNameStr, keyUUID, req.Message)
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
