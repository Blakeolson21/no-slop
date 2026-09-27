package lanehealth

import (
	"crypto/sha256"
	"encoding/hex"
)

// ScopeKey returns the stable storage key for one provider account and model.
// AccountID is already an opaque identifier derived from the resolved account
// home; the returned key does not expose either value.
func ScopeKey(accountID, model string) string {
	if accountID == "" || model == "" {
		return ""
	}
	hash := sha256.Sum256([]byte(accountID + "\x00" + model))
	return hex.EncodeToString(hash[:])
}
