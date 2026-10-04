package topic_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/app/topic"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/org"
)

type fakeBranchStore struct {
	called bool
	got    topic.Branch
	err    error
}

func (f *fakeBranchStore) Branch(_ context.Context, _, _, _ domain.ID, b topic.Branch, notice func(domain.Topic) string) (domain.Topic, int64, error) {
	f.called, f.got = true, b
	if f.err != nil {
		return domain.Topic{}, 0, f.err
	}
	return domain.Topic{ID: domain.ID{9}, Name: notice(domain.Topic{Name: "dest"})}, 42, nil
}

type raised struct {
	org domain.ID
	seq int64
}

func (r *raised) Raise(org domain.ID, seq int64) { r.org, r.seq = org, seq }

func TestBrancherValidates(t *testing.T) {
	from, to := domain.ID{1}, domain.ID{2}
	many := make([]domain.ID, topic.MaxBranchMessages+1)
	for i := range many {
		many[i] = domain.ID{byte(i), 0xff}
	}
	for _, tt := range []struct {
		name string
		b    topic.Branch
		want error
	}{
		{"no messages", topic.Branch{From: from, To: &to}, topic.ErrInvalidBranch},
		{"too many", topic.Branch{Messages: many, From: from, To: &to}, topic.ErrInvalidBranch},
		{"a message twice", topic.Branch{Messages: []domain.ID{{5}, {5}}, From: from, To: &to}, topic.ErrInvalidBranch},
		{"two destinations", topic.Branch{Messages: []domain.ID{{5}}, From: from, To: &to, NewName: "x"}, topic.ErrInvalidBranch},
		{"source as destination", topic.Branch{Messages: []domain.ID{{5}}, From: from, To: &from}, topic.ErrInvalidBranch},
		{"blank new name", topic.Branch{Messages: []domain.ID{{5}}, From: from, NewName: " "}, topic.ErrInvalidName},
		{"long new name", topic.Branch{Messages: []domain.ID{{5}}, From: from, NewName: strings.Repeat("x", 81)}, topic.ErrInvalidName},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeBranchStore{}
			_, err := topic.NewBrancher(store, nil).Branch(t.Context(), org.Membership{}, domain.ID{3}, tt.b, func(domain.Topic) string { return "n" })
			if !errors.Is(err, tt.want) || store.called {
				t.Fatalf("Branch = %v (store called %t), want %v before the store", err, store.called, tt.want)
			}
		})
	}
}

func TestBrancherRunsAndRaises(t *testing.T) {
	orgID := domain.ID{7}
	store, hub := &fakeBranchStore{}, &raised{}
	m := org.Membership{Organization: org.Organization{ID: orgID}}
	dest, err := topic.NewBrancher(store, hub).Branch(t.Context(), m, domain.ID{3}, topic.Branch{Messages: []domain.ID{{5}}, From: domain.ID{1}, NewName: "  設計 "}, func(d domain.Topic) string { return "to " + d.Name })
	if err != nil || dest.ID != (domain.ID{9}) || dest.Name != "to dest" || store.got.NewName != "設計" {
		t.Fatalf("Branch = %+v, %v; store got %+v", dest, err, store.got)
	}
	if hub.org != orgID || hub.seq != 42 {
		t.Fatalf("raised %+v, want the notice's sequence 42", hub)
	}
	store.err, hub.seq = topic.ErrConflict, 0
	if _, err := topic.NewBrancher(store, hub).Branch(t.Context(), m, domain.ID{3}, topic.Branch{Messages: []domain.ID{{5}}, From: domain.ID{1}, NewName: "x"}, func(domain.Topic) string { return "n" }); !errors.Is(err, topic.ErrConflict) || hub.seq != 0 {
		t.Fatalf("conflict: %v, raised %+v; want the error and no raise", err, hub)
	}
}
