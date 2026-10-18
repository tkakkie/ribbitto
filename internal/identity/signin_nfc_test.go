package identity

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

type rehashAccounts struct {
	hash    string
	unknown bool
	updates int
	err     error
}

func (f *rehashAccounts) AccountCredentials(context.Context, string) (kernel.ID, string, error) {
	if f.unknown {
		return kernel.ID{}, "", ErrNoAccount
	}
	return kernel.ID{7}, f.hash, nil
}

func (f *rehashAccounts) RehashPassword(_ context.Context, id kernel.ID, oldHash, newHash string) error {
	f.updates++
	if id != (kernel.ID{7}) || oldHash != f.hash {
		panic("rehash did not compare the verified credentials")
	}
	if f.err == nil {
		f.hash = newHash
	}
	return f.err
}

type signInSessions struct {
	SessionStore
	created int
}

func (f *signInSessions) CreateSession(_ context.Context, _ []byte, id kernel.ID, _ time.Time) error {
	if id != (kernel.ID{7}) {
		panic("wrong session account")
	}
	f.created++
	return nil
}

type countingHasher struct {
	verified []string
}

func (h *countingHasher) Verify(_ context.Context, password, stored string) (bool, error) {
	h.verified = append(h.verified, password)
	return password == stored, nil
}
func (h *countingHasher) verifyDummy(ctx context.Context, password string) error {
	_, err := h.Verify(ctx, password, "dummy")
	return err
}
func (*countingHasher) Hash(context.Context, string) (string, error) {
	panic("unexpected rehash")
}

func TestSignInVerificationCount(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		for _, right := range []bool{false, true} {
			for _, differs := range []bool{false, true} {
				t.Run(fmt.Sprintf("unknown=%t/right=%t/differs=%t", unknown, right, differs), func(t *testing.T) {
					input := "wrong password é"
					if right {
						input = "correct password é"
					}
					want := []string{input}
					if differs {
						input = input[:len(input)-len("é")] + "e\u0301"
						want = append(want, input)
					}
					h := &countingHasher{}
					accounts := &rehashAccounts{hash: "correct password é", unknown: unknown}
					sessions := &signInSessions{}
					s := &SignIn{accounts: accounts, hasher: h, sessions: NewSessions(sessions, time.Now, nil)}
					_, _, err := s.SignIn(t.Context(), "a@b", input, "")
					if (err == nil) != (right && !unknown) || err != nil && !errors.Is(err, ErrInvalidCredentials) {
						t.Fatalf("unexpected credential result: %v", err)
					}
					if !slices.Equal(h.verified, want) || accounts.updates != 0 {
						t.Fatalf("verification order = %q, want %q; updates = %d", h.verified, want, accounts.updates)
					}
				})
			}
		}
	}
}

func TestSignInPasswordNormalization(t *testing.T) {
	h, err := NewHasher()
	if err != nil {
		t.Fatal(err)
	}
	const nfc = "correct password é"
	const raw = "correct password e\u0301"
	for _, tc := range []struct {
		name, stored string
		inputs       []string
		updateErr    error
		wantUpdates  int
		wantSuccess  bool
	}{
		{"NFC account", nfc, []string{raw, nfc}, nil, 0, true},
		{"legacy migration", raw, []string{raw, nfc, raw}, nil, 1, true},
		{"already NFC input", nfc, []string{nfc}, nil, 0, true},
		{"wrong legacy attempt", raw, []string{"wrong password e\u0301"}, nil, 0, false},
		{"fullwidth stays distinct", "correct password Ａ", []string{"correct password A"}, nil, 0, false},
		{"update failure", raw, []string{raw}, errors.New("store unavailable"), 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hash, err := h.Hash(t.Context(), tc.stored)
			if err != nil {
				t.Fatal(err)
			}
			accounts := &rehashAccounts{hash: hash, err: tc.updateErr}
			sessions := &signInSessions{}
			s := NewSignIn(accounts, h, NewSessions(sessions, time.Now, nil))
			for _, input := range tc.inputs {
				_, _, err := s.SignIn(t.Context(), "a@b", input, "")
				if (err == nil) != tc.wantSuccess || err != nil && !errors.Is(err, ErrInvalidCredentials) {
					t.Fatalf("unexpected credential result: %v", err)
				}
			}
			if accounts.updates != tc.wantUpdates || (sessions.created == len(tc.inputs)) != tc.wantSuccess {
				t.Fatalf("updates = %d; sessions = %d", accounts.updates, sessions.created)
			}
			if tc.wantUpdates > 0 && tc.updateErr == nil {
				if ok, err := h.Verify(t.Context(), nfc, accounts.hash); err != nil || !ok || accounts.hash == hash {
					t.Fatalf("NFC hash was not persisted: %v", err)
				}
			}
		})
	}
}
