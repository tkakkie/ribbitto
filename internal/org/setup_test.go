package org_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

type setupState bool

func (s setupState) Open(context.Context) (bool, error) { return bool(s), nil }

func TestRejectedSetup(t *testing.T) {
	for _, tc := range []struct {
		name, configured, submitted, field string
		closed                             bool
		want                               error
	}{
		{name: "wrong token", configured: "secret", submitted: "wrong", want: org.ErrSetupToken},
		{name: "empty configured", want: org.ErrSetupToken},
		{name: "completed", configured: "secret", submitted: "secret", closed: true, want: org.ErrSetupCompleted},
		{name: "email", field: "email"},
		{name: "display name", field: "display_name"},
		{name: "password", field: "password"},
		{name: "organization name", field: "organization_name"},
		{name: "slug", field: "slug"},
		{name: "handle", field: "handle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := org.SetupInput{OrganizationName: "Example", Slug: "example", Email: "a@b", DisplayName: "Owner", Handle: "owner", Password: "long enough password"}
			if tc.field != "" {
				tc.configured, tc.submitted = "secret", "secret"
				fields := map[string]*string{"email": &input.Email, "display_name": &input.DisplayName, "password": &input.Password, "organization_name": &input.OrganizationName, "slug": &input.Slug, "handle": &input.Handle}
				*fields[tc.field] = ""
			}
			// The nil hasher and runner panic if rejection reaches hashing or writes.
			s := org.NewSetup(setupState(!tc.closed), nil, nil, nil, nil, nil, nil, tc.configured)
			_, err := s.Complete(t.Context(), tc.submitted, input)
			var fields org.ValidationErrors
			if tc.field != "" {
				if !errors.As(err, &fields) || len(fields) != 1 || fields[tc.field] == nil {
					t.Fatalf("want field %s, got %v", tc.field, err)
				}
			} else if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}

// setupTransaction returns failures through the runner just as a rolled-back
// store transaction does; state reads remain outside the transaction.
type setupTransaction struct {
	org.RegistrationWriter
	steps         []string
	fail          string
	err, checkErr error
	closed        bool
	checks        int
	t             *testing.T
}

func (s *setupTransaction) step(name string) error {
	s.steps = append(s.steps, name)
	if s.fail == name {
		return fmt.Errorf("fake write: %w", s.err)
	}
	return nil
}
func (s *setupTransaction) Open(context.Context) (bool, error) {
	s.checks++
	if s.checks == 1 {
		return true, nil
	}
	return !s.closed, s.checkErr
}
func (s *setupTransaction) InTx(_ context.Context, fn func(platform.Tx) error) error {
	return fn(platform.Tx{})
}
func (s *setupTransaction) CreateOrganization(_ context.Context, name, slug string) (kernel.ID, error) {
	if name != "Example" || slug != "example" {
		s.t.Fatal("bad normalized organization")
	}
	return kernel.ID{1}, s.step("organization")
}
func (s *setupTransaction) NextEventSeq(_ context.Context, organizationID kernel.ID) (int64, error) {
	if organizationID != (kernel.ID{1}) {
		s.t.Fatal("wrong organization")
	}
	return 3, s.step("sequence")
}
func (s *setupTransaction) CreateAccount(_ context.Context, email, name, hash string) (kernel.ID, error) {
	if email != "owner@example.org" || name != "Owner" || !strings.HasPrefix(hash, "$argon2id$") {
		s.t.Fatal("bad normalized account or hash")
	}
	return kernel.ID{2}, s.step("account")
}
func (s *setupTransaction) CreateMember(_ context.Context, organizationID, accountID kernel.ID, role org.Role, seq int64, handle string) (kernel.ID, error) {
	if organizationID != (kernel.ID{1}) || accountID != (kernel.ID{2}) || role != org.RoleOwner || seq != 3 || handle != "owner" {
		s.t.Fatal("bad owner member")
	}
	return kernel.ID{4}, s.step("member")
}
func (s *setupTransaction) Append(_ context.Context, organizationID kernel.ID, seq int64, kind realtime.EventKind, audience *kernel.ID, payload []byte) error {
	if organizationID != (kernel.ID{1}) || seq != 3 || kind != org.KindJoined || audience != nil || string(payload) != string(org.EncodeJoined(kernel.ID{4})) {
		s.t.Fatal("bad joined event")
	}
	return s.step("event")
}
func (s *setupTransaction) CreateDefaultChannel(_ context.Context, organizationID kernel.ID) error {
	if organizationID != (kernel.ID{1}) {
		s.t.Fatal("wrong default-channel organization")
	}
	return s.step("channel")
}
func (s *setupTransaction) CompleteSetup(_ context.Context, organizationID kernel.ID) error {
	if organizationID != (kernel.ID{1}) {
		s.t.Fatal("wrong setup organization")
	}
	return s.step("setup")
}

func TestSetupCompleteTransaction(t *testing.T) {
	hasher, err := identity.NewHasher()
	if err != nil {
		t.Fatal(err)
	}
	unexpected := errors.New("store unavailable")
	order := []string{"organization", "sequence", "account", "member", "event", "channel", "setup"}
	cases := []struct {
		name, fail, field string
		err, want         error
		conflict          bool
	}{
		{name: "success"},
		{name: "slug", fail: "organization", field: "slug", err: org.ErrSlugUnavailable, conflict: true},
		{name: "email taken", fail: "account", field: "email", err: identity.ErrEmailTaken, conflict: true},
		{name: "invalid email", fail: "account", field: "email", err: identity.ErrInvalidEmail, conflict: true},
		{name: "invalid handle", fail: "member", field: "handle", err: org.ErrInvalidHandle, conflict: true},
		{name: "handle taken", fail: "member", err: org.ErrHandleTaken, want: org.ErrHandleTaken, conflict: true},
		{name: "setup conflict", fail: "setup", err: org.ErrSetupCompleted, want: org.ErrSetupCompleted, conflict: true},
	}
	for _, step := range order {
		tc := cases[0]
		tc.name, tc.fail, tc.err, tc.want = step+" failure", step, unexpected, unexpected
		cases = append(cases, tc)
	}
	for _, tc := range cases {
		for _, recheck := range []string{"open", "closed", "error"} {
			if !tc.conflict && recheck != "open" {
				continue
			}
			t.Run(tc.name+"/"+recheck, func(t *testing.T) {
				store := &setupTransaction{fail: tc.fail, err: tc.err, closed: recheck != "open", t: t}
				if recheck == "error" {
					store.checkErr = unexpected
				}
				service := org.NewSetup(store, store,
					func(platform.Tx) org.RegistrationWriter { return store },
					func(platform.Tx) org.AccountCreator { return store },
					func(platform.Tx) org.EventAppender { return store },
					func(platform.Tx) org.DefaultChannelCreator { return store }, hasher, "secret")
				result, err := service.Complete(t.Context(), "secret", org.SetupInput{
					OrganizationName: " Example ", Slug: "example", DisplayName: " Owner ", Handle: " Owner ", Email: " Owner@Example.org ", Password: "long enough password",
				})
				wantOrder := order
				for i, step := range order {
					if step == tc.fail {
						wantOrder = order[:i+1]
					}
				}
				checks := 1
				if tc.conflict {
					checks++
				}
				if strings.Join(store.steps, ",") != strings.Join(wantOrder, ",") || store.checks != checks {
					t.Fatalf("steps %v, state reads %d; want %v, %d", store.steps, store.checks, wantOrder, checks)
				}
				want, field := tc.want, tc.field
				if tc.conflict && recheck == "closed" {
					want, field = org.ErrSetupCompleted, ""
				}
				var fields org.ValidationErrors
				if field != "" {
					if !errors.As(err, &fields) || len(fields) != 1 || !errors.Is(fields[field], tc.err) {
						t.Fatalf("want field %s: %v; got %v", field, tc.err, err)
					}
				} else if !errors.Is(err, want) {
					t.Fatalf("want %v, got %v", want, err)
				}
				if errors.Is(err, unexpected) || errors.Is(err, org.ErrHandleTaken) {
					if err.Error() != "creating setup: fake write: "+tc.err.Error() {
						t.Fatalf("unexpected wrapping: %v", err)
					}
				}
				if err == nil && result != (org.SetupResult{OrganizationID: kernel.ID{1}, AccountID: kernel.ID{2}}) || err != nil && result != (org.SetupResult{}) {
					t.Fatalf("bad result: %+v (%v)", result, err)
				}
			})
		}
	}
}
