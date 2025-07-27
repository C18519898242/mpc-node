package tss

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"mpc-node/internal/config"
	"mpc-node/internal/logger"
	"mpc-node/internal/network"
	"mpc-node/internal/party"
	"mpc-node/internal/storage"
	"mpc-node/internal/storage/models"
	"net"
	"strings"
	"sync"

	"github.com/bnb-chain/tss-lib/v2/common"
	"github.com/bnb-chain/tss-lib/v2/ecdsa/keygen"
	"github.com/bnb-chain/tss-lib/v2/ecdsa/signing"
	"github.com/bnb-chain/tss-lib/v2/tss"
	"github.com/google/uuid"
)

const (
	partiesCount = 3
	threshold    = 1
)

// GenerateAndSaveKey performs the TSS key generation using real network communication
// and saves the result to the database.
func GenerateAndSaveKey(cfg *config.Config, nodeName string) (*models.KeyData, error) {
	logger.Log.Info("--- Phase 1: Setup for Real Network Key Generation ---")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // Ensure all goroutines are signalled to stop when we exit

	// Find the configuration for the current node
	var currentNode *config.Node
	for _, node := range cfg.Nodes {
		if node.Node == nodeName {
			currentNode = &node
			break
		}
	}
	if currentNode == nil {
		return nil, fmt.Errorf("configuration for node '%s' not found", nodeName)
	}

	// 1. --- Party and Network Setup ---
	allPartyConfs := append(currentNode.Parties, config.Party{
		Name:   currentNode.Node,
		Port:   fmt.Sprintf("%d", currentNode.Port),
		Server: "127.0.0.1", // Assuming self is always local for now
	})

	partyIDs := make(tss.UnSortedPartyIDs, len(allPartyConfs))
	partyIDMap := make(map[string]string, len(allPartyConfs))
	for i, p := range allPartyConfs {
		pID := tss.NewPartyID(p.Name, p.Name, new(big.Int).SetInt64(int64(i+1)))
		partyIDs[i] = pID
		partyIDMap[pID.Id] = net.JoinHostPort(p.Server, p.Port)
	}
	sortedPartyIDs := tss.SortPartyIDs(partyIDs)

	// Find the PartyID for the current node
	var currentPartyID *tss.PartyID
	for _, pID := range sortedPartyIDs {
		if pID.Id == currentNode.Node {
			currentPartyID = pID
			break
		}
	}
	if currentPartyID == nil {
		return nil, fmt.Errorf("could not find party ID for current node '%s'", nodeName)
	}

	errCh := make(chan *tss.Error, len(sortedPartyIDs))
	endCh := make(chan *keygen.LocalPartySaveData, len(sortedPartyIDs))

	transport := network.NewTCPTransport(partyIDMap)
	parties := make([]tss.Party, 0, len(sortedPartyIDs))

	// 2. --- Create and Register Parties ---
	// In a real distributed setup, we would only create ONE party here - the local one.
	// The other parties would be running on other machines.
	// For this simulation, we create all of them.
	for _, pID := range sortedPartyIDs {
		params := tss.NewParameters(tss.S256(), tss.NewPeerContext(sortedPartyIDs), pID, len(sortedPartyIDs), threshold)
		outCh := make(chan tss.Message, len(sortedPartyIDs))
		inCh := make(chan tss.ParsedMessage, len(sortedPartyIDs))

		p := keygen.NewLocalParty(params, outCh, endCh)
		parties = append(parties, p)

		// Find the port for this party and register it
		var partyPort string
		if pID.Id == currentNode.Node {
			partyPort = fmt.Sprintf(":%d", currentNode.Port)
		} else {
			for _, remoteParty := range currentNode.Parties {
				if remoteParty.Name == pID.Id {
					partyPort = ":" + remoteParty.Port
					break
				}
			}
		}
		if partyPort == "" {
			return nil, fmt.Errorf("could not find port for party %s", pID.Id)
		}
		party.DefaultRegistry.Register(partyPort, inCh)

		// Start a goroutine to handle message transport for this party
		go func(p tss.Party, pTransport network.Transport, pOutCh <-chan tss.Message, pInCh <-chan tss.ParsedMessage, pCtx context.Context) {
			defer logger.Log.Infof("Party %s transport goroutine finished.", p.PartyID())
			for {
				select {
				case msg := <-pOutCh:
					if err := pTransport.Send(msg); err != nil {
						errCh <- p.WrapError(err)
						return
					}
				case msg := <-pInCh:
					// The `Update` method may block, so we run it in a new goroutine
					go func(p tss.Party, msg tss.ParsedMessage) {
						if _, err := p.Update(msg); err != nil {
							errCh <- p.WrapError(err)
						}
					}(p, msg)
				case <-pCtx.Done():
					return
				}
			}
		}(p, transport, outCh, inCh, ctx)
	}

	// 3. --- Start all parties ---
	var startWg sync.WaitGroup
	startWg.Add(len(parties))
	for _, p := range parties {
		go func(p tss.Party) {
			defer startWg.Done()
			if err := p.Start(); err != nil {
				errCh <- err
			}
		}(p)
	}

	// 4. --- Wait for all parties to finish ---
	saveData := make([]*keygen.LocalPartySaveData, 0, len(sortedPartyIDs))
	finishedParties := 0
	for finishedParties < len(sortedPartyIDs) {
		select {
		case save := <-endCh:
			saveData = append(saveData, save)
			finishedParties++
		case err := <-errCh:
			logger.Log.Errorf("Error during keygen: %v", err)
			// Deregister all parties on error
			for _, pID := range sortedPartyIDs {
				port, ok := partyIDMap[pID.Id]
				if ok {
					party.DefaultRegistry.Deregister(port)
				}
			}
			return nil, err
		}
	}

	startWg.Wait()

	// Deregister all parties now that they are finished
	for _, pConf := range allPartyConfs {
		port := ":" + pConf.Port
		party.DefaultRegistry.Deregister(port)
	}

	logger.Log.Info("Key generation successful!")
	pubKey := saveData[0].ECDSAPub
	pubKeyHex := hex.EncodeToString(pubKey.X().Bytes()) + hex.EncodeToString(pubKey.Y().Bytes())
	logger.Log.Infof("Generated Public Key: %s", pubKeyHex)

	var pIDs []string
	for _, pID := range partyIDs {
		pIDs = append(pIDs, pID.Id)
	}

	keyRecord := models.KeyData{
		KeyID:     uuid.New(),
		PublicKey: pubKeyHex,
		PartyIDs:  strings.Join(pIDs, ","),
		Threshold: threshold,
	}

	// 5. --- Save results to DB ---
	tx := storage.DB.Begin()
	if tx.Error != nil {
		return nil, fmt.Errorf("failed to begin transaction: %v", tx.Error)
	}

	if err := tx.Create(&keyRecord).Error; err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("failed to create key record: %v", err)
	}

	for _, sd := range saveData {
		// Find the party ID string for the given share by matching the share ID
		var pIDStr string
		partyIndex := -1
		for i, k := range sd.Ks {
			if k.Cmp(sd.ShareID) == 0 {
				partyIndex = i
				break
			}
		}
		if partyIndex == -1 {
			tx.Rollback()
			return nil, fmt.Errorf("could not find party for share")
		}
		pIDStr = sortedPartyIDs[partyIndex].Id

		shareBytes, err := json.Marshal(sd)
		if err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("failed to marshal share data for party %s: %v", pIDStr, err)
		}
		keyShare := models.KeyShare{
			KeyDataID: keyRecord.KeyID,
			ShareData: shareBytes,
			PartyID:   pIDStr,
		}
		if err := tx.Create(&keyShare).Error; err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("failed to create key share for party %s: %v", pIDStr, err)
		}
	}

	if err := tx.Commit().Error; err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %v", err)
	}

	logger.Log.Info("Key and shares successfully saved to database.")

	var finalRecord models.KeyData
	err := storage.DB.Preload("Shares").First(&finalRecord, "key_id = ?", keyRecord.KeyID).Error
	if err != nil {
		return nil, fmt.Errorf("failed to preload shares for the new key: %v", err)
	}

	return &finalRecord, nil
}

func SignMessage(cfg *config.Config, nodeName string, keyID uuid.UUID, message string) (*common.SignatureData, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. --- Load Key and Shares from DB ---
	var keyRecord models.KeyData
	err := storage.DB.Preload("Shares").First(&keyRecord, "key_id = ?", keyID).Error
	if err != nil {
		return nil, fmt.Errorf("failed to find key with ID %s: %v", keyID, err)
	}
	if len(keyRecord.Shares) == 0 {
		return nil, fmt.Errorf("no shares found for key with ID %s", keyID)
	}

	// 2. --- Deserialize Shares into a map for easy lookup ---
	saveDataMap := make(map[string]*keygen.LocalPartySaveData, len(keyRecord.Shares))
	for _, share := range keyRecord.Shares {
		var shareData keygen.LocalPartySaveData
		if err := json.Unmarshal(share.ShareData, &shareData); err != nil {
			return nil, fmt.Errorf("failed to unmarshal share data for party %s: %v", share.PartyID, err)
		}
		saveDataMap[share.PartyID] = &shareData
	}

	// 3. --- Setup Signing Parties ---
	// Find the configuration for the current node
	var currentNode *config.Node
	for _, node := range cfg.Nodes {
		if node.Node == nodeName {
			currentNode = &node
			break
		}
	}
	if currentNode == nil {
		return nil, fmt.Errorf("configuration for node '%s' not found", nodeName)
	}

	allPartyConfs := append(currentNode.Parties, config.Party{
		Name:   currentNode.Node,
		Port:   fmt.Sprintf("%d", currentNode.Port),
		Server: "127.0.0.1", // Assuming self is always local
	})

	partyIDMap := make(map[string]string, len(allPartyConfs))
	for _, p := range allPartyConfs {
		partyIDMap[p.Name] = net.JoinHostPort(p.Server, p.Port)
	}

	// Reconstruct PartyIDs from the saved data map
	partyIDs := make(tss.UnSortedPartyIDs, 0, len(saveDataMap))
	for partyName, sd := range saveDataMap {
		partyIDs = append(partyIDs, tss.NewPartyID(partyName, partyName, sd.ShareID))
	}
	sortedSigningPartyIDs := tss.SortPartyIDs(partyIDs)

	errCh := make(chan *tss.Error, len(sortedSigningPartyIDs))
	endCh := make(chan *common.SignatureData, len(sortedSigningPartyIDs))
	transport := network.NewTCPTransport(partyIDMap)
	signingParties := make([]tss.Party, 0, len(sortedSigningPartyIDs))

	hash := sha256.Sum256([]byte(message))
	msgToSign := new(big.Int).SetBytes(hash[:])

	for _, pID := range sortedSigningPartyIDs {
		params := tss.NewParameters(tss.S256(), tss.NewPeerContext(sortedSigningPartyIDs), pID, len(sortedSigningPartyIDs), keyRecord.Threshold)

		localSaveData, ok := saveDataMap[pID.Id]
		if !ok || localSaveData.ShareID == nil {
			return nil, fmt.Errorf("could not find local save data for party %s", pID.Id)
		}

		outCh := make(chan tss.Message, len(sortedSigningPartyIDs))
		inCh := make(chan tss.ParsedMessage, len(sortedSigningPartyIDs))
		localParty := signing.NewLocalParty(msgToSign, params, *localSaveData, outCh, endCh)
		signingParties = append(signingParties, localParty)

		// Register party for message routing
		var partyPort string
		if pID.Id == currentNode.Node {
			partyPort = fmt.Sprintf(":%d", currentNode.Port)
		} else {
			for _, remoteParty := range currentNode.Parties {
				if remoteParty.Name == pID.Id {
					partyPort = ":" + remoteParty.Port
					break
				}
			}
		}
		if partyPort == "" {
			return nil, fmt.Errorf("could not find port for party %s", pID.Id)
		}
		party.DefaultRegistry.Register(partyPort, inCh)

		go func(p tss.Party, pTransport network.Transport, pOutCh <-chan tss.Message, pInCh <-chan tss.ParsedMessage, pCtx context.Context) {
			defer logger.Log.Infof("Party %s transport goroutine finished.", p.PartyID())
			for {
				select {
				case msg := <-pOutCh:
					if err := pTransport.Send(msg); err != nil {
						errCh <- p.WrapError(err)
						return
					}
				case msg := <-pInCh:
					go func(p tss.Party, msg tss.ParsedMessage) {
						if _, err := p.Update(msg); err != nil {
							errCh <- p.WrapError(err)
						}
					}(p, msg)
				case <-pCtx.Done():
					return
				}
			}
		}(localParty, transport, outCh, inCh, ctx)
	}

	var startWg sync.WaitGroup
	startWg.Add(len(signingParties))
	for _, p := range signingParties {
		go func(p tss.Party) {
			defer startWg.Done()
			if err := p.Start(); err != nil {
				errCh <- err
			}
		}(p)
	}

	var signature *common.SignatureData
	finishedSigners := 0
	for finishedSigners < len(sortedSigningPartyIDs) {
		select {
		case sig := <-endCh:
			signature = sig
			finishedSigners++
		case err := <-errCh:
			logger.Log.Errorf("Error during signing: %v", err)
			// Deregister all parties now that they are finished
			for _, pConf := range allPartyConfs {
				port := ":" + pConf.Port
				party.DefaultRegistry.Deregister(port)
			}
			return nil, err
		}
	}

	startWg.Wait()

	for _, pID := range sortedSigningPartyIDs {
		port, ok := partyIDMap[pID.Id]
		if ok {
			party.DefaultRegistry.Deregister(port)
		}
	}

	return signature, nil
}

// VerifySignature verifies a signature against a public key and message.
func VerifySignature(publicKeyHex string, message string, rBytes, sBytes []byte) (bool, error) {
	pubKeyBytes, err := hex.DecodeString(publicKeyHex)
	if err != nil {
		return false, fmt.Errorf("invalid public key hex: %v", err)
	}
	if len(pubKeyBytes) != 64 {
		return false, fmt.Errorf("invalid public key length")
	}

	pk := &ecdsa.PublicKey{
		Curve: tss.S256(),
		X:     new(big.Int).SetBytes(pubKeyBytes[:32]),
		Y:     new(big.Int).SetBytes(pubKeyBytes[32:]),
	}

	hash := sha256.Sum256([]byte(message))
	msgToVerify := new(big.Int).SetBytes(hash[:])

	ok := ecdsa.Verify(pk, msgToVerify.Bytes(), new(big.Int).SetBytes(rBytes), new(big.Int).SetBytes(sBytes))
	return ok, nil
}
