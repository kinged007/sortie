package orchestrator

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash"
	"slices"
	"sync"
	"time"

	"github.com/sortie-ai/sortie/internal/domain"
)

// verificationWindow is how long a proof of one agent setup keeps the next
// run from verifying the credential again. A stale proof costs one failed
// first turn and a retry that verifies, not an authorization exposure.
const verificationWindow = 15 * time.Minute

// verificationKey digests the inputs that decide whether two runs share one
// credential proof. It is never logged, put in an event or an error, or
// persisted: the settings it digests can hold resolved secrets.
type verificationKey [sha256.Size]byte

// verificationCache remembers which agent setups proved their credential
// recently. A nil cache never reports a proof and records nothing.
type verificationCache struct {
	mu     sync.Mutex
	proven map[verificationKey]time.Time // guarded by mu
}

func newVerificationCache() *verificationCache {
	return &verificationCache{proven: make(map[verificationKey]time.Time)}
}

func (c *verificationCache) fresh(key verificationKey, now time.Time) (provenAt time.Time, ok bool) {
	if c == nil {
		return time.Time{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	provenAt, found := c.proven[key]
	if !found {
		return time.Time{}, false
	}
	return provenAt, now.Sub(provenAt) < verificationWindow
}

func (c *verificationCache) record(key verificationKey, at time.Time) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.proven[key] = at
}

func (c *verificationCache) evict(key verificationKey) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.proven, key)
}

// verificationKeyFor leaves the workspace path, the resume fields, and the MCP
// config path out of the digest because a verification session clears or
// ignores them. It reports false when the settings cannot be encoded, so such a
// run verifies and records nothing.
func verificationKeyFor(kind string, params domain.StartSessionParams) (verificationKey, bool) {
	settings, err := json.Marshal(params.Settings)
	if err != nil {
		return verificationKey{}, false
	}

	digest := sha256.New()
	writeKeyField(digest, kind)
	if len(params.AgentConfig.CommandArgv) > 0 {
		writeKeyField(digest, "argv")
		writeKeyList(digest, params.AgentConfig.CommandArgv)
	} else {
		writeKeyField(digest, "string")
		writeKeyField(digest, params.AgentConfig.Command)
	}
	writeKeyField(digest, params.SSHHost)
	writeKeyList(digest, slices.Sorted(slices.Values(params.SSHEnvNames)))
	writeKeyField(digest, string(settings))

	var key verificationKey
	digest.Sum(key[:0])
	return key, true
}

// writeKeyField length-prefixes value so adjacent fields cannot run together
// into a collision.
func writeKeyField(digest hash.Hash, value string) {
	var prefix [8]byte
	binary.BigEndian.PutUint64(prefix[:], uint64(len(value)))
	digest.Write(prefix[:])
	digest.Write([]byte(value))
}

func writeKeyList(digest hash.Hash, values []string) {
	var count [8]byte
	binary.BigEndian.PutUint64(count[:], uint64(len(values)))
	digest.Write(count[:])
	for _, value := range values {
		writeKeyField(digest, value)
	}
}

func refusesCredential(err error) bool {
	agentErr, ok := errors.AsType[*domain.AgentError](err)
	return ok && agentErr.Kind == domain.ErrCredentialUnverified
}
