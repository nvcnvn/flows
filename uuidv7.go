package flows

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// newUUIDv7 generates an RFC 9562 UUID version 7 string.
// It is time-ordered (milliseconds) with random bits.
func newUUIDv7(now time.Time) (string, error) {
	// UUIDv7 layout (16 bytes):
	// - 48 bits unix epoch milliseconds
	// - 4 bits version (0111)
	// - 12 bits random
	// - 2 bits variant (10)
	// - 62 bits random
	var b [16]byte

	ms := uint64(now.UnixNano() / int64(time.Millisecond))
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)

	// Fill remaining bytes with randomness first.
	if _, err := rand.Read(b[6:]); err != nil {
		return "", err
	}

	// Set version (7) in high nibble of byte 6.
	b[6] = (b[6] & 0x0F) | 0x70
	// Set variant (10) in byte 8.
	b[8] = (b[8] & 0x3F) | 0x80

	hexStr := hex.EncodeToString(b[:])
	// 8-4-4-4-12
	return fmt.Sprintf("%s-%s-%s-%s-%s", hexStr[0:8], hexStr[8:12], hexStr[12:16], hexStr[16:20], hexStr[20:32]), nil
}

func newCryptoUint64() (uint64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	return uint64(b[0])<<56 |
		uint64(b[1])<<48 |
		uint64(b[2])<<40 |
		uint64(b[3])<<32 |
		uint64(b[4])<<24 |
		uint64(b[5])<<16 |
		uint64(b[6])<<8 |
		uint64(b[7]), nil
}
