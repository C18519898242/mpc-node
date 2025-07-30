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
	"mpc-node/internal/party"
	"mpc-node/internal/storage"
	"mpc-node/internal/storage/models"
	"sort"
	"strings"

	"github.com/bnb-chain/tss-lib/v2/common"
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

// CreatePartyIDs creates a sorted list of PartyIDs from a list of participant names.
// It ensures that PartyIDs are created consistently across the application.
func CreatePartyIDs(participants []string, currentNodeName string) (*tss.PartyID, tss.SortedPartyIDs, error) {
	// Sort party names alphabetically to ensure consistent PartyID creation
	sort.Strings(participants)

	partyIDs := make(tss.UnSortedPartyIDs, len(participants))
	var currentPartyID *tss.PartyID

	for i, name := range participants {
		// The key used in NewPartyID must be a unique, consistent identifier for each party.
		// Using the index from the sorted list ensures this.
		pID := tss.NewPartyID(name, name, new(big.Int).SetInt64(int64(i+1)))
		partyIDs[i] = pID
		if name == currentNodeName {
			currentPartyID = pID
		}
	}

	if currentPartyID == nil && currentNodeName != "" {
		return nil, nil, fmt.Errorf("could not find party ID for current node '%s' in participant list", currentNodeName)
	}

	sortedPartyIDs := tss.SortPartyIDs(partyIDs)
	return currentPartyID, sortedPartyIDs, nil
}

// GenerateAndSaveKey performs the TSS key generation using real network communication.
// It returns the full local save data (to be stored locally and sent to the coordinator).
func GenerateAndSaveKey(cfg *config.Config, nodeName string, participants []string, transport Transport) (*keygen.LocalPartySaveData, error) {
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
		return nil, fmt.Errorf("configuration for node '%s' not found", nodeName)
	}

	// 1. --- Party and Network Setup ---
	currentPartyID, sortedPartyIDs, err := CreatePartyIDs(participants, nodeName)
	if err != nil {
		return nil, fmt.Errorf("failed to create party IDs: %v", err)
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
		return nil, err
	}

	logger.Log.Info("Key generation successful for this party!")

	return saveData, nil
}

// CombinePublicData creates a new LocalPartySaveData object containing only the
// public key material from a single party's save data. It does this by creating a
// deep copy and then setting the private key fields to nil. This is the data
// that will be stored in the `full_save_data` column for the key.
func CombinePublicData(shares map[string]*keygen.LocalPartySaveData) *keygen.LocalPartySaveData {
	if len(shares) == 0 {
		return nil
	}

	// Get the first share from the map. It doesn't matter which one, as the public
	// data required for signing is the same across all of them.
	var firstShare *keygen.LocalPartySaveData
	for _, s := range shares {
		firstShare = s
		break
	}

	// Create a deep copy to avoid modifying the original object in the session.
	// A simple struct copy `*firstShare` is not enough due to nested pointers.
	// Marshalling and unmarshalling is a reliable way to deep copy.
	tempBytes, err := json.Marshal(firstShare)
	if err != nil {
		logger.Log.Errorf("Failed to marshal for deep copy: %v", err)
		return nil
	}
	var publicData keygen.LocalPartySaveData
	if err := json.Unmarshal(tempBytes, &publicData); err != nil {
		logger.Log.Errorf("Failed to unmarshal for deep copy: %v", err)
		return nil
	}

	// Nil out all private key fields. These are not needed for the combined
	// public key data that is stored in the database. The local party's private
	// share (`Xi`) will be injected back in at signing time.
	publicData.Xi = nil
	publicData.ShareID = nil
	// These are also private and should not be in the combined data
	publicData.PaillierSK = nil
	publicData.NTildei = nil
	publicData.H1i = nil
	publicData.H2i = nil
	publicData.Alpha = nil
	publicData.Beta = nil
	publicData.P = nil
	publicData.Q = nil

	return &publicData
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

	// Inject the local private key and other private data into the combined public data object.
	// This creates the complete SaveData object needed for this party to sign.
	localSaveData := combinedSaveData
	localSaveData.Xi = localPrivateShare.Xi
	localSaveData.ShareID = localPrivateShare.ShareID
	localSaveData.PaillierSK = localPrivateShare.PaillierSK
	// Also inject the party-specific public values, in case the one we used for the
	// combined data was from a different party.
	localSaveData.NTildei = localPrivateShare.NTildei
	localSaveData.H1i = localPrivateShare.H1i
	localSaveData.H2i = localPrivateShare.H2i

	// 2. --- Reconstruct Party IDs ---
	partyNames := strings.Split(keyRecord.PartyIDs, ",")
	currentPartyID, sortedSigningPartyIDs, err := CreatePartyIDs(partyNames, nodeName)
	if err != nil {
		return nil, fmt.Errorf("failed to create party IDs for signing: %v", err)
	}

	// The combinedSaveData loaded from FullSaveData now contains the correct, full set of Ks.
	// The old workaround is no longer needed.
	// IMPORTANT: We must reconstruct the Ks slice to match the sorted order of the parties
	// that will be used for this signing ceremony.
	kIDs := make([]*big.Int, len(sortedSigningPartyIDs))
	for i, p := range sortedSigningPartyIDs {
		kIDs[i] = new(big.Int).SetBytes(p.Key)
	}
	localSaveData.Ks = kIDs

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
