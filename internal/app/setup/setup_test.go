package setup_test

import (
	"context"
	"errors"
	"testing"

	"github.com/tkakkie/ribbitto/internal/app/setup"
)

type setupStore struct {
	setup.Store
	open bool
}

func (s setupStore) Open(context.Context) (bool, error) { return s.open, nil }

func TestRejectedSetup(t *testing.T) {
	for _, tc := range []struct {
		name, configured, submitted, field string
		closed                             bool
		want                               error
	}{
		{name: "wrong token", configured: "secret", submitted: "wrong", want: setup.ErrToken},
		{name: "empty configured", want: setup.ErrToken},
		{name: "completed", configured: "secret", submitted: "secret", closed: true, want: setup.ErrCompleted},
		{name: "email", field: "email"},
		{name: "display name", field: "display_name"},
		{name: "password", field: "password"},
		{name: "organization name", field: "organization_name"},
		{name: "slug", field: "slug"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := setup.Input{OrganizationName: "Example", Slug: "example", Email: "a@b", DisplayName: "Owner", Password: "long enough password"}
			if tc.field != "" {
				tc.configured, tc.submitted = "secret", "secret"
				fields := map[string]*string{"email": &input.Email, "display_name": &input.DisplayName, "password": &input.Password, "organization_name": &input.OrganizationName, "slug": &input.Slug}
				*fields[tc.field] = ""
			}
			// Nil hasher and embedded store panic if rejection reaches hashing or writes.
			s := setup.New(setupStore{open: !tc.closed}, nil, tc.configured)
			_, err := s.Complete(t.Context(), tc.submitted, input)
			var fields setup.ValidationErrors
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
