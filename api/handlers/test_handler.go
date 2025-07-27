package handlers

import (
	"fmt"
	"mpc-node/internal/logger"
	"net"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

var tcpPorts = []string{":8001", ":8002", ":8003"}

type TestMessage struct {
	Type    string `json:"type" binding:"required"`
	Message string `json:"message" binding:"required"`
	From    string `json:"from"` // Example: ":8001"
	To      string `json:"to"`   // Example: ":8002"
}

// sendMessage connects to a TCP port and sends a message.
func sendMessage(address, message string) error {
	conn, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err != nil {
		return fmt.Errorf("failed to connect to %s: %v", address, err)
	}
	defer conn.Close()

	_, err = conn.Write([]byte(message))
	if err != nil {
		return fmt.Errorf("failed to send message to %s: %v", address, err)
	}
	logger.Log.Infof("Successfully sent message '%s' to %s", message, address)
	return nil
}

// TestCommunication handles requests to test network communication.
func TestCommunication(c *gin.Context) {
	var json TestMessage
	if err := c.ShouldBindJSON(&json); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	switch json.Type {
	case "broadcast":
		logger.Log.Infof("--- Initiating Broadcast Test ---")
		successCount := 0
		errorMessages := []string{}
		for _, port := range tcpPorts {
			msg := fmt.Sprintf("Broadcast from API: %s", json.Message)
			if err := sendMessage(port, msg); err != nil {
				logger.Log.Errorf("Broadcast to %s failed: %v", port, err)
				errorMessages = append(errorMessages, err.Error())
			} else {
				successCount++
			}
		}
		c.JSON(http.StatusOK, gin.H{
			"status":     "Broadcast test finished",
			"successful": successCount,
			"failed":     len(tcpPorts) - successCount,
			"errors":     errorMessages,
		})

	case "p2p":
		logger.Log.Infof("--- Initiating P2P Test ---")
		if json.To == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "'to' field is required for p2p type"})
			return
		}
		msg := fmt.Sprintf("P2P message from %s: %s", json.From, json.Message)
		if err := sendMessage(json.To, msg); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"status": "P2P test failed",
				"error":  err.Error(),
			})
		} else {
			c.JSON(http.StatusOK, gin.H{"status": "P2P message sent successfully"})
		}

	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid message type, must be 'broadcast' or 'p2p'"})
	}
}
