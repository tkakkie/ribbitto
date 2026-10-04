package org_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

type signUpStore struct {
	org.RegistrationWriter
	steps   *[]string
	pending bool
	err     error
	t       *testing.T
}

func (s signUpStore) Open(context.Context) (bool, error) { return s.pending, nil }
func (s signUpStore) CreateAccount(_ context.Context, email, name, hash string) (domain.ID, error) {
	s.t.Helper()
	*s.steps = append(*s.steps, "account")
	if name != "Alice" || email != "alice@example.org" || !strings.HasPrefix(hash, "$argon2id$") {
		s.t.Fatalf("bad normalized account or hash: %s %s", name, email)
	}
	if errors.Is(s.err, org.ErrEmailTaken) {
		return domain.ID{}, identity.ErrEmailTaken
	}
	return domain.ID{1}, nil
}
func (s signUpStore) InTx(_ context.Context, fn func(platform.Tx) error) error {
	err := fn(platform.Tx{})
	if err == nil && strings.Join(*s.steps, ",") != "setup,sequence,account,member,event" {
		s.t.Fatalf("transaction order: %v", *s.steps)
	}
	return err
}
func (s signUpStore) SetupOrganization(context.Context) (domain.ID, error) {
	*s.steps = append(*s.steps, "setup")
	return domain.ID{2}, nil
}
func (s signUpStore) NextEventSeq(_ context.Context, organizationID domain.ID) (int64, error) {
	*s.steps = append(*s.steps, "sequence")
	if organizationID != (domain.ID{2}) {
		s.t.Fatal("wrong organization")
	}
	return 3, nil
}
func (s signUpStore) CreateMember(_ context.Context, organizationID, accountID domain.ID, role org.Role, seq int64, handle string) (domain.ID, error) {
	*s.steps = append(*s.steps, "member")
	if organizationID != (domain.ID{2}) || accountID != (domain.ID{1}) || role != org.RoleMember || seq != 3 || handle != "alice" {
		s.t.Fatal("bad member")
	}
	return domain.ID{4}, s.err
}
func (s signUpStore) Append(_ context.Context, organizationID domain.ID, seq int64, kind realtime.EventKind, audience *domain.ID, payload []byte) error {
	*s.steps = append(*s.steps, "event")
	if organizationID != (domain.ID{2}) || seq != 3 || kind != org.KindJoined || audience != nil || string(payload) != string(org.EncodeJoined(domain.ID{4})) {
		s.t.Fatal("bad joined event")
	}
	return nil
}

func TestSignUp(t *testing.T) {
	hasher, err := identity.NewHasher()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		label, name, handle, email, password, field string
		off, pending                                bool
		want                                        error
	}{
		{label: "normalized account", name: " Alice ", handle: " Alice ", email: " Alice@Example.org ", password: "long enough password"},
		{label: "duplicate email", name: "Alice", handle: "alice", email: "alice@example.org", password: "long enough password", want: org.ErrEmailTaken},
		{label: "duplicate handle", name: "Alice", handle: "alice", email: "alice@example.org", password: "long enough password", want: org.ErrHandleTaken},
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
			if tc.field != "" || tc.off || tc.pending {
				h = nil
			}
			var steps []string
			store := signUpStore{steps: &steps, pending: tc.pending, err: tc.want, t: t}
			service := org.NewSignUp(store, store,
				func(platform.Tx) org.RegistrationWriter { return store },
				func(platform.Tx) org.AccountCreator { return store },
				func(platform.Tx) org.EventAppender { return store }, h, !tc.off)
			open, err := service.Open(t.Context())
			if err != nil || open != (!tc.off && !tc.pending) {
				t.Fatalf("Open: %t %v", open, err)
			}
			id, err := service.SignUp(t.Context(), tc.name, tc.handle, tc.email, tc.password)
			var fields org.ValidationErrors
			if tc.field != "" {
				if !errors.As(err, &fields) || len(fields) != 1 || fields[tc.field] == nil {
					t.Fatalf("field %s: %v", tc.field, err)
				}
			} else if !errors.Is(err, tc.want) || (err == nil && id != (domain.ID{1})) {
				t.Fatalf("case %+v: %v %v", tc, id, err)
			}
		})
	}
}
