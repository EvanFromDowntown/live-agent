package domain

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// NewID returns a short random, prefixed identifier (e.g. "exp_1a2b3c...").
// We generate our own IDs to avoid pulling in a UUID dependency.
func NewID(prefix string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is catastrophic; panic is acceptable here.
		panic(fmt.Sprintf("domain: cannot read random bytes: %v", err))
	}
	return prefix + "_" + hex.EncodeToString(b)
}
