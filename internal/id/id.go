// Package id generates UUIDv7 identifiers as strings.
package id

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// New returns a new UUIDv7 string (time-ordered).
func New() string {
	var b [16]byte
	// 48-bit big-endian Unix milliseconds.
	ms := uint64(time.Now().UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	// Fill the rest with randomness.
	if _, err := rand.Read(b[6:]); err != nil {
		panic("id: rand.Read failed: " + err.Error())
	}
	// Version 7 and RFC 4122 variant.
	b[6] = (b[6] & 0x0f) | 0x70
	b[8] = (b[8] & 0x3f) | 0x80

	var dst [36]byte
	hex.Encode(dst[0:8], b[0:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], b[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], b[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], b[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:36], b[10:16])
	return string(dst[:])
}
