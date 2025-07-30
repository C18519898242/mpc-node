package network

import (
	"encoding/json"
	"mpc-node/internal/config"
	"mpc-node/internal/dto"
	"mpc-node/internal/logger"
	"mpc-node/internal/party"
	"mpc-node/internal/session"
	"mpc-node/internal/storage"
	"mpc-node/internal/storage/models"
	"mpc-node/internal/tss"
	"net"
	"strings"

	"github.com/bnb-chain/tss-lib/v2/common"
	tsslib "github.com/bnb-chain/tss-lib/v2/tss"
	"github.com/google/uuid"
)

// Server manages the TCP connections and message handling.
type Server struct {
	cfg            *config.Config
	nodeName       string
	sessionManager *session.Manager
	transport      Transport
}

// NewServer creates a new TCP server instance.
func NewServer(cfg *config.Config, nodeName string, sm *session.Manager, transport Transport) *Server {
	return &Server{
		cfg:            cfg,
		nodeName:       nodeName,
		sessionManager: sm,
		transport:      transport,
	}
}

// Start listens for incoming TCP connections and handles them.
func (s *Server) Start(listenAddr string) {
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		logger.Log.Fatalf("Failed to listen on %s: %v", listenAddr, err)
	}
	defer ln.Close()
	logger.Log.Infof("TCP server listening on %s", listenAddr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			logger.Log.Errorf("TCP accept error: %v", err)
			continue
		}
		go s.handleTCPConnection(conn)
	}
}

func (s *Server) handleTCPConnection(conn net.Conn) {
	defer conn.Close()
	localAddr := conn.LocalAddr().String()
	port := ":" + strings.Split(localAddr, ":")[1]
	logger.Log.Infof("Accepted TCP connection on %s from %s", port, conn.RemoteAddr())

	decoder := json.NewDecoder(conn)
	var wireMsg WireMessage
	if err := decoder.Decode(&wireMsg); err != nil {
		logger.Log.Errorf("Failed to decode wire message on %s: %v", port, err)
		return
	}

	switch wireMsg.MessageType {
	case "TSS":
		handleTSSMessage(wireMsg, port)
	case "Coordination":
		s.handleCoordinationMessage(wireMsg)
	default:
		logger.Log.Errorf("Unknown message type received: %s", wireMsg.MessageType)
	}
}

func handleTSSMessage(wireMsg WireMessage, port string) {
	var tssMsg struct {
		From        *tsslib.PartyID
		IsBroadcast bool
		Payload     []byte
	}
	if err := json.Unmarshal(wireMsg.Payload, &tssMsg); err != nil {
		logger.Log.Errorf("Failed to unmarshal TSS message payload: %v", err)
		return
	}

	pMsg, err := tsslib.ParseWireMessage(tssMsg.Payload, tssMsg.From, tssMsg.IsBroadcast)
	if err != nil {
		logger.Log.Errorf("Failed to parse TSS message on %s: %v", port, err)
		return
	}

	partyCh, ok := party.DefaultRegistry.Get(port)
	if !ok {
		logger.Log.Warnf("No active party ceremony for address %s. Dropping message.", port)
		return
	}

	logger.Log.Infof("Routing TSS message from %s to party on %s", tssMsg.From.Id, port)
	partyCh <- pMsg
}

func (s *Server) handleCoordinationMessage(wireMsg WireMessage) {
	var coordMsg CoordinationMessage
	if err := json.Unmarshal(wireMsg.Payload, &coordMsg); err != nil {
		logger.Log.Errorf("Failed to unmarshal coordination message: %v", err)
		return
	}

	logger.Log.Infof("Handling coordination message type %s for session %s", coordMsg.Type, coordMsg.SessionID)

	switch coordMsg.Type {
	case KeyIDBroadcast:
		var payload KeyIDBroadcastPayload
		if err := json.Unmarshal(coordMsg.Payload, &payload); err != nil {
			logger.Log.Errorf("Failed to unmarshal KeyIDBroadcast payload: %v", err)
			return
		}
		keyUUID, err := uuid.Parse(payload.KeyID)
		if err != nil {
			logger.Log.Errorf("Invalid KeyID in broadcast: %v", err)
			return
		}

		// Store the KeyID in the session
		s.sessionManager.SetKeyID(coordMsg.SessionID, keyUUID)

		// Follower nodes must also create a placeholder record to satisfy foreign key constraints.
		// The coordinator will have already created its own, so it should skip this step.
		session, ok := s.sessionManager.GetSession(coordMsg.SessionID)
		if !ok {
			logger.Log.Warnf("Received key id broadcast for unknown session %s", coordMsg.SessionID)
			return
		}

		if s.nodeName != session.Coordinator {
			placeholderKey := models.KeyData{
				KeyID:     keyUUID,
				PartyIDs:  strings.Join(payload.Participants, ","),
				Threshold: 1, // Placeholder
				PublicKey: "placeholder-" + keyUUID.String(),
			}
			if err := storage.DB.Create(&placeholderKey).Error; err != nil {
				logger.Log.Errorf("Node %s failed to create placeholder key record: %v", s.nodeName, err)
				// Do not send an ACK if we failed to prepare our database.
				return
			}
			logger.Log.Infof("Follower node %s created placeholder KeyID %s for session %s", s.nodeName, payload.KeyID, coordMsg.SessionID)
		} else {
			logger.Log.Infof("Coordinator node %s received its own KeyID broadcast for session %s. Skipping placeholder creation.", s.nodeName, coordMsg.SessionID)
		}

		// Find current party ID
		currentPartyID, _, err := tss.CreatePartyIDs(payload.Participants, s.nodeName)
		if err != nil {
			logger.Log.Errorf("Failed to create party ID for ACK: %v", err)
			return
		}

		ackMsg := &CoordinationMessage{
			Type:      KeyIDAck,
			SessionID: coordMsg.SessionID,
			From:      currentPartyID,
			To:        []*tsslib.PartyID{coordMsg.From}, // Send ACK back to the coordinator
		}
		if err := s.transport.SendCoordinationMessage(ackMsg); err != nil {
			logger.Log.Errorf("Failed to send KeyID ACK: %v", err)
		}

	case KeyIDAck:
		// This is handled by the coordinator
		allAcksIn := s.sessionManager.RecordAcknowledgement(coordMsg.SessionID, coordMsg.From.Id)
		logger.Log.Infof("Coordinator received ACK for session %s from %s. All ACKs received: %t", coordMsg.SessionID, coordMsg.From.Id, allAcksIn)
		if allAcksIn {
			logger.Log.Infof("All parties acknowledged. Broadcasting StartKeygen for session %s.", coordMsg.SessionID)
			session, ok := s.sessionManager.GetSession(coordMsg.SessionID)
			if !ok {
				logger.Log.Errorf("Session %s not found for starting keygen", coordMsg.SessionID)
				return
			}

			// Find coordinator's party ID to set as the 'From' field
			coordinatorPartyID, _, err := tss.CreatePartyIDs(session.Participants, session.Coordinator)
			if err != nil {
				logger.Log.Errorf("Could not create party ID for coordinator %s: %v", session.Coordinator, err)
				return
			}
			if coordinatorPartyID == nil { // Should be redundant due to error check, but good practice
				logger.Log.Errorf("Could not find party ID for coordinator %s", session.Coordinator)
				return
			}

			startKeygenMsg := &CoordinationMessage{
				Type:      StartKeygen,
				SessionID: coordMsg.SessionID,
				From:      coordinatorPartyID,
				To:        nil, // Broadcast to all parties
			}
			// The coordinator sends to all, and will also receive it and start the process.
			if err := s.transport.SendCoordinationMessage(startKeygenMsg); err != nil {
				logger.Log.Errorf("Coordinator failed to broadcast StartKeygen: %v", err)
			}
		}

	case StartKeygen:
		logger.Log.Infof("Received StartKeygen for session %s. Triggering TSS key generation.", coordMsg.SessionID)
		session, ok := s.sessionManager.GetSession(coordMsg.SessionID)
		if !ok {
			logger.Log.Errorf("Session %s not found for keygen", coordMsg.SessionID)
			return
		}
		keyUUID, err := uuid.Parse(session.KeyID)
		if err != nil {
			logger.Log.Errorf("Invalid KeyID in session %s: %v", coordMsg.SessionID, err)
			return
		}

		// This needs to run in a goroutine so it doesn't block the network handler
		go s.runKeyGeneration(session, coordMsg.SessionID, keyUUID)

	case RequestSignature:
		var payload RequestSignaturePayload
		if err := json.Unmarshal(coordMsg.Payload, &payload); err != nil {
			logger.Log.Errorf("Failed to unmarshal RequestSignature payload: %v", err)
			return
		}
		session, ok := s.sessionManager.GetSession(coordMsg.SessionID)
		if !ok {
			logger.Log.Warnf("Received signature request for unknown session %s", coordMsg.SessionID)
			return
		}
		session.MessageToSign = payload.Message
		session.KeyID = payload.KeyID

		go s.runSigningCeremony(session)

	case SignatureShare:
		// This message is received by the coordinator from all parties (including itself)
		var payload common.SignatureData
		if err := json.Unmarshal(coordMsg.Payload, &payload); err != nil {
			logger.Log.Errorf("Failed to unmarshal SignatureShare payload: %v", err)
			return
		}

		logger.Log.Infof("Coordinator received signature share from %s for session %s", coordMsg.From.Id, coordMsg.SessionID)
		s.handleSignatureShare(&coordMsg, &payload)

	case KeygenPublicDataShare:
		// This message is received by the coordinator from all parties (including itself)
		var payload KeygenPublicDataPayload
		if err := json.Unmarshal(coordMsg.Payload, &payload); err != nil {
			logger.Log.Errorf("Failed to unmarshal KeygenPublicDataShare payload: %v", err)
			return
		}

		logger.Log.Infof("Coordinator received public data share from %s for session %s", coordMsg.From.Id, coordMsg.SessionID)
		s.handlePublicDataShare(&coordMsg, &payload)

	case KeygenResultBroadcast:
		// This is received by followers from the coordinator
		session, ok := s.sessionManager.GetSession(coordMsg.SessionID)
		if !ok {
			logger.Log.Warnf("Received keygen result for unknown session %s", coordMsg.SessionID)
			return
		}
		// The coordinator already has the final data, so it can ignore this.
		if s.nodeName == session.Coordinator {
			return
		}

		var payload KeygenResultBroadcastPayload
		if err := json.Unmarshal(coordMsg.Payload, &payload); err != nil {
			logger.Log.Errorf("Failed to unmarshal KeygenResultBroadcast payload: %v", err)
			return
		}

		keyUUID, _ := uuid.Parse(session.KeyID)
		updateData := map[string]interface{}{
			"public_key":     payload.PublicKey,
			"full_save_data": payload.FullSaveData,
		}

		if err := storage.DB.Model(&models.KeyData{}).Where("key_id = ?", keyUUID).Updates(updateData).Error; err != nil {
			logger.Log.Errorf("Follower %s failed to update final key record: %v", s.nodeName, err)
			return
		}
		logger.Log.Infof("Follower %s successfully updated final key record for %s", s.nodeName, session.KeyID)
	}
}

func (s *Server) handlePublicDataShare(coordMsg *CoordinationMessage, payload *KeygenPublicDataPayload) {
	session, ok := s.sessionManager.GetSession(coordMsg.SessionID)
	if !ok {
		logger.Log.Errorf("Session %s not found for public data share", coordMsg.SessionID)
		return
	}

	// This function should only be executed by the coordinator
	if s.nodeName != session.Coordinator {
		return
	}

	// Store the received public data share
	session.PublicDataShares[coordMsg.From.Id] = payload.SaveData

	logger.Log.Infof("Coordinator has %d/%d public data shares for session %s.", len(session.PublicDataShares), len(session.Participants), coordMsg.SessionID)

	// Check if we have received shares from all participants
	if len(session.PublicDataShares) != len(session.Participants) {
		return // Not all shares received yet, wait for more.
	}

	logger.Log.Infof("All public data shares received for session %s. Combining and saving the key.", coordMsg.SessionID)

	// --- Combine all public data into a single SaveData object ---
	// The coordinator creates a final SaveData object that contains the combined public information.
	// The private key share (Xi) is not included, as the coordinator doesn't have it.
	// This combined object is what's needed for signing.
	combinedSaveData := tss.CombinePublicData(session.PublicDataShares)
	if combinedSaveData == nil {
		logger.Log.Errorf("Failed to combine public data for session %s", coordMsg.SessionID)
		s.sessionManager.UpdateStatus(coordMsg.SessionID, "Failed")
		return
	}

	// Marshal the combined data
	fullSaveDataBytes, err := json.Marshal(combinedSaveData)
	if err != nil {
		logger.Log.Errorf("Failed to marshal combined save data: %v", err)
		s.sessionManager.UpdateStatus(coordMsg.SessionID, "Failed")
		return
	}

	// --- Update the placeholder KeyData record with the final data ---
	keyUUID, _ := uuid.Parse(session.KeyID)
	updateData := map[string]interface{}{
		"public_key":     combinedSaveData.ECDSAPub.Y().String(), // A simplified representation
		"full_save_data": fullSaveDataBytes,
	}

	if err := storage.DB.Model(&models.KeyData{}).Where("key_id = ?", keyUUID).Updates(updateData).Error; err != nil {
		logger.Log.Errorf("Coordinator failed to update final key record: %v", err)
		s.sessionManager.UpdateStatus(coordMsg.SessionID, "Failed")
		return
	}

	logger.Log.Infof("Coordinator successfully saved final key %s to the database.", session.KeyID)
	s.sessionManager.UpdateStatus(coordMsg.SessionID, "Finished")

	// --- Broadcast the final result to all participants ---
	resultPayload := KeygenResultBroadcastPayload{
		PublicKey:    combinedSaveData.ECDSAPub.Y().String(),
		FullSaveData: fullSaveDataBytes,
	}
	resultPayloadBytes, err := json.Marshal(resultPayload)
	if err != nil {
		logger.Log.Errorf("Coordinator failed to marshal result payload: %v", err)
		return // The key is saved, but followers won't get the update.
	}

	// Find coordinator's party ID to set as the 'From' field
	coordinatorPartyID, _, err := tss.CreatePartyIDs(session.Participants, session.Coordinator)
	if err != nil {
		logger.Log.Errorf("Coordinator failed to create its own party ID for result broadcast: %v", err)
		return
	}

	resultMsg := &CoordinationMessage{
		Type:      KeygenResultBroadcast,
		SessionID: coordMsg.SessionID,
		Payload:   resultPayloadBytes,
		From:      coordinatorPartyID,
		To:        nil, // Broadcast
	}

	if err := s.transport.SendCoordinationMessage(resultMsg); err != nil {
		logger.Log.Errorf("Coordinator failed to broadcast keygen result: %v", err)
		// If broadcast fails, we should still signal completion, but maybe with a failed status.
		// For now, we'll still close the channel.
		s.sessionManager.UpdateStatus(coordMsg.SessionID, "Failed")
		close(session.Done)
		return
	}

	// Signal to the original HTTP handler that the process is complete.
	close(session.Done)
}

func (s *Server) runKeyGeneration(session *session.SessionState, sessionID string, keyUUID uuid.UUID) {
	// The original HTTP handler only closes the Done channel for the coordinator.
	// This is now handled in `handlePublicDataShare` after the final result is broadcast.
	isCoordinator := s.nodeName == session.Coordinator

	localSaveData, err := tss.GenerateAndSaveKey(s.cfg, s.nodeName, session.Participants, s.transport)
	if err != nil {
		logger.Log.Errorf("TSS key generation failed for session %s: %v", sessionID, err)
		if isCoordinator {
			s.sessionManager.UpdateStatus(sessionID, "Failed")
		}
		return
	}

	// --- 1. Save the local share to the database ---
	shareBytes, err := json.Marshal(localSaveData)
	if err != nil {
		logger.Log.Errorf("Failed to marshal local save data: %v", err)
		if isCoordinator {
			s.sessionManager.UpdateStatus(sessionID, "Failed")
		}
		return
	}
	keyShare := models.KeyShare{
		KeyDataID: keyUUID,
		ShareData: shareBytes,
		PartyID:   s.nodeName,
	}
	if err := storage.DB.Create(&keyShare).Error; err != nil {
		logger.Log.Errorf("Failed to save key share to DB: %v", err)
		if isCoordinator {
			s.sessionManager.UpdateStatus(sessionID, "Failed")
		}
		return
	}
	logger.Log.Infof("Successfully saved local key share for key %s", keyUUID)

	// --- 2. Send the public data to the coordinator ---
	payloadBytes, err := json.Marshal(KeygenPublicDataPayload{SaveData: localSaveData})
	if err != nil {
		logger.Log.Errorf("Failed to marshal public data payload: %v", err)
		if isCoordinator {
			s.sessionManager.UpdateStatus(sessionID, "Failed")
		}
		return
	}

	// Find current party ID and coordinator party ID
	currentPartyID, _, err := tss.CreatePartyIDs(session.Participants, s.nodeName)
	if err != nil {
		logger.Log.Errorf("Could not determine party IDs for keygen public data share: %v", err)
		if isCoordinator {
			s.sessionManager.UpdateStatus(sessionID, "Failed")
		}
		return
	}
	coordinatorPartyID, _, err := tss.CreatePartyIDs(session.Participants, session.Coordinator)
	if err != nil {
		logger.Log.Errorf("Could not determine coordinator party ID for keygen public data share: %v", err)
		if isCoordinator {
			s.sessionManager.UpdateStatus(sessionID, "Failed")
		}
		return
	}
	if currentPartyID == nil || coordinatorPartyID == nil { // Should be redundant
		logger.Log.Error("Could not determine party IDs for keygen public data share")
		if isCoordinator {
			s.sessionManager.UpdateStatus(sessionID, "Failed")
		}
		return
	}

	msg := &CoordinationMessage{
		Type:      KeygenPublicDataShare,
		SessionID: sessionID,
		Payload:   payloadBytes,
		From:      currentPartyID,
		To:        []*tsslib.PartyID{coordinatorPartyID},
	}

	if err := s.transport.SendCoordinationMessage(msg); err != nil {
		logger.Log.Errorf("Failed to send public data to coordinator: %v", err)
		if isCoordinator {
			s.sessionManager.UpdateStatus(sessionID, "Failed")
		}
		return
	}

	logger.Log.Infof("Node %s sent public data to coordinator %s", s.nodeName, session.Coordinator)

	// If this is a follower node, its job is done.
	// If this is the coordinator, it will now wait for messages from all other nodes.
	// The final status update will happen in the KeygenPublicDataShare handler.
}

func (s *Server) runSigningCeremony(session *session.SessionState) {
	logger.Log.Infof("Node %s: Starting signing ceremony for session %s", s.nodeName, session.SessionID)

	keyUUID, err := uuid.Parse(session.KeyID)
	if err != nil {
		logger.Log.Errorf("Invalid KeyID in signing session %s: %v", session.SessionID, err)
		return
	}

	// The tss.SignMessage function now encapsulates the logic for a single party to participate.
	// We need to rename/refactor it to reflect this. Let's call it `ParticipateInSigning`.
	signature, err := tss.SignMessage(s.cfg, s.nodeName, keyUUID, session.MessageToSign, s.transport)
	if err != nil {
		logger.Log.Errorf("TSS signing failed for session %s: %v", session.SessionID, err)
		// Inform the coordinator about the failure? For now, we just log.
		return
	}

	// --- Send the signature share to the coordinator ---
	payloadBytes, err := json.Marshal(signature)
	if err != nil {
		logger.Log.Errorf("Failed to marshal signature share payload: %v", err)
		return
	}

	// Find current party ID and coordinator party ID
	currentPartyID, _, err := tss.CreatePartyIDs(session.Participants, s.nodeName)
	if err != nil {
		logger.Log.Errorf("Could not determine party IDs for signature share: %v", err)
		return
	}
	coordinatorPartyID, _, err := tss.CreatePartyIDs(session.Participants, session.Coordinator)
	if err != nil {
		logger.Log.Errorf("Could not determine coordinator party ID for signature share: %v", err)
		return
	}

	if currentPartyID == nil || coordinatorPartyID == nil { // Should be redundant
		logger.Log.Error("Could not determine party IDs for signature share")
		return
	}

	msg := &CoordinationMessage{
		Type:      SignatureShare,
		SessionID: session.SessionID,
		Payload:   payloadBytes,
		From:      currentPartyID,
		To:        []*tsslib.PartyID{coordinatorPartyID},
	}

	if err := s.transport.SendCoordinationMessage(msg); err != nil {
		logger.Log.Errorf("Failed to send signature share to coordinator: %v", err)
		return
	}

	logger.Log.Infof("Node %s sent signature share to coordinator %s for session %s", s.nodeName, session.Coordinator, session.SessionID)
}

func (s *Server) handleSignatureShare(coordMsg *CoordinationMessage, payload *common.SignatureData) {
	session, ok := s.sessionManager.GetSession(coordMsg.SessionID)
	if !ok {
		logger.Log.Errorf("Session %s not found for signature share", coordMsg.SessionID)
		return
	}

	// This function should only be executed by the coordinator
	if s.nodeName != session.Coordinator {
		return
	}

	// Store the received signature share
	session.SignatureShares[coordMsg.From.Id] = payload

	logger.Log.Infof("Coordinator has %d/%d signature shares for session %s.", len(session.SignatureShares), len(session.Participants), coordMsg.SessionID)

	// In ECDSA, the first party to output a signature has the full, final signature.
	// There's no need to wait for all shares. The first one is sufficient.
	if len(session.SignatureShares) >= 1 {
		logger.Log.Infof("First signature share received for session %s. Sending result to API handler.", coordMsg.SessionID)

		// The payload is the complete signature
		finalSignature := payload

		// Send the result back to the waiting API handler
		session.SignatureResult <- &dto.SignatureResponsePayload{
			Signature: finalSignature,
		}
		close(session.SignatureResult) // Close channel to signal completion

		// Optionally, broadcast the final signature to other participants if they need it.
		// For now, we'll skip this step.
		s.sessionManager.UpdateStatus(coordMsg.SessionID, "Finished")
	}
}
