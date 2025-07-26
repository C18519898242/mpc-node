package tss

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"mpc-node/internal/logger"
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

// GenerateAndSaveKey performs the TSS key generation and saves the result to the database.
// It returns the newly created key record.
func GenerateAndSaveKey() (*models.KeyData, error) {
	// 1. --- Setup ---
	partyIDs := tss.GenerateTestPartyIDs(partiesCount)
	outCh := make(chan tss.Message, partiesCount*partiesCount)
	endCh := make(chan *keygen.LocalPartySaveData, partiesCount)
	errCh := make(chan *tss.Error, partiesCount*partiesCount)

	var wg sync.WaitGroup
	wg.Add(partiesCount)

	parties := make(map[*tss.PartyID]tss.Party, partiesCount)
	saveData := make([]*keygen.LocalPartySaveData, partiesCount)

	logger.Log.Info("--- Phase 1: Key Generation ---")

	// 2. --- Key Generation ---
	for i := 0; i < partiesCount; i++ {
		ctx := tss.NewPeerContext(partyIDs)
		params := tss.NewParameters(tss.S256(), ctx, partyIDs[i], partiesCount, threshold)
		party := keygen.NewLocalParty(params, outCh, endCh)
		parties[partyIDs[i]] = party
		go func(p tss.Party) {
			defer wg.Done()
			if err := p.Start(); err != nil {
				errCh <- err
			}
		}(party)
	}

	// 3. --- Network Simulation (Key Generation) ---
	finishedParties := 0
	for finishedParties < partiesCount {
		select {
		case msg := <-outCh:
			bz, _, err := msg.WireBytes()
			if err != nil {
				return nil, fmt.Errorf("error getting wire bytes: %v", err)
			}
			parsedMsg, err := tss.ParseWireMessage(bz, msg.GetFrom(), msg.IsBroadcast())
			if err != nil {
				return nil, fmt.Errorf("error parsing message: %v", err)
			}
			dest := msg.GetTo()
			if dest == nil { // broadcast
				for _, pID := range partyIDs {
					if pID == msg.GetFrom() {
						continue
					}
					go func(p tss.Party) {
						if _, err := p.Update(parsedMsg); err != nil {
							errCh <- err
						}
					}(parties[pID])
				}
			} else { // point-to-point
				for _, pID := range dest {
					if pID == msg.GetFrom() {
						continue
					}
					go func(p tss.Party) {
						if _, err := p.Update(parsedMsg); err != nil {
							errCh <- err
						}
					}(parties[pID])
				}
			}
		case save := <-endCh:
			var found bool
			for i, pID := range partyIDs {
				if pID.KeyInt().Cmp(save.ShareID) == 0 {
					saveData[i] = save
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("could not find party for save data")
			}
			finishedParties++
		case err := <-errCh:
			return nil, fmt.Errorf("error in keygen: %v", err)
		}
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		logger.Log.Errorf("Error received after wait: %v", err)
		return nil, fmt.Errorf("error received after wait: %v", err)
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

	// Use a transaction to ensure atomicity
	tx := storage.DB.Begin()
	if tx.Error != nil {
		return nil, fmt.Errorf("failed to begin transaction: %v", tx.Error)
	}

	// Create the main key record
	if err := tx.Create(&keyRecord).Error; err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("failed to create key record: %v", err)
	}

	// Create a share record for each party
	for i, pID := range partyIDs {
		shareBytes, err := json.Marshal(saveData[i])
		if err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("failed to marshal share data for party %s: %v", pID.Id, err)
		}
		keyShare := models.KeyShare{
			KeyDataID: keyRecord.KeyID, // Link to the parent key using UUID
			ShareData: shareBytes,
			PartyID:   pID.Id,
		}
		if err := tx.Create(&keyShare).Error; err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("failed to create key share for party %s: %v", pID.Id, err)
		}
	}

	if err := tx.Commit().Error; err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %v", err)
	}

	logger.Log.Info("Key and shares successfully saved to database.")

	// Preload the shares for the return value
	// We need to query by the UUID now to get the full record with shares
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
				for _, pID := range signingPartyIDs {
					if pID == msg.GetFrom() {
						continue
					}
					go func(p tss.Party) {
						if _, err := p.Update(pMsg); err != nil {
							signErrCh <- err
						}
					}(signingParties[pID])
				}
			} else {
				for _, pID := range dest {
					if pID == msg.GetFrom() {
						continue
					}
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
