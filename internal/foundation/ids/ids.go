// Package ids is the only UUIDv7 generator in the application (SPEC-00 §2,
// SPEC-01 §5.2). IDs are lowercase 36-character strings; the first 48 bits are
// the Unix millisecond timestamp taken from an injectable clock.
package ids

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"strings"

	"researchos/internal/foundation/clock"
)

// New returns a UUIDv7 string. A nil clock means clock.System. The only failure
// mode is an unavailable random source.
func New(c clock.Clock) (string, error) {
	if c == nil {
		c = clock.System{}
	}
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], uint64(c.Now().UTC().UnixMilli())<<16)
	if _, err := rand.Read(b[6:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x70 // version 7
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant 10xx
	return format(b), nil
}

// MustNew is New with a panic on random-source failure, for tests and constants.
func MustNew(c clock.Clock) string {
	id, err := New(c)
	if err != nil {
		panic("ids: " + err.Error())
	}
	return id
}

// Valid reports whether s is a 36-character lowercase UUIDv7 (version nibble 7,
// variant bits 10xx).
func Valid(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := range len(s) {
		switch i {
		case 8, 13, 18, 23:
			if s[i] != '-' {
				return false
			}
		default:
			if !isLowerHex(s[i]) {
				return false
			}
		}
	}
	if s[14] != '7' {
		return false
	}
	switch s[19] {
	case '8', '9', 'a', 'b':
		return true
	}
	return false
}

func format(b [16]byte) string {
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

func isLowerHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
}

// TimestampPrefix returns the 13 leading characters, i.e. the 48-bit timestamp
// with its separator. Prefixes are comparable in the same order as the
// underlying timestamps.
func TimestampPrefix(s string) string {
	if len(s) < 13 {
		return s
	}
	return strings.ToLower(s[:13])
}
