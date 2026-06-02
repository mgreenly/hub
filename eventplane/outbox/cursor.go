package outbox

import (
	"crypto/rand"
	"encoding/base32"
	"strconv"
	"strings"
	"time"
)

var ulidEnc = base32.StdEncoding.WithPadding(base32.NoPadding)

// newULID returns a 26-char Crockford-style ULID: 48 bits of millisecond time
// plus 80 bits of cryptographic randomness, base32-encoded. Used for event ids
// and for the generation/epoch token. Single-line and free of '.' so it is safe
// both on the SSE wire and as the generation half of a cursor.
func newULID() string {
	var b [16]byte
	now := uint64(time.Now().UnixMilli())
	b[0] = byte(now >> 40)
	b[1] = byte(now >> 32)
	b[2] = byte(now >> 24)
	b[3] = byte(now >> 16)
	b[4] = byte(now >> 8)
	b[5] = byte(now)
	if _, err := rand.Read(b[6:]); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return ulidEnc.EncodeToString(b[:])
}

// makeCursor encodes a position as the opaque cursor the consumer echoes back
// (event-protocol.md §8.4, §9.3). On SQLite the cursor is the row seq wrapped
// with the live generation token: "<generation>.<seq>". It is single-line and
// newline-free (§9.3) so it rides verbatim on the SSE `id:` line and in the
// Last-Event-ID header. The consumer never parses it (§9.1).
func makeCursor(generation string, seq int64) string {
	return generation + "." + strconv.FormatInt(seq, 10)
}

// parseCursor splits an opaque cursor back into its generation token and seq.
// ok is false when the cursor is unintelligible — missing the separator or
// carrying a non-integer seq — which the feed maps to a resync
// `unintelligible-cursor` (§10.1). The generation half is returned without
// further validation; the caller compares it against the live generation.
func parseCursor(cursor string) (generation string, seq int64, ok bool) {
	i := strings.LastIndexByte(cursor, '.')
	if i <= 0 || i == len(cursor)-1 {
		return "", 0, false
	}
	n, err := strconv.ParseInt(cursor[i+1:], 10, 64)
	if err != nil || n < 0 {
		return "", 0, false
	}
	return cursor[:i], n, true
}
