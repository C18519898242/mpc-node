package tss

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"fmt"
	"math/big"
	"sync"

	"github.com/bnb-chain/tss-lib/v2/common"
	"github.com/bnb-chain/tss-lib/v2/ecdsa/keygen"
	"github.com/bnb-chain/tss-lib/v2/ecdsa/signing"
	"github.com/bnb-chain/tss-lib/v2/tss"
	"github.com/ipfs/go-log"
)

const (
	partiesCount = 3
	threshold    = 1
)

func RunTssSimulation() {
	// 1. --- Setup ---
	if err := log.SetLogLevel("tss-lib", "info"); err != nil {
		panic(err)
	}

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
				fmt.Printf("Error getting wire bytes: %v\n", err)
				continue
			}
			parsedMsg, err := tss.ParseWireMessage(bz, msg.GetFrom(), msg.IsBroadcast())
			if err != nil {
				fmt.Printf("Error parsing message: %v\n", err)
				continue
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
			// find party index by ShareID
			var found bool
			for i, pID := range partyIDs {
				if pID.KeyInt().Cmp(save.ShareID) == 0 {
					saveData[i] = save
					found = true
					break
				}
			}
			if !found {
				fmt.Println("Error: could not find party for save data")
				return
			}
			finishedParties++
		case err := <-errCh:
			fmt.Printf("Error in keygen: %v\n", err)
			return
		}
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		fmt.Printf("Error received after wait: %v\n", err)
		return
	}

	fmt.Println("Key generation successful!")
	pubKey := saveData[0].ECDSAPub
	fmt.Printf("Generated Public Key: X: %s, Y: %s\n", pubKey.X(), pubKey.Y())

	// 4. --- Phase 2: Signing ---
	fmt.Println("\n--- Phase 2: Signing ---")

	signingPartyIDs := partyIDs[:threshold+1]
	signingParties := make(map[*tss.PartyID]tss.Party, len(signingPartyIDs))

	signOutCh := make(chan tss.Message, len(signingPartyIDs)*len(signingPartyIDs))
	signEndCh := make(chan *common.SignatureData, len(signingPartyIDs))
	signErrCh := make(chan *tss.Error, len(signingPartyIDs)*len(signingPartyIDs))

	var signWg sync.WaitGroup
	signWg.Add(len(signingPartyIDs))

	msg := "Hello, TSS!"
	hash := sha256.Sum256([]byte(msg))
	msgToSign := new(big.Int).SetBytes(hash[:])

	fmt.Printf("Message to sign: \"%s\"\n", msg)
	fmt.Printf("Message hash: %x\n", msgToSign)

	// 5. --- Create and start signing parties ---
	for _, pID := range signingPartyIDs {
		ctx := tss.NewPeerContext(signingPartyIDs)
		params := tss.NewParameters(tss.S256(), ctx, pID, len(signingPartyIDs), threshold)
		party := signing.NewLocalParty(msgToSign, params, *saveData[pID.Index], signOutCh, signEndCh)
		signingParties[pID] = party
		go func(p tss.Party) {
			defer signWg.Done()
			if err := p.Start(); err != nil {
				signErrCh <- err
			}
		}(party)
	}

	// 6. --- Network Simulation (Signing) ---
	var signature common.SignatureData
	signFinishedParties := 0
	for signFinishedParties < len(signingPartyIDs) {
		select {
		case msg := <-signOutCh:
			bz, _, err := msg.WireBytes()
			if err != nil {
				fmt.Printf("Error getting wire bytes: %v\n", err)
				continue
			}
			parsedMsg, err := tss.ParseWireMessage(bz, msg.GetFrom(), msg.IsBroadcast())
			if err != nil {
				fmt.Printf("Error parsing message: %v\n", err)
				continue
			}
			dest := msg.GetTo()
			if dest == nil { // broadcast
				for _, pID := range signingPartyIDs {
					if pID == msg.GetFrom() {
						continue
					}
					go func(p tss.Party) {
						if _, err := p.Update(parsedMsg); err != nil {
							signErrCh <- err
						}
					}(signingParties[pID])
				}
			} else { // point-to-point
				for _, pID := range dest {
					if pID == msg.GetFrom() {
						continue
					}
					go func(p tss.Party) {
						if _, err := p.Update(parsedMsg); err != nil {
							signErrCh <- err
						}
					}(signingParties[pID])
				}
			}
		case sig := <-signEndCh:
			signature = *sig
			signFinishedParties++
		case err := <-errCh:
			fmt.Printf("Error in signing: %v\n", err)
			return
		}
	}

	signWg.Wait()
	close(signErrCh)
	for err := range signErrCh {
		fmt.Printf("Error received after sign wait: %v\n", err)
		return
	}

	fmt.Println("Signing successful!")
	fmt.Printf("Signature R: %s\n", new(big.Int).SetBytes(signature.R))
	fmt.Printf("Signature S: %s\n", new(big.Int).SetBytes(signature.S))

	// 7. --- Phase 3: Verification ---
	fmt.Println("\n--- Phase 3: Verification ---")
	pk := &ecdsa.PublicKey{
		Curve: tss.S256(),
		X:     pubKey.X(),
		Y:     pubKey.Y(),
	}
	ok := ecdsa.Verify(pk, msgToSign.Bytes(), new(big.Int).SetBytes(signature.R), new(big.Int).SetBytes(signature.S))

	if ok {
		fmt.Println("✅ Signature verified successfully!")
	} else {
		fmt.Println("❌ Signature verification failed!")
	}
}
