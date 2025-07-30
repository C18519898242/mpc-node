package dto

import "github.com/bnb-chain/tss-lib/v2/common"

// SignatureResponsePayload is the payload for a SignatureResponse message.
// It is also used to pass the final result back to the API handler.
type SignatureResponsePayload struct {
	Signature *common.SignatureData `json:"signature"`
	Error     string                `json:"error,omitempty"`
}
