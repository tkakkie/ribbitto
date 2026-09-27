package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

// Default Argon2id parameters for new hashes (DECISIONS.md 10).
const (
	defaultMemoryKiB = 19 * 1024
	defaultTime      = 2
	defaultThreads   = 1
	defaultSaltLen   = 16
	defaultKeyLen    = 32
)

// Bounds for stored hashes. A stored hash is not trusted input: argon2.IDKey
// panics on zero time or threads and allocates whatever memory it is asked
// for, so every parameter is checked before it reaches Argon2id.
const (
	minMemoryKiB = 8 * 1024
	maxMemoryKiB = 64 * 1024
	minTime      = 1
	maxTime      = 10
	minThreads   = 1
	maxThreads   = 4
	minBytes     = 16
	maxBytes     = 64
	maxSlots     = 4
	slotWait     = 5 * time.Second
	// maxEncodedLen is the length of the longest string parse accepts:
	// "$argon2id$v=19$m=65536,t=10,p=4$" plus 64-byte salt and key in base64.
	// Checking it first keeps a huge stored value from being split or decoded.
	maxEncodedLen = 205
)

// ErrBusy means every hashing slot stayed taken until the wait ended or the
// caller's context was done. Nothing was computed; handlers answer 503.
var ErrBusy = errors.New("password hasher busy")

// ErrInvalidHash means a stored hash is malformed or outside the supported
// parameters.
var ErrInvalidHash = errors.New("invalid password hash")

// Hasher hashes and verifies passwords with Argon2id. Construct one per
// process and share it: its slots bound the CPU and memory that concurrent
// hashing can use.
type Hasher struct {
	slots chan struct{}
	wait  time.Duration
	now   func() time.Time
	dummy string
}

// NewHasher returns a Hasher with min(GOMAXPROCS, 4) slots. GOMAXPROCS, not
// NumCPU, because it follows the container's CPU limit.
func NewHasher() (*Hasher, error) {
	return newHasher(min(runtime.GOMAXPROCS(0), maxSlots), slotWait)
}

func newHasher(slots int, wait time.Duration) (*Hasher, error) {
	h := &Hasher{slots: make(chan struct{}, slots), wait: wait, now: time.Now}
	// The dummy hash protects an unknown account's sign-in with the same
	// work as a real one, so it must use the current parameters.
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("creating dummy password: %w", err)
	}
	dummy, err := encode(string(secret))
	if err != nil {
		return nil, err
	}
	h.dummy = dummy
	return h, nil
}

// Hash returns the PHC string of password with the default parameters.
func (h *Hasher) Hash(ctx context.Context, password string) (string, error) {
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	return encode(password)
}

// Verify reports whether password matches the stored PHC string. A wrong
// password is (false, nil); a malformed hash is ErrInvalidHash.
func (h *Hasher) Verify(ctx context.Context, password, stored string) (bool, error) {
	p, err := parse(stored)
	if err != nil {
		return false, err
	}
	if err := h.acquire(ctx); err != nil {
		return false, err
	}
	defer h.release()
	key := argon2.IDKey([]byte(password), p.salt, p.time, p.memory, p.threads, uint32(len(p.key)))
	return subtle.ConstantTimeCompare(key, p.key) == 1, nil
}

// VerifyDummy does the work of a verification against a hash no password
// matches. Sign-in calls it for unknown emails so that response time does
// not tell which accounts exist.
func (h *Hasher) VerifyDummy(ctx context.Context, password string) error {
	_, err := h.Verify(ctx, password, h.dummy)
	return err
}

func (h *Hasher) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrBusy, err)
	}
	deadline := h.now().Add(h.wait)
	timer := time.NewTimer(h.wait)
	defer timer.Stop()
	select {
	case h.slots <- struct{}{}:
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", ErrBusy, ctx.Err())
	case <-timer.C:
		return ErrBusy
	}
	// select picks at random among ready cases, and this goroutine may run
	// late: recheck so that a caller past its context or its wait never
	// starts Argon2id.
	if err := ctx.Err(); err != nil {
		h.release()
		return fmt.Errorf("%w: %w", ErrBusy, err)
	}
	if !h.now().Before(deadline) {
		h.release()
		return ErrBusy
	}
	return nil
}

func (h *Hasher) release() { <-h.slots }

type params struct {
	memory    uint32 // KiB
	time      uint32
	threads   uint8
	salt, key []byte
}

func encode(password string) (string, error) {
	salt := make([]byte, defaultSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("creating salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, defaultTime, defaultMemoryKiB, defaultThreads, defaultKeyLen)
	return format(params{defaultMemoryKiB, defaultTime, defaultThreads, salt, key}), nil
}

func format(p params) string {
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.memory, p.time, p.threads, b64.EncodeToString(p.salt), b64.EncodeToString(p.key))
}

// parse accepts only the canonical form that format produces, so there is
// exactly one spelling of each hash to reason about.
func parse(stored string) (params, error) {
	if len(stored) > maxEncodedLen {
		return params{}, ErrInvalidHash
	}
	parts := strings.Split(stored, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" ||
		parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return params{}, ErrInvalidHash
	}
	var p params
	fields := strings.Split(parts[3], ",")
	if len(fields) != 3 {
		return params{}, ErrInvalidHash
	}
	memory, ok1 := parseField(fields[0], "m=", minMemoryKiB, maxMemoryKiB)
	iterations, ok2 := parseField(fields[1], "t=", minTime, maxTime)
	threads, ok3 := parseField(fields[2], "p=", minThreads, maxThreads)
	if !ok1 || !ok2 || !ok3 {
		return params{}, ErrInvalidHash
	}
	p.memory, p.time, p.threads = uint32(memory), uint32(iterations), uint8(threads)
	b64 := base64.RawStdEncoding.Strict()
	var err error
	if p.salt, err = b64.DecodeString(parts[4]); err != nil || len(p.salt) < minBytes || len(p.salt) > maxBytes {
		return params{}, ErrInvalidHash
	}
	if p.key, err = b64.DecodeString(parts[5]); err != nil || len(p.key) < minBytes || len(p.key) > maxBytes {
		return params{}, ErrInvalidHash
	}
	// The base64 decoder skips newlines, so compare with the canonical form.
	if format(p) != stored {
		return params{}, ErrInvalidHash
	}
	return p, nil
}

func parseField(field, prefix string, lo, hi uint64) (uint64, bool) {
	digits, found := strings.CutPrefix(field, prefix)
	if !found {
		return 0, false
	}
	n, err := strconv.ParseUint(digits, 10, 32)
	// Reject non-canonical spellings such as leading zeros.
	if err != nil || strconv.FormatUint(n, 10) != digits || n < lo || n > hi {
		return 0, false
	}
	return n, true
}
