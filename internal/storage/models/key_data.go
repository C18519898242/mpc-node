package models

import (
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// KeyData corresponds to the `saveData` struct from tss-lib,
// but adapted for database storage.
type KeyData struct {
	gorm.Model
	KeyID     uuid.UUID  `gorm:"type:uuid;uniqueIndex;not null"`
	PublicKey string     `gorm:"type:varchar(200);uniqueIndex"`         // Store the public key hex
	Shares    []KeyShare `gorm:"foreignKey:KeyDataID;references:KeyID"` // Has-many relationship using UUID
	PartyIDs  string     // Store a comma-separated list of party IDs involved
	Threshold int
}
