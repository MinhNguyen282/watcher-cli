package harness

import (
	"crypto/rand"
	"encoding/hex"
)

// shortRandom returns 4 random bytes as a hex string, enough to make
// worktree directories and branches unique within a run.
func shortRandom() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is essentially impossible; fall back to
		// a fixed value rather than aborting the probe setup.
		return "deadbeef"
	}
	return hex.EncodeToString(b[:])
}
