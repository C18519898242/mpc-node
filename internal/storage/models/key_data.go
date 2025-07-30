package models

import (
	"github.com/google/uuid"
)

// KeyData corresponds to the `saveData` struct from tss-lib,
// but adapted for database storage.
type KeyData struct {
	KeyID        uuid.UUID  `gorm:"type:uuid;primary_key;" json:"keyId"`
	PublicKey    string     `gorm:"type:varchar(200);uniqueIndex" json:"publicKey"` // Store the public key hex
	Shares       []KeyShare `gorm:"foreignKey:KeyDataID;references:KeyID"`          // Has-many relationship using UUID
	PartyIDs     string     // Store a comma-separated list of party IDs involved
	Threshold    int        `json:"threshold"`
	FullSaveData []byte     `json:"-"` // The combined public save data, ignored by JSON responses
}
