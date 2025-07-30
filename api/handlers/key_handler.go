package handlers

import (
	"encoding/base64"
	"encoding/json"
	"math/big"
	"mpc-node/internal/logger"
	"mpc-node/internal/network"
	"mpc-node/internal/party"
	"mpc-node/internal/session"
	"mpc-node/internal/storage"
	"mpc-node/internal/storage/models"
	"mpc-node/internal/tss"
	"net/http"
	"strings"
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

		// Create a placeholder KeyData record in the database immediately.
		// This ensures that foreign key constraints are met when followers save their shares.
		placeholderKey := models.KeyData{
			KeyID:     keyID,
			PartyIDs:  strings.Join(req.Participants, ","),
			Threshold: 1, // This will be updated later by the coordinator
			PublicKey: "placeholder-" + keyID.String(),
		}
		if err := storage.DB.Create(&placeholderKey).Error; err != nil {
			logger.Log.Errorf("Coordinator failed to create placeholder key record: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to initialize key record"})
			return
		}

		logger.Log.Infof("Coordinator %s: Generated KeyID %s and created placeholder record for session %s. Broadcasting...", nodeName, keyID, req.SessionID)

		// Prepare broadcast message
		payload, _ := json.Marshal(network.KeyIDBroadcastPayload{
			KeyID:        keyID.String(),
			Participants: req.Participants,
		})
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

	// --- Retrieve services from context ---
	nodeName, _ := c.Get("nodeName")
	nodeNameStr, _ := nodeName.(string)

	sm, _ := c.Get("sessionManager")
	sessionManager, _ := sm.(*session.Manager)

	transport, _ := c.Get("transport")
	transportVal, _ := transport.(network.Transport)

	// --- 1. Find the key and its participants ---
	keyUUID, err := uuid.Parse(req.KeyID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid KeyID format"})
		return
	}
	var keyRecord models.KeyData
	if err := storage.DB.First(&keyRecord, "key_id = ?", keyUUID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "key not found"})
		return
	}
	participants := strings.Split(keyRecord.PartyIDs, ",")

	// --- 2. Create a new signing session ---
	sessionID := uuid.New().String()
	// For signing, any node can be the coordinator. The node receiving the API call is the natural choice.
	signingSession := sessionManager.GetOrCreateSession(sessionID, participants, nodeNameStr)
	signingSession.KeyID = req.KeyID
	signingSession.MessageToSign = req.Message

	// --- 3. Broadcast the request to sign ---
	payload, _ := json.Marshal(network.RequestSignaturePayload{
		KeyID:   req.KeyID,
		Message: req.Message,
	})

	// Find current party ID
	var currentPartyID *tsslib.PartyID
	// This is a simplification. A robust solution would use sorted party IDs.
	currentPartyID = tsslib.NewPartyID(nodeNameStr, nodeNameStr, new(big.Int).SetInt64(1))

	broadcastMsg := &network.CoordinationMessage{
		Type:      network.RequestSignature,
		SessionID: sessionID,
		Payload:   payload,
		From:      currentPartyID,
		To:        nil, // Broadcast to all
	}

	if err := transportVal.SendCoordinationMessage(broadcastMsg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to start signing ceremony"})
		return
	}

	// --- 4. Wait for the result ---
	select {
	case result := <-signingSession.SignatureResult:
		if result.Error != "" {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate signature: " + result.Error})
			return
		}
		// Map to our custom SignatureData struct for camelCase response
		response := SignatureData{
			Signature:         result.Signature.Signature,
			SignatureRecovery: result.Signature.SignatureRecovery,
			R:                 result.Signature.R,
			S:                 result.Signature.S,
			M:                 result.Signature.M,
		}
		c.JSON(http.StatusOK, gin.H{"signature": response})

	case <-time.After(60 * time.Second): // Timeout
		c.JSON(http.StatusRequestTimeout, gin.H{"error": "Signing ceremony timed out"})
	}
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
