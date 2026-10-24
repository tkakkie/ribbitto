package org_test

import (
	"context"
	"errors"
	"testing"

	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

type registrationErrState struct {
	open bool
	err  error
}

func (s registrationErrState) Open(context.Context) (bool, error) {
	return s.open, s.err
}

// registrationErrStore embeds a nil RegistrationWriter so any write after the
// failing organisation lock panics and fails the test.
type registrationErrStore struct {
	org.RegistrationWriter
	organizationID       kernel.ID
	lockedOrganizationID kernel.ID
	err                  error
}

func (s *registrationErrStore) InTx(_ context.Context, fn func(platform.Tx) error) error {
	return fn(platform.Tx{})
}

func (s *registrationErrStore) SetupOrganization(context.Context) (kernel.ID, error) {
	return s.organizationID, nil
}

func (s *registrationErrStore) NextEventSeq(_ context.Context, organizationID kernel.ID) (int64, error) {
	s.lockedOrganizationID = organizationID
	return 0, s.err
}

func TestRegistrationServerFailures(t *testing.T) {
	hasher, err := identity.NewHasher()
	if err != nil {
		t.Fatal(err)
	}
	setupRowErr := errors.New("setup row unavailable")
	seqErr := errors.New("sequence unavailable")
	setupOrgID := kernel.ID{2} // organisation returned by SetupOrganization

	for _, tc := range []struct {
		name       string
		op         string
		state      registrationErrState
		cancel     bool
		seqErr     error
		wantErr    error
		wantText   string
		wantLocked kernel.ID
	}{
		{
			name:     "setup open row failure",
			op:       "setup-open",
			state:    registrationErrState{open: true, err: setupRowErr},
			wantErr:  setupRowErr,
			wantText: "checking setup: setup row unavailable",
		},
		{
			name:     "setup complete row failure",
			op:       "setup-complete",
			state:    registrationErrState{open: true, err: setupRowErr},
			wantErr:  setupRowErr,
			wantText: "checking setup: setup row unavailable",
		},
		{
			name:     "setup complete hasher failure",
			op:       "setup-complete",
			state:    registrationErrState{open: true},
			cancel:   true,
			wantErr:  identity.ErrBusy,
			wantText: "hashing setup password: password hasher busy: context canceled",
		},
		{
			name:     "sign-up hasher failure",
			op:       "signup",
			state:    registrationErrState{open: false},
			cancel:   true,
			wantErr:  identity.ErrBusy,
			wantText: "hashing sign-up password: password hasher busy: context canceled",
		},
		{
			name:       "sign-up sequence lock failure",
			op:         "signup",
			state:      registrationErrState{open: false},
			seqErr:     seqErr,
			wantErr:    seqErr,
			wantText:   "storing sign-up transaction: sequence unavailable",
			wantLocked: setupOrgID,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			if tc.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			input := org.SetupInput{
				OrganizationName: "Example",
				Slug:             "example",
				Email:            "owner@example.org",
				DisplayName:      "Owner",
				Handle:           "owner",
				Password:         "long enough password",
			}
			var (
				open   bool
				result org.SetupResult
				id     kernel.ID
				err    error
				store  *registrationErrStore
			)
			switch tc.op {
			case "setup-open":
				setup := org.NewSetup(tc.state, nil, nil, nil, nil, nil, nil, "secret")
				open, err = setup.Open(ctx)
			case "setup-complete":
				var h *identity.Hasher
				if tc.cancel {
					h = hasher
				}
				// Nil runner and writers panic if Complete reaches a write.
				setup := org.NewSetup(tc.state, nil, nil, nil, nil, nil, h, "secret")
				result, err = setup.Complete(ctx, "secret", input)
			case "signup":
				var runner org.TxRunner
				var writes org.RegistrationWriterIn
				if tc.seqErr != nil {
					store = &registrationErrStore{organizationID: setupOrgID, err: tc.seqErr}
					runner = store
					writes = func(platform.Tx) org.RegistrationWriter { return store }
				}
				// Nil account and event factories panic if SignUp writes after a failed lock.
				signUp := org.NewSignUp(tc.state, runner, writes, nil, nil, hasher, true)
				id, err = signUp.SignUp(ctx, input.DisplayName, input.Handle, input.Email, input.Password)
			}
			if err == nil || err.Error() != tc.wantText {
				t.Fatalf("error = %v, want exact %q", err, tc.wantText)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error %v does not wrap %v", err, tc.wantErr)
			}
			var fields org.ValidationErrors
			if errors.As(err, &fields) {
				t.Fatalf("server failure matched ValidationErrors: %v", fields)
			}
			if open || result != (org.SetupResult{}) || id != (kernel.ID{}) {
				t.Fatalf("got open=%t result=%+v id=%v, want all zero", open, result, id)
			}
			var gotLocked kernel.ID
			if store != nil {
				gotLocked = store.lockedOrganizationID
			}
			if gotLocked != tc.wantLocked {
				t.Fatalf("locked organization = %v, want %v", gotLocked, tc.wantLocked)
			}
		})
	}
}

func TestValidationErrorsError(t *testing.T) {
	err := org.ValidationErrors{
		"email":  errors.New("owner@example.org is taken"),
		"handle": org.ErrInvalidHandle,
	}
	if got := err.Error(); got != "invalid registration fields" {
		t.Fatalf("Error() = %q, want %q", got, "invalid registration fields")
	}
}
