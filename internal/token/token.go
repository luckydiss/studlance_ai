// Package token generates opaque secrets (worker tokens, session ids).
package token

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
)

const secretLen = 32

// New returns a new random secret (base64url, no padding).
func New() string {
	b := make([]byte, secretLen)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		panic("token: rand failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Hash returns the hex sha256 of a secret, as stored in the DB.
func Hash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
