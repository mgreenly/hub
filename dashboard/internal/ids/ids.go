// Package ids generates opaque, unguessable identifiers. Every id the suite
// hands out — session ids, state ids, OAuth chain/token/client/code ids, and
// the random half of token plaintexts — is a 128-bit cryptographically-random
// value, base32-encoded. Unlike a ULID it carries no embedded timestamp, so it
// leaks nothing about when it was minted on user-facing surfaces.
package ids

import (
	"crypto/rand"
	"encoding/base32"
)

var enc = base32.StdEncoding.WithPadding(base32.NoPadding)

// New returns a fresh 128-bit cryptographically-random opaque identifier. It
// panics if the system CSPRNG fails — an unusable entropy source is not a
// condition the caller can sensibly recover from.
func New() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("ids: crypto/rand failed: " + err.Error())
	}
	return enc.EncodeToString(b[:])
}
