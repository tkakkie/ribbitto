package identity

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

// failOn is the 1-based Verify or verifyDummy call to fail, because the
// unknown-email rows must tell the first dummy verification from the second.
type signInErrHasher struct {
	verifies map[string]bool
	failOn   int
	err      error
	hashErr  error
	calls    []string
	hashes   int
}

func (h *signInErrHasher) Verify(_ context.Context, password, _ string) (bool, error) {
	h.calls = append(h.calls, password)
	if len(h.calls) == h.failOn {
		return false, h.err
	}
	return h.verifies[password], nil
}

func (h *signInErrHasher) verifyDummy(_ context.Context, password string) error {
	h.calls = append(h.calls, password)
	if len(h.calls) == h.failOn {
		return h.err
	}
	return nil
}

func (h *signInErrHasher) Hash(context.Context, string) (string, error) {
	h.hashes++
	return "new", h.hashErr
}

type signInErrAccounts struct {
	rehashAccounts
	credErr error
}

func (a *signInErrAccounts) AccountCredentials(ctx context.Context, email string) (kernel.ID, string, error) {
	if a.credErr != nil {
		return kernel.ID{}, "", a.credErr
	}
	return a.rehashAccounts.AccountCredentials(ctx, email)
}

func TestSignInErrorReturns(t *testing.T) {
	const (
		raw = "pw e\u0301"
		nfc = "pw é"
	)
	if got := normalizePassword(raw); got != nfc {
		t.Fatalf("normalizePassword(%q) = %q, want %q", raw, got, nfc)
	}
	errScripted := errors.New("hasher failed")
	credErr := errors.New("db down")

	for _, tc := range []struct {
		name        string
		unknown     bool
		credErr     error
		password    string
		verifies    map[string]bool
		failOn      int
		hashErr     error
		wantErr     error
		wantDirect  bool
		wantErrText string
		wantCalls   []string
		wantHashes  int
		wantCreated int
	}{
		{
			name:        "unknown email first dummy verify error",
			unknown:     true,
			password:    "pw",
			failOn:      1,
			wantErr:     errScripted,
			wantDirect:  true,
			wantErrText: "hasher failed",
			wantCalls:   []string{"pw"},
		},
		{
			name:        "unknown email second dummy verify error",
			unknown:     true,
			password:    raw,
			failOn:      2,
			wantErr:     errScripted,
			wantDirect:  true,
			wantErrText: "hasher failed",
			wantCalls:   []string{nfc, raw},
		},
		{
			name:        "known account NFC verify error",
			password:    "pw",
			failOn:      1,
			wantErr:     errScripted,
			wantErrText: "verifying password: hasher failed",
			wantCalls:   []string{"pw"},
		},
		{
			// The NFC check says yes, so the legacy check is reached and its
			// error must fail the sign-in closed with no session.
			name:        "known account legacy verify error after NFC match",
			password:    raw,
			verifies:    map[string]bool{nfc: true},
			failOn:      2,
			wantErr:     errScripted,
			wantErrText: "verifying legacy password: hasher failed",
			wantCalls:   []string{nfc, raw},
		},
		{
			// A failed Hash skips the migration and never calls RehashPassword
			// (accounts.updates stays 0), while the sign-in still succeeds.
			name:        "known account legacy migration hash error is best effort",
			password:    raw,
			verifies:    map[string]bool{raw: true},
			hashErr:     errScripted,
			wantCalls:   []string{nfc, raw},
			wantHashes:  1,
			wantCreated: 1,
		},
		{
			name:        "account credentials store error",
			credErr:     credErr,
			password:    "pw",
			wantErr:     credErr,
			wantErrText: "finding account: db down",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			accounts := &signInErrAccounts{
				rehashAccounts: rehashAccounts{hash: "stored", unknown: tc.unknown},
				credErr:        tc.credErr,
			}
			h := &signInErrHasher{
				verifies: tc.verifies,
				failOn:   tc.failOn,
				err:      errScripted,
				hashErr:  tc.hashErr,
			}
			sessions := &signInSessions{}
			s := &SignIn{
				accounts: accounts,
				hasher:   h,
				sessions: NewSessions(sessions, time.Now, nil),
			}

			token, expiry, err := s.SignIn(t.Context(), "a@b", tc.password, "")
			if tc.wantErr != nil {
				if err == nil || err.Error() != tc.wantErrText {
					t.Errorf("err = %v, want exact %q", err, tc.wantErrText)
				}
				if tc.wantDirect {
					if err != tc.wantErr {
						t.Errorf("err = %v, want exact errScripted identity", err)
					}
				} else if !errors.Is(err, tc.wantErr) {
					t.Errorf("err %v does not wrap %v", err, tc.wantErr)
				}
				if errors.Is(err, ErrInvalidCredentials) {
					t.Errorf("err %v must not match ErrInvalidCredentials", err)
				}
				if token != "" || !expiry.IsZero() {
					t.Errorf("got token=%q expiry=%v, want empty token and zero expiry", token, expiry)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if token == "" || expiry.IsZero() {
					t.Errorf("got token=%q expiry=%v, want non-empty token and non-zero expiry", token, expiry)
				}
			}
			if !slices.Equal(h.calls, tc.wantCalls) {
				t.Errorf("calls = %q, want %q", h.calls, tc.wantCalls)
			}
			if h.hashes != tc.wantHashes {
				t.Errorf("hashes = %d, want %d", h.hashes, tc.wantHashes)
			}
			if sessions.created != tc.wantCreated {
				t.Errorf("sessions.created = %d, want %d", sessions.created, tc.wantCreated)
			}
			if accounts.updates != 0 {
				t.Errorf("accounts.updates = %d, want 0", accounts.updates)
			}
		})
	}
}

type signInErrSessionStore struct {
	SessionStore
	gotAccountID kernel.ID
	gotCurrentID kernel.ID
	others       []kernel.ID
	horizon      kernel.ID
	err          error
}

func (s *signInErrSessionStore) DeleteOtherSessions(_ context.Context, accountID, currentID kernel.ID) ([]kernel.ID, error) {
	s.gotAccountID = accountID
	s.gotCurrentID = currentID
	if s.err != nil {
		return nil, s.err
	}
	return s.others, nil
}

func (s *signInErrSessionStore) SessionHorizon(context.Context) (kernel.ID, error) {
	// Return horizon with the error so a mutation that forwards the store's ID
	// on error is caught.
	return s.horizon, s.err
}

type signInErrCanceller struct {
	sessions []kernel.ID
	accounts []kernel.ID
}

func (c *signInErrCanceller) CancelSession(id kernel.ID) {
	c.sessions = append(c.sessions, id)
}

func (c *signInErrCanceller) CancelAccount(id kernel.ID) {
	c.accounts = append(c.accounts, id)
}

func TestSignOutOthersAndHorizon(t *testing.T) {
	accountID := kernel.ID{1}
	currentID := kernel.ID{2}
	otherID := kernel.ID{3}
	horizonID := kernel.ID{0x42}
	errScripted := errors.New("hasher failed")

	for _, tc := range []struct {
		name               string
		err                error
		wantSignOutErrText string
		wantCancelSessions []kernel.ID
		wantCancelAccounts []kernel.ID
		wantHorizon        kernel.ID
		wantHorizonErrText string
	}{
		{
			name:               "success",
			wantCancelSessions: []kernel.ID{otherID},
			wantHorizon:        horizonID,
		},
		{
			name:               "failure",
			err:                errScripted,
			wantSignOutErrText: "deleting other sessions: hasher failed",
			wantCancelAccounts: []kernel.ID{accountID},
			wantHorizonErrText: "reading session horizon: hasher failed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &signInErrSessionStore{
				others:  []kernel.ID{otherID},
				horizon: horizonID,
				err:     tc.err,
			}
			cancel := &signInErrCanceller{}
			s := &SignIn{sessions: NewSessions(store, time.Now, cancel)}

			err := s.SignOutOthers(t.Context(), accountID, currentID)
			if tc.err != nil {
				if err == nil || err.Error() != tc.wantSignOutErrText {
					t.Errorf("SignOutOthers err = %v, want exact %q", err, tc.wantSignOutErrText)
				}
				if !errors.Is(err, tc.err) {
					t.Errorf("SignOutOthers err %v does not wrap %v", err, tc.err)
				}
			} else if err != nil {
				t.Errorf("SignOutOthers unexpected error: %v", err)
			}
			if store.gotAccountID != accountID {
				t.Errorf("DeleteOtherSessions accountID = %v, want %v", store.gotAccountID, accountID)
			}
			if store.gotCurrentID != currentID {
				t.Errorf("DeleteOtherSessions currentID = %v, want %v", store.gotCurrentID, currentID)
			}
			if !slices.Equal(cancel.sessions, tc.wantCancelSessions) {
				t.Errorf("CancelSession calls = %v, want %v", cancel.sessions, tc.wantCancelSessions)
			}
			if !slices.Equal(cancel.accounts, tc.wantCancelAccounts) {
				t.Errorf("CancelAccount calls = %v, want %v", cancel.accounts, tc.wantCancelAccounts)
			}

			gotHorizon, err := s.SessionHorizon(t.Context())
			if tc.err != nil {
				if err == nil || err.Error() != tc.wantHorizonErrText {
					t.Errorf("SessionHorizon err = %v, want exact %q", err, tc.wantHorizonErrText)
				}
				if !errors.Is(err, tc.err) {
					t.Errorf("SessionHorizon err %v does not wrap %v", err, tc.err)
				}
			} else if err != nil {
				t.Errorf("SessionHorizon unexpected error: %v", err)
			}
			if gotHorizon != tc.wantHorizon {
				t.Errorf("SessionHorizon id = %v, want %v", gotHorizon, tc.wantHorizon)
			}
		})
	}
}
