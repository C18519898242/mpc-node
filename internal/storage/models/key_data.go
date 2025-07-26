package models

import (
	"gorm.io/gorm"
)

// KeyData corresponds to the `saveData` struct from tss-lib,
// but adapted for database storage.
type KeyData struct {
	gorm.Model
	PublicKey string `gorm:"uniqueIndex"` // Store the public key hex
	KeyData   []byte // Store the serialized keygen.LocalPartySaveData
	PartyIDs  string // Store a comma-separated list of party IDs involved
	Threshold int
}
