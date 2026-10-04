package org_test

import (
	"context"
	"errors"
	"testing"

	"github.com/tkakkie/ribbitto/internal/org"
)

type setupStore struct {
	org.SetupStore
	open bool
}

func (s setupStore) Open(context.Context) (bool, error) { return s.open, nil }

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
			// Nil hasher and embedded store panic if rejection reaches hashing or writes.
			s := org.NewSetup(setupStore{open: !tc.closed}, nil, tc.configured)
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
