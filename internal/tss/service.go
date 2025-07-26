package tss

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mpc-node/internal/storage"
	"mpc-node/internal/storage/models"
	"strings"
	"sync"

	"github.com/bnb-chain/tss-lib/v2/ecdsa/keygen"
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

	fmt.Println("--- Phase 1: Key Generation ---")

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
		return nil, fmt.Errorf("error received after wait: %v", err)
	}

	fmt.Println("Key generation successful!")
	pubKey := saveData[0].ECDSAPub
	pubKeyHex := hex.EncodeToString(pubKey.X().Bytes()) + hex.EncodeToString(pubKey.Y().Bytes())
	fmt.Printf("Generated Public Key: %s\n", pubKeyHex)

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

	fmt.Println("Key and shares successfully saved to database.")

	// Preload the shares for the return value
	// We need to query by the UUID now to get the full record with shares
	var finalRecord models.KeyData
	err := storage.DB.Preload("Shares").First(&finalRecord, "key_id = ?", keyRecord.KeyID).Error
	if err != nil {
		return nil, fmt.Errorf("failed to preload shares for the new key: %v", err)
	}

	return &finalRecord, nil
}
