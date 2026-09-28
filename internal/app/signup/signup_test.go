package signup_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/app/auth"
	"github.com/tkakkie/ribbitto/internal/app/signup"
	"github.com/tkakkie/ribbitto/internal/domain"
)

type store struct {
	pending bool
	err     error
	t       *testing.T
}

func (s store) Open(context.Context) (bool, error) { return s.pending, nil }
func (s store) SignUp(_ context.Context, name, handle, email, hash string) (domain.ID, error) {
	s.t.Helper()
	if name != "Alice" || handle != "alice" || email != "alice@example.org" || !strings.HasPrefix(hash, "$argon2id$") {
		s.t.Fatalf("bad normalized account or hash: %s %s %s", name, handle, email)
	}
	return domain.ID{1}, s.err
}
func TestSignUp(t *testing.T) {
	hasher, err := auth.NewHasher()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		label, name, handle, email, password, field string
		off, pending                                bool
		want                                        error
	}{
		{label: "normalized account", name: " Alice ", handle: " Alice ", email: " Alice@Example.org ", password: "long enough password"},
		{label: "duplicate email", name: "Alice", handle: "alice", email: "alice@example.org", password: "long enough password", want: signup.ErrEmailTaken},
		{label: "duplicate handle", name: "Alice", handle: "alice", email: "alice@example.org", password: "long enough password", want: signup.ErrHandleTaken},
		{label: "invalid name", field: "display_name", handle: "alice", email: "a@b", password: "long enough password"},
		{label: "invalid handle", name: "Alice", field: "handle", email: "a@b", password: "long enough password"},
		{label: "reserved handle", name: "Alice", handle: "everyone", field: "handle", email: "a@b", password: "long enough password"},
		{label: "invalid email", name: "Alice", handle: "alice", field: "email", password: "long enough password"},
		{label: "invalid password", name: "Alice", handle: "alice", email: "a@b", field: "password"},
		{label: "disabled", off: true, want: signup.ErrClosed},
		{label: "setup pending", pending: true, want: signup.ErrClosed},
	} {
		t.Run(tc.label, func(t *testing.T) {
			h := hasher
			// Rejections must not reach hashing.
			if tc.field != "" || tc.off || tc.pending {
				h = nil
			}
			service := signup.New(store{pending: tc.pending, err: tc.want, t: t}, h, !tc.off)
			open, err := service.Open(t.Context())
			if err != nil || open != (!tc.off && !tc.pending) {
				t.Fatalf("Open: %t %v", open, err)
			}
			id, err := service.SignUp(t.Context(), tc.name, tc.handle, tc.email, tc.password)
			var fields signup.ValidationErrors
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
