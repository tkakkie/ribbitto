package org_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

type signUpStore struct {
	org.RegistrationWriter
	steps   *[]string
	pending bool
	openErr error
	err     error
	t       *testing.T
}

func (s signUpStore) Open(context.Context) (bool, error) { return s.pending, s.openErr }
func (s signUpStore) CreateAccount(_ context.Context, email, name, hash string) (kernel.ID, error) {
	s.t.Helper()
	*s.steps = append(*s.steps, "account")
	if name != "Alice" || email != "alice@example.org" || !strings.HasPrefix(hash, "$argon2id$") {
		s.t.Fatalf("bad normalized account or hash: %s %s", name, email)
	}
	if errors.Is(s.err, org.ErrEmailTaken) {
		return kernel.ID{}, identity.ErrEmailTaken
	}
	if errors.Is(s.err, identity.ErrInvalidEmail) {
		return kernel.ID{}, s.err
	}
	return kernel.ID{1}, nil
}
func (s signUpStore) InTx(_ context.Context, fn func(platform.Tx) error) error {
	err := fn(platform.Tx{})
	if err == nil && strings.Join(*s.steps, ",") != "setup,sequence,account,member,event" {
		s.t.Fatalf("transaction order: %v", *s.steps)
	}
	return err
}
func (s signUpStore) SetupOrganization(context.Context) (kernel.ID, error) {
	*s.steps = append(*s.steps, "setup")
	if errors.Is(s.err, org.ErrSignUpClosed) {
		return kernel.ID{}, s.err
	}
	return kernel.ID{2}, nil
}
func (s signUpStore) NextEventSeq(_ context.Context, organizationID kernel.ID) (int64, error) {
	*s.steps = append(*s.steps, "sequence")
	if organizationID != (kernel.ID{2}) {
		s.t.Fatal("wrong organization")
	}
	return 3, nil
}
func (s signUpStore) CreateMember(_ context.Context, organizationID, accountID kernel.ID, role org.Role, seq int64, handle string) (kernel.ID, error) {
	*s.steps = append(*s.steps, "member")
	if organizationID != (kernel.ID{2}) || accountID != (kernel.ID{1}) || role != org.RoleMember || seq != 3 || handle != "alice" {
		s.t.Fatal("bad member")
	}
	return kernel.ID{4}, s.err
}
func (s signUpStore) Append(_ context.Context, organizationID kernel.ID, seq int64, kind realtime.EventKind, audience *kernel.ID, payload []byte) error {
	*s.steps = append(*s.steps, "event")
	if organizationID != (kernel.ID{2}) || seq != 3 || kind != org.KindJoined || audience != nil || string(payload) != string(org.EncodeJoined(kernel.ID{4})) {
		s.t.Fatal("bad joined event")
	}
	return nil
}

func TestSignUp(t *testing.T) {
	hasher, err := identity.NewHasher()
	if err != nil {
		t.Fatal(err)
	}
	unexpected := errors.New("store unavailable")
	for _, tc := range []struct {
		label, name, handle, email, password, field, storeField string
		off, pending, openFailure                               bool
		want                                                    error
	}{
		{label: "normalized account", name: " Alice ", handle: " Alice ", email: " Alice@Example.org ", password: "long enough password"},
		{label: "duplicate email", name: "Alice", handle: "alice", email: "alice@example.org", password: "long enough password", want: org.ErrEmailTaken},
		{label: "duplicate handle", name: "Alice", handle: "alice", email: "alice@example.org", password: "long enough password", want: org.ErrHandleTaken},
		{label: "store invalid email", name: "Alice", handle: "alice", email: "alice@example.org", password: "long enough password", storeField: "email", want: identity.ErrInvalidEmail},
		{label: "store invalid handle", name: "Alice", handle: "alice", email: "alice@example.org", password: "long enough password", storeField: "handle", want: org.ErrInvalidHandle},
		{label: "setup row missing", name: "Alice", handle: "alice", email: "alice@example.org", password: "long enough password", want: org.ErrSignUpClosed},
		{label: "unexpected store error", name: "Alice", handle: "alice", email: "alice@example.org", password: "long enough password", want: unexpected},
		{label: "open failure", openFailure: true, want: unexpected},
		{label: "invalid name", field: "display_name", handle: "alice", email: "a@b", password: "long enough password"},
		{label: "invalid handle", name: "Alice", field: "handle", email: "a@b", password: "long enough password"},
		{label: "reserved handle", name: "Alice", handle: "everyone", field: "handle", email: "a@b", password: "long enough password"},
		{label: "invalid email", name: "Alice", handle: "alice", field: "email", password: "long enough password"},
		{label: "invalid password", name: "Alice", handle: "alice", email: "a@b", field: "password"},
		{label: "disabled", off: true, want: org.ErrSignUpClosed},
		{label: "setup pending", pending: true, want: org.ErrSignUpClosed},
	} {
		t.Run(tc.label, func(t *testing.T) {
			h := hasher
			// Rejections must not reach hashing.
			if tc.field != "" || tc.off || tc.pending || tc.openFailure {
				h = nil
			}
			var steps []string
			store := signUpStore{steps: &steps, pending: tc.pending, err: tc.want, t: t}
			service := org.NewSignUp(store, store,
				func(platform.Tx) org.RegistrationWriter { return store },
				func(platform.Tx) org.AccountCreator { return store },
				func(platform.Tx) org.EventAppender { return store }, h, !tc.off)
			if tc.openFailure {
				store.openErr = tc.want
				service = org.NewSignUp(store, nil, nil, nil, nil, nil, true)
			}
			open, err := service.Open(t.Context())
			if !errors.Is(err, store.openErr) || open != (!tc.off && !tc.pending && !tc.openFailure) {
				t.Fatalf("Open: %t %v", open, err)
			}
			if tc.openFailure && err.Error() != "checking sign-up: "+unexpected.Error() {
				t.Fatalf("Open missing context: %v", err)
			}
			id, err := service.SignUp(t.Context(), tc.name, tc.handle, tc.email, tc.password)
			var fields org.ValidationErrors
			field := tc.field
			if tc.storeField != "" {
				field = tc.storeField
			}
			if field != "" {
				if !errors.As(err, &fields) || len(fields) != 1 || fields[field] == nil || tc.storeField != "" && !errors.Is(fields[field], tc.want) {
					t.Fatalf("field %s: %v", field, err)
				}
			} else if !errors.Is(err, tc.want) || (err == nil && id != (kernel.ID{1})) {
				t.Fatalf("case %+v: %v %v", tc, id, err)
			}
			if tc.openFailure && (len(steps) != 0 || err.Error() != "checking sign-up: "+unexpected.Error()) {
				t.Fatalf("Open failure reached writes or added context twice: %v, %v", steps, err)
			}
			if tc.want == unexpected && !tc.openFailure && err.Error() != "storing sign-up transaction: "+unexpected.Error() {
				t.Fatalf("unexpected wrapping: %v", err)
			}
		})
	}
}
