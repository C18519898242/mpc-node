package party

import (
	"crypto/sha256"
	"math/big"

	"github.com/bnb-chain/tss-lib/v2/tss"
)

// ElectCoordinator deterministically selects a coordinator from a list of participants
// based on the hash of a session ID.
func ElectCoordinator(sessionID string, participants []*tss.PartyID) *tss.PartyID {
	if len(participants) == 0 {
		return nil
	}

	// 1. Calculate SHA256 hash of the session ID.
	hash := sha256.Sum256([]byte(sessionID))

	// 2. Convert the hash to a big.Int.
	hashInt := new(big.Int).SetBytes(hash[:])

	// 3. Modulo the hash by the number of participants to get an index.
	mod := new(big.Int).Mod(hashInt, big.NewInt(int64(len(participants))))
	index := mod.Int64()

	// 4. Return the participant at the determined index.
	return participants[index]
}
