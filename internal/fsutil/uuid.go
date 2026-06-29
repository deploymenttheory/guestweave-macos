// UUID generation (replacing NSUUID().uuidString).
//go:build darwin

package fsutil

import (
	"crypto/rand"
	"fmt"
	"strings"
)

// UUID returns a random RFC 4122 version-4 UUID string, replacing
// NSUUID().uuidString (which is upper-cased).
func UUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return strings.ToUpper(fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]))
}
