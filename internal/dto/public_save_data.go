package dto

import (
	"math/big"

	"github.com/bnb-chain/tss-lib/v2/crypto"
	"github.com/bnb-chain/tss-lib/v2/crypto/paillier"
)

// PublicPartySaveData contains the public part of keygen.LocalPartySaveData
// that can be safely shared between parties.
type PublicPartySaveData struct {
	Ks          []*big.Int
	NTildej     []*big.Int
	H1j, H2j    []*big.Int
	ECDSAPub    *crypto.ECPoint
	PaillierPKs []*paillier.PublicKey
	ShareID     *big.Int
}
