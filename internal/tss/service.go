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
func GenerateAndSaveKey(cfg *config.Config) (*models.KeyData, error) {
	logger.Log.Info("--- Phase 1: Setup for Real Network Key Generation ---")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // Ensure all goroutines are signalled to stop when we exit

	// 1. --- Party and Network Setup ---
	partyIDs := tss.GenerateTestPartyIDs(len(cfg.NodePorts))
	partyIDMap := make(map[string]string, len(cfg.NodePorts))
	nodePorts := cfg.NodePorts
	for i, pID := range partyIDs {
		partyIDMap[pID.Id] = nodePorts[i]
	}

	errCh := make(chan *tss.Error, len(cfg.NodePorts))
	endCh := make(chan *keygen.LocalPartySaveData, len(cfg.NodePorts))

	transport := network.NewTCPTransport(partyIDMap)
	parties := make([]tss.Party, 0, len(cfg.NodePorts))

	// 2. --- Create and Register Parties ---
	for i := 0; i < len(cfg.NodePorts); i++ {
		params := tss.NewParameters(tss.S256(), tss.NewPeerContext(partyIDs), partyIDs[i], len(cfg.NodePorts), threshold)
		outCh := make(chan tss.Message, len(cfg.NodePorts))
		inCh := make(chan tss.ParsedMessage, len(cfg.NodePorts))

		p := keygen.NewLocalParty(params, outCh, endCh)
		parties = append(parties, p)

		party.DefaultRegistry.Register(nodePorts[i], inCh)

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
	saveData := make([]*keygen.LocalPartySaveData, 0, len(cfg.NodePorts))
	finishedParties := 0
	for finishedParties < len(cfg.NodePorts) {
		select {
		case save := <-endCh:
			saveData = append(saveData, save)
			finishedParties++
		case err := <-errCh:
			logger.Log.Errorf("Error during keygen: %v", err)
			// Deregister all parties on error
			for i := 0; i < len(cfg.NodePorts); i++ {
				party.DefaultRegistry.Deregister(nodePorts[i])
			}
			return nil, err
		}
	}

	startWg.Wait()

	// Deregister all parties now that they are finished
	for i := 0; i < len(cfg.NodePorts); i++ {
		party.DefaultRegistry.Deregister(nodePorts[i])
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
		// Find the party ID string for the given share
		var pIDStr string
		for _, pID := range partyIDs {
			if pID.KeyInt().Cmp(sd.ShareID) == 0 {
				pIDStr = pID.Id
				break
			}
		}
		if pIDStr == "" {
			tx.Rollback()
			return nil, fmt.Errorf("could not find party ID for share")
		}

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

func SignMessage(keyID uuid.UUID, message string) (*common.SignatureData, error) {
	// 1. --- Load Key and Shares from DB ---
	var keyRecord models.KeyData
	err := storage.DB.Preload("Shares").First(&keyRecord, "key_id = ?", keyID).Error
	if err != nil {
		return nil, fmt.Errorf("failed to find key with ID %s: %v", keyID, err)
	}
	if len(keyRecord.Shares) == 0 {
		return nil, fmt.Errorf("no shares found for key with ID %s", keyID)
	}

	// 2. --- Deserialize Shares ---
	saveData := make([]*keygen.LocalPartySaveData, len(keyRecord.Shares))
	for i, share := range keyRecord.Shares {
		var shareData keygen.LocalPartySaveData
		if err := json.Unmarshal(share.ShareData, &shareData); err != nil {
			return nil, fmt.Errorf("failed to unmarshal share data for party %s: %v", share.PartyID, err)
		}
		saveData[i] = &shareData
	}

	// 3. --- Setup Signing Parties ---
	// Reconstruct PartyIDs from the saved data instead of generating new ones
	partyIDs := make(tss.UnSortedPartyIDs, len(saveData))
	saveDataMap := make(map[string]*keygen.LocalPartySaveData, len(saveData))
	for i, sd := range saveData {
		// The PartyID is not saved in keygen.LocalPartySaveData, but the ShareID (pID.Key) is.
		// We stored the party ID string in our KeyShare model.
		pID := tss.NewPartyID(keyRecord.Shares[i].PartyID, keyRecord.Shares[i].PartyID, sd.ShareID)
		partyIDs[i] = pID
		saveDataMap[pID.Id] = sd
	}

	signingPartyIDs := partyIDs[:keyRecord.Threshold+1]

	signOutCh := make(chan tss.Message, len(signingPartyIDs)*len(signingPartyIDs))
	signEndCh := make(chan *common.SignatureData, len(signingPartyIDs))
	signErrCh := make(chan *tss.Error, len(signingPartyIDs)*len(signingPartyIDs))

	var signWg sync.WaitGroup
	signWg.Add(len(signingPartyIDs))

	hash := sha256.Sum256([]byte(message))
	msgToSign := new(big.Int).SetBytes(hash[:])

	signingParties := make(map[*tss.PartyID]tss.Party, len(signingPartyIDs))
	sortedSigningPartyIDs := tss.SortPartyIDs(signingPartyIDs)
	for _, pID := range signingPartyIDs {
		params := tss.NewParameters(tss.S256(), tss.NewPeerContext(sortedSigningPartyIDs), pID, len(signingPartyIDs), keyRecord.Threshold)
		// Look up the correct save data from our map
		localSaveData, ok := saveDataMap[pID.Id]
		if !ok {
			return nil, fmt.Errorf("could not find save data for party %s", pID.Id)
		}
		party := signing.NewLocalParty(msgToSign, params, *localSaveData, signOutCh, signEndCh)
		signingParties[pID] = party
		go func(p tss.Party) {
			defer signWg.Done()
			if err := p.Start(); err != nil {
				signErrCh <- err
			}
		}(party)
	}

	// 4. --- Network Simulation (Signing) ---
	var signature *common.SignatureData
	finishedSigners := 0
	for finishedSigners < len(signingPartyIDs) {
		select {
		case msg := <-signOutCh:
			logger.Log.Infof("[TSS Signing] Received message: Type=%s, From=%s, To=%v, Broadcast=%v", msg.Type(), msg.GetFrom(), msg.GetTo(), msg.IsBroadcast())
			bz, _, err := msg.WireBytes()
			if err != nil {
				signErrCh <- tss.NewError(err, "SignAndVerify", 0, msg.GetFrom())
				continue
			}
			pMsg, err := tss.ParseWireMessage(bz, msg.GetFrom(), msg.IsBroadcast())
			if err != nil {
				signErrCh <- tss.NewError(err, "SignAndVerify", 0, msg.GetFrom())
				continue
			}
			dest := msg.GetTo()
			if dest == nil {
				// broadcast
				for _, pID := range signingPartyIDs {
					if pID == msg.GetFrom() {
						continue
					}
					logger.Log.Infof("[TSS Signing] Relaying broadcast message from %s to %s", msg.GetFrom(), pID)
					go func(p tss.Party) {
						if _, err := p.Update(pMsg); err != nil {
							signErrCh <- err
						}
					}(signingParties[pID])
				}
			} else {
				// point-to-point
				for _, pID := range dest {
					if pID == msg.GetFrom() {
						continue
					}
					logger.Log.Infof("[TSS Signing] Relaying p2p message from %s to %s", msg.GetFrom(), pID)
					go func(p tss.Party) {
						if _, err := p.Update(pMsg); err != nil {
							signErrCh <- err
						}
					}(signingParties[pID])
				}
			}
		case sig := <-signEndCh:
			signature = sig
			finishedSigners++
		case err := <-signErrCh:
			return nil, fmt.Errorf("error during signing: %v", err)
		}
	}
	signWg.Wait()

	// Close the error channel and check for any remaining errors
	close(signErrCh)
	for err := range signErrCh {
		return nil, fmt.Errorf("error received after wait: %v", err)
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
