package identity

import (
	"context"
	"encoding/base64"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"
)

func newTestHasher(t *testing.T) *Hasher {
	t.Helper()
	h, err := NewHasher()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// hashWith builds a valid PHC string with non-default parameters, as an
// older or newer version of ribbitto might have stored.
func hashWith(password string, memory, iterations uint32, threads uint8, saltLen, keyLen int) string {
	salt := []byte(strings.Repeat("s", saltLen))
	key := argon2.IDKey([]byte(password), salt, iterations, memory, threads, uint32(keyLen))
	return format(params{memory, iterations, threads, salt, key})
}

func TestHashAndVerify(t *testing.T) {
	t.Parallel()
	h := newTestHasher(t)
	ctx := t.Context()
	first, err := h.Hash(ctx, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("unexpected encoding %q", first)
	}
	second, err := h.Hash(ctx, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("two hashes of one password are equal; salt is not random")
	}
	for _, tt := range []struct {
		name     string
		password string
		stored   string
		want     bool
	}{
		{"correct password", "correct horse battery", first, true},
		{"wrong password", "correct horse batterY", first, false},
		{"empty password", "", first, false},
		{"other parameters within bounds", "pw", hashWith("pw", 8*1024, 1, 4, 64, 16), true},
		{"other parameters, wrong password", "px", hashWith("pw", 8*1024, 1, 4, 64, 16), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := h.Verify(ctx, tt.password, tt.stored)
			if err != nil || got != tt.want {
				t.Fatalf("Verify = %v, %v; want %v, nil", got, err, tt.want)
			}
		})
	}
	if err := h.VerifyDummy(ctx, "anything"); err != nil {
		t.Fatalf("VerifyDummy: %v", err)
	}
	if !strings.HasPrefix(h.dummy, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("dummy hash does not use the current parameters: %q", h.dummy)
	}
}

func TestVerifyRejectsInvalidHashes(t *testing.T) {
	t.Parallel()
	h := newTestHasher(t)
	valid := hashWith("pw", 8*1024, 1, 1, 16, 16)
	salt := base64.RawStdEncoding.EncodeToString([]byte(strings.Repeat("s", 16)))
	key := base64.RawStdEncoding.EncodeToString([]byte(strings.Repeat("k", 16)))
	withParams := func(p string) string { return "$argon2id$v=19$" + p + "$" + salt + "$" + key }
	for _, tt := range []struct{ name, stored string }{
		{"empty", ""},
		{"plain text", "pw"},
		{"argon2i", strings.Replace(valid, "argon2id", "argon2i", 1)},
		{"bcrypt", "$2a$10$abcdefghijklmnopqrstuuvwxyzABCDEFGHIJKLMNOPQRSTUVWXY"},
		{"old version", strings.Replace(valid, "v=19", "v=16", 1)},
		{"missing version", strings.Replace(valid, "$v=19", "", 1)},
		{"extra field", valid + "$x"},
		{"zero time", withParams("m=8192,t=0,p=1")},
		{"zero threads", withParams("m=8192,t=1,p=0")},
		{"zero memory", withParams("m=0,t=1,p=1")},
		{"memory too small", withParams("m=8191,t=1,p=1")},
		{"memory too large", withParams("m=65537,t=1,p=1")},
		{"memory overflowing uint32", withParams("m=4294967296,t=1,p=1")},
		{"time too large", withParams("m=8192,t=11,p=1")},
		{"threads too large", withParams("m=8192,t=1,p=5")},
		{"threads overflowing uint8", withParams("m=8192,t=1,p=257")},
		{"negative", withParams("m=-8192,t=1,p=1")},
		{"leading zero", withParams("m=08192,t=1,p=1")},
		{"plus sign", withParams("m=+8192,t=1,p=1")},
		{"reordered", withParams("t=1,m=8192,p=1")},
		{"missing field", withParams("m=8192,t=1")},
		{"extra parameter", withParams("m=8192,t=1,p=1,k=1")},
		{"padded base64", "$argon2id$v=19$m=8192,t=1,p=1$" + salt + "==$" + key},
		{"invalid base64", "$argon2id$v=19$m=8192,t=1,p=1$!!!!$" + key},
		{"newline in base64", "$argon2id$v=19$m=8192,t=1,p=1$" + salt[:4] + "\n" + salt[4:] + "$" + key},
		{"salt too short", "$argon2id$v=19$m=8192,t=1,p=1$" + base64.RawStdEncoding.EncodeToString(make([]byte, 15)) + "$" + key},
		{"salt too long", "$argon2id$v=19$m=8192,t=1,p=1$" + base64.RawStdEncoding.EncodeToString(make([]byte, 65)) + "$" + key},
		{"key too short", "$argon2id$v=19$m=8192,t=1,p=1$" + salt + "$" + base64.RawStdEncoding.EncodeToString(make([]byte, 15))},
		{"key too long", "$argon2id$v=19$m=8192,t=1,p=1$" + salt + "$" + base64.RawStdEncoding.EncodeToString(make([]byte, 65))},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ok, err := h.Verify(t.Context(), "pw", tt.stored)
			if ok || !errors.Is(err, ErrInvalidHash) {
				t.Fatalf("Verify = %v, %v; want false, ErrInvalidHash", ok, err)
			}
		})
	}
}

func TestBusy(t *testing.T) {
	t.Parallel()
	h, err := newHasher(1, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	stored := hashWith("pw", 8*1024, 1, 1, 16, 16)
	h.slots <- struct{}{} // hold the only slot
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tt := range []struct {
		name string
		ctx  context.Context
	}{
		{"wait ends", t.Context()},
		{"context cancelled", cancelled},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := h.Hash(tt.ctx, "pw"); !errors.Is(err, ErrBusy) {
				t.Fatalf("Hash: want ErrBusy, got %v", err)
			}
			if _, err := h.Verify(tt.ctx, "pw", stored); !errors.Is(err, ErrBusy) {
				t.Fatalf("Verify: want ErrBusy, got %v", err)
			}
			if err := h.VerifyDummy(tt.ctx, "pw"); !errors.Is(err, ErrBusy) {
				t.Fatalf("VerifyDummy: want ErrBusy, got %v", err)
			}
		})
	}
	h.release()
	if ok, err := h.Verify(t.Context(), "pw", stored); !ok || err != nil {
		t.Fatalf("after release: Verify = %v, %v", ok, err)
	}
}

func TestCancelledContextWithFreeSlot(t *testing.T) {
	t.Parallel()
	h := newTestHasher(t)
	stored := hashWith("pw", 8*1024, 1, 1, 16, 16)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	// Run many times: a race between a free slot and a done context would
	// show up as an occasional success.
	for range 100 {
		if _, err := h.Hash(ctx, "pw"); !errors.Is(err, ErrBusy) {
			t.Fatalf("Hash: want ErrBusy, got %v", err)
		}
		if _, err := h.Verify(ctx, "pw", stored); !errors.Is(err, ErrBusy) {
			t.Fatalf("Verify: want ErrBusy, got %v", err)
		}
		if err := h.VerifyDummy(ctx, "pw"); !errors.Is(err, ErrBusy) {
			t.Fatalf("VerifyDummy: want ErrBusy, got %v", err)
		}
	}
	if len(h.slots) != 0 {
		t.Fatalf("%d slots still held", len(h.slots))
	}
}

func TestWaitExpiredWithFreeSlot(t *testing.T) {
	t.Parallel()
	h, err := newHasher(1, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	calls := 0
	// The first reading sets the deadline; every later one is past it, as
	// when the goroutine resumes late and finds a slot free.
	h.now = func() time.Time {
		calls++
		if calls == 1 {
			return start
		}
		return start.Add(time.Second)
	}
	if _, err := h.Hash(t.Context(), "pw"); !errors.Is(err, ErrBusy) {
		t.Fatalf("Hash: want ErrBusy, got %v", err)
	}
	if len(h.slots) != 0 {
		t.Fatal("slot still held after an expired wait")
	}
}

// Not parallel: it measures the bytes allocated while parse runs, and
// parallel tests stay paused until the sequential ones have finished.
func TestParseBoundsAllocation(t *testing.T) {
	longest := format(params{maxMemoryKiB, maxTime, maxThreads, make([]byte, maxBytes), make([]byte, maxBytes)})
	if len(longest) != maxEncodedLen {
		t.Fatalf("maxEncodedLen = %d, longest accepted hash has %d bytes", maxEncodedLen, len(longest))
	}
	if _, err := parse(longest); err != nil {
		t.Fatalf("longest valid hash rejected: %v", err)
	}
	const huge = 16 << 20
	for _, tt := range []struct{ name, stored string }{
		{"one byte too long", longest + "A"},
		{"huge salt", "$argon2id$v=19$m=8192,t=1,p=1$" + strings.Repeat("A", huge) + "$" + strings.Repeat("A", 22)},
		{"huge salt of newlines", "$argon2id$v=19$m=8192,t=1,p=1$" + strings.Repeat("\n", huge) + "$" + strings.Repeat("A", 22)},
		{"huge key", "$argon2id$v=19$m=8192,t=1,p=1$" + strings.Repeat("A", 22) + "$" + strings.Repeat("A", huge)},
		{"many delimiters", strings.Repeat("$", huge)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			_, err := parse(tt.stored)
			runtime.ReadMemStats(&after)
			if !errors.Is(err, ErrInvalidHash) {
				t.Fatalf("parse: want ErrInvalidHash, got %v", err)
			}
			if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 64<<10 {
				t.Fatalf("parse allocated %d bytes for a %d-byte input", allocated, len(tt.stored))
			}
		})
	}
}

func TestDummyHashTakesASlot(t *testing.T) {
	t.Parallel()
	// With no slot ever free, building the dummy hash must fail as busy,
	// which shows it goes through the semaphore like every other hash.
	if _, err := newHasher(0, 10*time.Millisecond); !errors.Is(err, ErrBusy) {
		t.Fatalf("newHasher without slots: want ErrBusy, got %v", err)
	}
}

func TestSlots(t *testing.T) {
	t.Parallel()
	h := newTestHasher(t)
	if n := cap(h.slots); n < 1 || n > maxSlots {
		t.Fatalf("slots = %d, want 1..%d", n, maxSlots)
	}
}

func FuzzParse(f *testing.F) {
	f.Add(hashWith("pw", 8*1024, 1, 1, 16, 16))
	f.Add("$argon2id$v=19$m=0,t=0,p=0$$")
	f.Add("$argon2id$v=19$m=99999999999,t=1,p=1$c2FsdHNhbHRzYWx0c2FsdA$a2V5a2V5a2V5a2V5a2V5")
	f.Fuzz(func(t *testing.T, stored string) {
		p, err := parse(stored)
		if err != nil {
			return
		}
		if p.memory < minMemoryKiB || p.memory > maxMemoryKiB || p.time < minTime || p.time > maxTime ||
			p.threads < minThreads || p.threads > maxThreads ||
			len(p.salt) < minBytes || len(p.salt) > maxBytes || len(p.key) < minBytes || len(p.key) > maxBytes {
			t.Fatalf("parse accepted out-of-bounds parameters: %+v", p)
		}
		if format(p) != stored {
			t.Fatalf("parse accepted a non-canonical spelling: %q", stored)
		}
	})
}
