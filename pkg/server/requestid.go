package server

import (
	"crypto/rand"
	"encoding/binary"
	"time"
)

// Crockford's base32 alphabet, used by ULID (no I, L, O, U to avoid ambiguity).
const ulidAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// newRequestID returns a ULID-style identifier: a 48-bit big-endian millisecond
// timestamp followed by 80 bits of cryptographic randomness, encoded as 26
// Crockford base32 characters. This is lexicographically sortable by creation
// time and globally unique without any external dependency.
func newRequestID() string {
	var buf [16]byte

	ms := uint64(time.Now().UnixMilli())
	// 48-bit timestamp in the first 6 bytes (big-endian).
	binary.BigEndian.PutUint64(buf[:8], ms<<16)

	// 80 bits of randomness in the last 10 bytes.
	if _, err := rand.Read(buf[6:]); err != nil {
		// crypto/rand should never fail; fall back to the timestamp-only
		// encoding rather than panicking inside a request.
		return encodeULID(buf)
	}

	return encodeULID(buf)
}

// encodeULID renders the 128-bit value as 26 Crockford base32 characters.
func encodeULID(buf [16]byte) string {
	out := make([]byte, 26)

	// The first character only encodes the top 2 bits of the 128-bit value.
	out[0] = ulidAlphabet[(buf[0]&0xE0)>>5]
	out[1] = ulidAlphabet[buf[0]&0x1F]
	out[2] = ulidAlphabet[(buf[1]&0xF8)>>3]
	out[3] = ulidAlphabet[((buf[1]&0x07)<<2)|((buf[2]&0xC0)>>6)]
	out[4] = ulidAlphabet[(buf[2]&0x3E)>>1]
	out[5] = ulidAlphabet[((buf[2]&0x01)<<4)|((buf[3]&0xF0)>>4)]
	out[6] = ulidAlphabet[((buf[3]&0x0F)<<1)|((buf[4]&0x80)>>7)]
	out[7] = ulidAlphabet[(buf[4]&0x7C)>>2]
	out[8] = ulidAlphabet[((buf[4]&0x03)<<3)|((buf[5]&0xE0)>>5)]
	out[9] = ulidAlphabet[buf[5]&0x1F]
	out[10] = ulidAlphabet[(buf[6]&0xF8)>>3]
	out[11] = ulidAlphabet[((buf[6]&0x07)<<2)|((buf[7]&0xC0)>>6)]
	out[12] = ulidAlphabet[(buf[7]&0x3E)>>1]
	out[13] = ulidAlphabet[((buf[7]&0x01)<<4)|((buf[8]&0xF0)>>4)]
	out[14] = ulidAlphabet[((buf[8]&0x0F)<<1)|((buf[9]&0x80)>>7)]
	out[15] = ulidAlphabet[(buf[9]&0x7C)>>2]
	out[16] = ulidAlphabet[((buf[9]&0x03)<<3)|((buf[10]&0xE0)>>5)]
	out[17] = ulidAlphabet[buf[10]&0x1F]
	out[18] = ulidAlphabet[(buf[11]&0xF8)>>3]
	out[19] = ulidAlphabet[((buf[11]&0x07)<<2)|((buf[12]&0xC0)>>6)]
	out[20] = ulidAlphabet[(buf[12]&0x3E)>>1]
	out[21] = ulidAlphabet[((buf[12]&0x01)<<4)|((buf[13]&0xF0)>>4)]
	out[22] = ulidAlphabet[((buf[13]&0x0F)<<1)|((buf[14]&0x80)>>7)]
	out[23] = ulidAlphabet[(buf[14]&0x7C)>>2]
	out[24] = ulidAlphabet[((buf[14]&0x03)<<3)|((buf[15]&0xE0)>>5)]
	out[25] = ulidAlphabet[buf[15]&0x1F]

	return string(out)
}
