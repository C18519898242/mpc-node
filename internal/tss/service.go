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
	"sort"
	"strings"

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

	// Sort the party configurations by name to ensure a consistent order for PartyID creation
	sort.Slice(allPartyConfs, func(i, j int) bool {
		return allPartyConfs[i].Name < allPartyConfs[j].Name
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

	// Use a higher buffer size for channels to prevent blocking
	bufSize := len(sortedPartyIDs) * 10
	errCh := make(chan *tss.Error, bufSize)
	endCh := make(chan *keygen.LocalPartySaveData, bufSize)

	transport := network.NewTCPTransport(partyIDMap)

	// 2. --- Create and Register the LOCAL Party ---
	params := tss.NewParameters(tss.S256(), tss.NewPeerContext(sortedPartyIDs), currentPartyID, len(sortedPartyIDs), threshold)
	outCh := make(chan tss.Message, bufSize)
	inCh := make(chan tss.ParsedMessage, bufSize)

	// Register the channel for the local party
	localPort := fmt.Sprintf(":%d", currentNode.Port)
	party.DefaultRegistry.Register(localPort, inCh)
	// Ensure the party is deregistered when the function returns
	defer party.DefaultRegistry.Deregister(localPort)

	p := keygen.NewLocalParty(params, outCh, endCh)

	// Start a goroutine to handle message transport for the local party
	go func(p tss.Party, pTransport network.Transport, pOutCh <-chan tss.Message, pInCh <-chan tss.ParsedMessage, pCtx context.Context) {
		defer logger.Log.Infof("Party %s transport goroutine finished.", p.PartyID())
		for {
			select {
			case msg := <-pOutCh:
				logger.Log.Debugf("Party %s: sending message %s to %s", p.PartyID(), msg.Type(), msg.GetTo())
				if err := pTransport.Send(msg); err != nil {
					errCh <- p.WrapError(err)
					return
				}
			case msg := <-pInCh:
				logger.Log.Debugf("Party %s: received message %s from %s", p.PartyID(), msg.Type(), msg.GetFrom())
				// The `Update` method on the party is not thread-safe, so we process messages serially.
				if _, err := p.Update(msg); err != nil {
					errCh <- p.WrapError(err)
				}
				logger.Log.Debugf("Party %s: finished updating with message from %s", p.PartyID(), msg.GetFrom())
			case <-pCtx.Done():
				return
			}
		}
	}(p, transport, outCh, inCh, ctx)

	// 3. --- Start the local party in a new goroutine ---
	logger.Log.Infof("Party %s: starting...", p.PartyID())
	go func() {
		if err := p.Start(); err != nil {
			logger.Log.Errorf("Party %s: failed to start: %v", p.PartyID(), err)
			errCh <- err
		}
	}()
	logger.Log.Infof("Party %s: start called.", p.PartyID())

	// 4. --- Wait for the local party to finish ---
	logger.Log.Infof("Party %s: waiting for result...", p.PartyID())
	var saveData *keygen.LocalPartySaveData
	select {
	case save := <-endCh:
		logger.Log.Infof("Party %s: received save data from endCh.", p.PartyID())
		saveData = save
	case err := <-errCh:
		logger.Log.Errorf("Error during keygen for party %s: %v", p.PartyID(), err)
		return nil, err
	}

	logger.Log.Info("Key generation successful for this party!")
	pubKey := saveData.ECDSAPub
	pubKeyHex := hex.EncodeToString(pubKey.X().Bytes()) + hex.EncodeToString(pubKey.Y().Bytes())
	logger.Log.Infof("Generated Public Key: %s", pubKeyHex)

	var pIDs []string
	for _, pID := range partyIDs {
		pIDs = append(pIDs, pID.Id)
	}

	// In a real application, a shared transaction ID would be used to unify the key record.
	// Here, each node will create a separate key record, which is a known limitation of this simplified design.
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

	shareBytes, err := json.Marshal(saveData)
	if err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("failed to marshal share data for party %s: %v", currentPartyID.Id, err)
	}
	keyShare := models.KeyShare{
		KeyDataID: keyRecord.KeyID,
		ShareData: shareBytes,
		PartyID:   currentPartyID.Id,
	}
	if err := tx.Create(&keyShare).Error; err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("failed to create key share for party %s: %v", currentPartyID.Id, err)
	}

	if err := tx.Commit().Error; err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %v", err)
	}

	logger.Log.Info("Key and shares successfully saved to database.")

	var finalRecord models.KeyData
	err = storage.DB.Preload("Shares").First(&finalRecord, "key_id = ?", keyRecord.KeyID).Error
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

	// Sort the party configurations by name to ensure a consistent order
	sort.Slice(allPartyConfs, func(i, j int) bool {
		return allPartyConfs[i].Name < allPartyConfs[j].Name
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

	// Use a higher buffer size for channels to prevent blocking
	bufSize := len(sortedSigningPartyIDs) * 10
	errCh := make(chan *tss.Error, bufSize)
	endCh := make(chan *common.SignatureData, bufSize)
	transport := network.NewTCPTransport(partyIDMap)

	hash := sha256.Sum256([]byte(message))
	msgToSign := new(big.Int).SetBytes(hash[:])

	// Find the PartyID for the current node
	var currentPartyID *tss.PartyID
	for _, pID := range sortedSigningPartyIDs {
		if pID.Id == nodeName {
			currentPartyID = pID
			break
		}
	}
	if currentPartyID == nil {
		return nil, fmt.Errorf("could not find party ID for current node '%s'", nodeName)
	}

	params := tss.NewParameters(tss.S256(), tss.NewPeerContext(sortedSigningPartyIDs), currentPartyID, len(sortedSigningPartyIDs), keyRecord.Threshold)
	localSaveData, ok := saveDataMap[currentPartyID.Id]
	if !ok || localSaveData.ShareID == nil {
		return nil, fmt.Errorf("could not find local save data for party %s", currentPartyID.Id)
	}

	outCh := make(chan tss.Message, bufSize)
	inCh := make(chan tss.ParsedMessage, bufSize)

	// Register the channel for the local party
	localPort := fmt.Sprintf(":%d", currentNode.Port)
	party.DefaultRegistry.Register(localPort, inCh)
	defer party.DefaultRegistry.Deregister(localPort)

	localParty := signing.NewLocalParty(msgToSign, params, *localSaveData, outCh, endCh)

	go func(p tss.Party, pTransport network.Transport, pOutCh <-chan tss.Message, pInCh <-chan tss.ParsedMessage, pCtx context.Context) {
		defer logger.Log.Infof("Party %s transport goroutine finished.", p.PartyID())
		for {
			select {
			case msg := <-pOutCh:
				logger.Log.Debugf("Party %s: sending signing message %s to %s", p.PartyID(), msg.Type(), msg.GetTo())
				if err := pTransport.Send(msg); err != nil {
					errCh <- p.WrapError(err)
					return
				}
			case msg := <-pInCh:
				logger.Log.Debugf("Party %s: received signing message %s from %s", p.PartyID(), msg.Type(), msg.GetFrom())
				// The `Update` method on the party is not thread-safe, so we process messages serially.
				if _, err := p.Update(msg); err != nil {
					errCh <- p.WrapError(err)
				}
				logger.Log.Debugf("Party %s: finished updating with signing message from %s", p.PartyID(), msg.GetFrom())
			case <-pCtx.Done():
				return
			}
		}
	}(localParty, transport, outCh, inCh, ctx)

	// Start the local party in a new goroutine
	logger.Log.Infof("Party %s: starting signing...", localParty.PartyID())
	go func() {
		if err := localParty.Start(); err != nil {
			logger.Log.Errorf("Party %s: failed to start signing: %v", localParty.PartyID(), err)
			errCh <- err
		}
	}()
	logger.Log.Infof("Party %s: start signing called.", localParty.PartyID())

	logger.Log.Infof("Party %s: waiting for signature...", localParty.PartyID())
	var signature *common.SignatureData
	select {
	case sig := <-endCh:
		logger.Log.Infof("Party %s: received signature from endCh.", localParty.PartyID())
		signature = sig
	case err := <-errCh:
		logger.Log.Errorf("Error during signing: %v", err)
		return nil, err
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
