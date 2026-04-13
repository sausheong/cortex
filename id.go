package cortex

import (
	"math/rand"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

var (
	entropy     = rand.New(rand.NewSource(time.Now().UnixNano()))
	entropyOnce sync.Once
	entropyMu   sync.Mutex
)

// newID generates a new ULID string.
func newID() string {
	entropyMu.Lock()
	defer entropyMu.Unlock()
	return ulid.MustNew(ulid.Timestamp(time.Now()), entropy).String()
}
