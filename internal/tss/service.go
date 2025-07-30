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
	"mpc-node/internal/dto"
	"mpc-node/internal/logger"
	"mpc-node/internal/party"
	"mpc-node/internal/storage"
	"mpc-node/internal/storage/models"
	"net"
	"sort"
	"strings"

	"github.com/bnb-chain/tss-lib/v2/common"
	"github.com/bnb-chain/tss-lib/v2/crypto/paillier"
	"github.com/bnb-chain/tss-lib/v2/ecdsa/keygen"
	"github.com/bnb-chain/tss-lib/v2/ecdsa/signing"
	"github.com/bnb-chain/tss-lib/v2/tss"
	tsslib "github.com/bnb-chain/tss-lib/v2/tss"
	"github.com/google/uuid"
)

const (
	partiesCount = 3
	threshold    = 1
)

// Transport defines the interface for sending TSS messages.
type Transport interface {
	Send(msg tsslib.Message) error
}

// GenerateAndSaveKey performs the TSS key generation using real network communication.
// It returns the public part of the save data (to be sent to the coordinator) and
// the full local save data (to be stored locally).
func GenerateAndSaveKey(cfg *config.Config, nodeName string, transport Transport) (*dto.PublicPartySaveData, *keygen.LocalPartySaveData, error) {
	logger.Log.Infof("--- Phase 1: Setup for Real Network Key Generation ---")
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
		return nil, nil, fmt.Errorf("configuration for node '%s' not found", nodeName)
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
		return nil, nil, fmt.Errorf("could not find party ID for current node '%s'", nodeName)
	}

	// Use a higher buffer size for channels to prevent blocking
	bufSize := len(sortedPartyIDs) * 10
	errCh := make(chan *tss.Error, bufSize)
	endCh := make(chan *keygen.LocalPartySaveData, bufSize)

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
	go func(p tsslib.Party, pTransport Transport, pOutCh <-chan tsslib.Message, pInCh <-chan tss.ParsedMessage, pCtx context.Context) {
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
		return nil, nil, err
	}

	logger.Log.Info("Key generation successful for this party!")

	publicData := &dto.PublicPartySaveData{
		Ks:          saveData.Ks,
		NTildej:     saveData.NTildej,
		H1j:         saveData.H1j,
		H2j:         saveData.H2j,
		ECDSAPub:    saveData.ECDSAPub,
		PaillierPKs: saveData.PaillierPKs,
		ShareID:     saveData.ShareID,
	}

	return publicData, saveData, nil
}

// CombinePublicData takes the public shares from all parties and combines them into a single
// LocalPartySaveData object that contains all public information for the key.
func CombinePublicData(shares map[string]*dto.PublicPartySaveData) *keygen.LocalPartySaveData {
	combined := &keygen.LocalPartySaveData{
		Ks:          make([]*big.Int, 0),
		NTildej:     make([]*big.Int, 0),
		H1j:         make([]*big.Int, 0),
		H2j:         make([]*big.Int, 0),
		PaillierPKs: make([]*paillier.PublicKey, 0),
	}

	// We need a consistent order to combine the shares
	partyIDs := make([]string, 0, len(shares))
	for id := range shares {
		partyIDs = append(partyIDs, id)
	}
	sort.Strings(partyIDs)

	isFirst := true
	for _, partyID := range partyIDs {
		data := shares[partyID]
		if isFirst {
			combined.ECDSAPub = data.ECDSAPub
			isFirst = false
		}
		combined.Ks = append(combined.Ks, data.Ks...)
		combined.NTildej = append(combined.NTildej, data.NTildej...)
		combined.H1j = append(combined.H1j, data.H1j...)
		combined.H2j = append(combined.H2j, data.H2j...)
		combined.PaillierPKs = append(combined.PaillierPKs, data.PaillierPKs...)
	}

	return combined
}

func SignMessage(cfg *config.Config, nodeName string, keyID uuid.UUID, message string, transport Transport) (*common.SignatureData, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. --- Load Combined Public Data and Local Private Share from DB ---
	var keyRecord models.KeyData
	if err := storage.DB.First(&keyRecord, "key_id = ?", keyID).Error; err != nil {
		return nil, fmt.Errorf("failed to find key with ID %s: %v", keyID, err)
	}

	// Unmarshal the combined public data
	var combinedSaveData keygen.LocalPartySaveData
	if err := json.Unmarshal(keyRecord.FullSaveData, &combinedSaveData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal combined save data for key %s: %v", keyID, err)
	}

	// Load the local party's private share
	var localShareRecord models.KeyShare
	if err := storage.DB.Where("key_data_id = ? AND party_id = ?", keyID, nodeName).First(&localShareRecord).Error; err != nil {
		return nil, fmt.Errorf("failed to find share for party %s and key %s: %v", nodeName, keyID, err)
	}

	// Unmarshal the local party's share to get the private key (Xi)
	var localPrivateShare keygen.LocalPartySaveData
	if err := json.Unmarshal(localShareRecord.ShareData, &localPrivateShare); err != nil {
		return nil, fmt.Errorf("failed to unmarshal local private share for party %s: %v", nodeName, err)
	}

	// Inject the local private key into the combined public data object
	// This creates the complete SaveData object needed for signing
	combinedSaveData.Xi = localPrivateShare.Xi
	combinedSaveData.ShareID = localPrivateShare.ShareID

	localSaveData := combinedSaveData

	// 2. --- Reconstruct Party IDs ---
	partyNames := strings.Split(keyRecord.PartyIDs, ",")
	if len(partyNames) == 0 {
		return nil, fmt.Errorf("no party IDs found in key record for key %s", keyID)
	}
	// Sort party names alphabetically to ensure consistent PartyID creation, same as in keygen
	sort.Strings(partyNames)

	partyIDs := make(tss.UnSortedPartyIDs, len(partyNames))
	for i, name := range partyNames {
		// The key used in NewPartyID during keygen was the index + 1
		partyIDs[i] = tss.NewPartyID(name, name, new(big.Int).SetInt64(int64(i+1)))
	}
	sortedSigningPartyIDs := tss.SortPartyIDs(partyIDs)

	// The combinedSaveData loaded from FullSaveData now contains the correct, full set of Ks.
	// The old workaround is no longer needed.

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

	// Use a higher buffer size for channels to prevent blocking
	bufSize := len(sortedSigningPartyIDs) * 10
	errCh := make(chan *tss.Error, bufSize)
	endCh := make(chan *common.SignatureData, bufSize)

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
	if localSaveData.ShareID == nil {
		return nil, fmt.Errorf("invalid local save data for party %s: ShareID is nil", currentPartyID.Id)
	}

	outCh := make(chan tss.Message, bufSize)
	inCh := make(chan tss.ParsedMessage, bufSize)

	// Register the channel for the local party
	localPort := fmt.Sprintf(":%d", currentNode.Port)
	party.DefaultRegistry.Register(localPort, inCh)
	defer party.DefaultRegistry.Deregister(localPort)

	localParty := signing.NewLocalParty(msgToSign, params, localSaveData, outCh, endCh)

	go func(p tsslib.Party, pTransport Transport, pOutCh <-chan tsslib.Message, pInCh <-chan tss.ParsedMessage, pCtx context.Context) {
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
