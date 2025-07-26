package models

import (
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// KeyShare holds a single party's keygen data (a "share").
// It belongs to a KeyData record.
type KeyShare struct {
	gorm.Model
	KeyDataID uuid.UUID `gorm:"type:uuid;index" json:"-"` // Foreign key to the KeyData table's KeyID
	ShareData []byte    `json:"shareData"`                // The serialized keygen.LocalPartySaveData for one party
	PartyID   string    `json:"partyId"`                  // The party ID this share belongs to
}
