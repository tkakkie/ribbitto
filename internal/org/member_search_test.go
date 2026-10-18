package org_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/identity"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

type searchFixture struct {
	members []org.ListedMember
	names   map[kernel.ID]string
	orgID   kernel.ID
	ids     []kernel.ID
	fail    string
}

func (f *searchFixture) InSnapshot(ctx context.Context, fn func(platform.Snapshot) error) error {
	return fn(platform.Snapshot{})
}

func (f *searchFixture) ListSearchMembers(_ context.Context, id kernel.ID) ([]org.ListedMember, error) {
	f.orgID = id
	if f.fail == "members" {
		return nil, org.ErrNotFound
	}
	return f.members, nil
}

func (f *searchFixture) LookupDisplayNames(_ context.Context, ids []kernel.ID) (map[kernel.ID]string, error) {
	f.ids = ids
	if f.fail == "names" {
		return nil, org.ErrNotFound
	}
	return f.names, nil
}

func TestMemberSearch(t *testing.T) {
	for _, tt := range []struct {
		name, query string
		people      [][2]string // handle, display name; the store orders by handle.
		want        []string
	}{
		{"surname", "加藤", [][2]string{{"tomoya", "加藤 智也"}}, []string{"tomoya"}},
		{"given name", "智也", [][2]string{{"tomoya", "加藤 智也"}}, []string{"tomoya"}},
		{"trim and at", " \t@ToMo　", [][2]string{{"tomoya", "加藤 智也"}}, []string{"tomoya"}},
		{"one at only", "@@tomo", [][2]string{{"tomoya", "加藤 智也"}}, nil},
		{"width query", "ｔｏｍｏ", [][2]string{{"tomoya", "加藤 智也"}}, []string{"tomoya"}},
		{"width name and Unicode lower case", "älice", [][2]string{{"alice", "ÄＬＩＣＥ"}}, []string{"alice"}},
		{"NFC query", "e\u0301", [][2]string{{"alice", "Élodie"}}, []string{"alice"}},
		{"NFC name", "É", [][2]string{{"alice", "E\u0301lodie"}}, []string{"alice"}},
		{"rank and handle ties", "tomo", [][2]string{{"atomo", "Other"}, {"bstart", "Tomoko"}, {"cword", "加藤　Tomoya"}, {"dtomo", "Other"}, {"tomo", "Other"}}, []string{"bstart", "cword", "tomo", "atomo", "dtomo"}},
		{"display substring", "也", [][2]string{{"tomoya", "加藤 智也"}}, []string{"tomoya"}},
		{"no match", "かとう", [][2]string{{"tomoya", "加藤 智也"}}, nil},
		{"empty organisation", "@", nil, nil},
		{"all members only", "all", [][2]string{{"alice", "Alice"}, {"sally", "Sally"}}, []string{"sally"}},
		{"everyone member only", "everyone", [][2]string{{"alice", "Everyone's friend"}, {"bob", "Bob"}}, []string{"alice"}},
		{"here member only", "here", [][2]string{{"alice", "Here I am"}}, []string{"alice"}},
		{"channel member only", "channel", [][2]string{{"alice", "Channel keeper"}}, []string{"alice"}},
		{"reserved without match", "everyone", [][2]string{{"alice", "Alice"}}, nil},
		{"blank-looking name", "\u3164", [][2]string{{"alice", " \u3164\uFFA0\u115F\u1160\u2800\u0301"}}, nil},
		{"blank name handle match", "ali", [][2]string{{"alice", "\u2800"}}, []string{"alice"}},
		{"50 code points", strings.Repeat("界", 50), [][2]string{{"alice", strings.Repeat("界", 50)}}, []string{"alice"}},
		{"51 code points", strings.Repeat("界", 51), [][2]string{{"alice", strings.Repeat("界", 51)}}, nil},
		{"length after trim at NFC", " @" + strings.Repeat("e\u0301", 50) + " ", [][2]string{{"alice", strings.Repeat("é", 50)}}, []string{"alice"}},
		{"length before NFKC expansion", strings.Repeat("㍍", 50), [][2]string{{"alice", strings.Repeat("メートル", 50)}}, []string{"alice"}},
		{"empty limit", "@", [][2]string{{"aa", "A"}, {"ab", "A"}, {"ac", "A"}, {"ad", "A"}, {"ae", "A"}, {"af", "A"}, {"ag", "A"}, {"ah", "A"}, {"ai", "A"}}, []string{"aa", "ab", "ac", "ad", "ae", "af", "ag", "ah"}},
		{"matching limit and late prefix", "a", [][2]string{{"ba", "B"}, {"ca", "B"}, {"da", "B"}, {"ea", "B"}, {"fa", "B"}, {"ga", "B"}, {"ha", "B"}, {"ia", "B"}, {"ja", "B"}, {"zz", "A"}}, []string{"zz", "ba", "ca", "da", "ea", "fa", "ga", "ha"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &searchFixture{names: map[kernel.ID]string{}}
			var ids []kernel.ID
			for i, person := range tt.people {
				id, account := kernel.ID{byte(i + 1)}, kernel.ID{byte(i + 101)}
				f.members = append(f.members, org.ListedMember{ID: id, DirectoryEntry: org.DirectoryEntry{AccountID: account, Handle: person[0]}})
				f.names[account] = person[1]
				ids = append(ids, account)
			}
			search := org.NewMemberSearch(f, func(platform.Snapshot) org.MemberSearchStore { return f }, func(platform.Snapshot) identity.Directory { return f })
			got, err := search.Search(t.Context(), org.Membership{Organization: org.Organization{ID: kernel.ID{99}}}, tt.query)
			if err != nil {
				t.Fatal(err)
			}
			var handles []string
			for _, candidate := range got {
				handles = append(handles, candidate.Handle)
				member := f.members[slices.IndexFunc(f.members, func(m org.ListedMember) bool { return m.Handle == candidate.Handle })]
				name := f.names[member.AccountID]
				if identity.IsBlankLookingName(name) {
					name = ""
				}
				if candidate.ID != member.ID || candidate.DisplayName != name {
					t.Fatalf("candidate = %#v, want member %v and name %q", candidate, member.ID, name)
				}
			}
			if !reflect.DeepEqual(handles, tt.want) {
				t.Fatalf("handles = %v, want %v", handles, tt.want)
			}
			if tt.name == "51 code points" {
				if f.orgID != (kernel.ID{}) {
					t.Fatal("overlong query reached store")
				}
			} else if f.orgID != (kernel.ID{99}) || !slices.Equal(f.ids, ids) {
				t.Fatalf("reads for org %v accounts %v; want resolved org and listed accounts only", f.orgID, f.ids)
			}
		})
	}
}

func TestMemberSearchErrors(t *testing.T) {
	for _, fail := range []string{"members", "names"} {
		t.Run(fail, func(t *testing.T) {
			f := &searchFixture{fail: fail}
			search := org.NewMemberSearch(f, func(platform.Snapshot) org.MemberSearchStore { return f }, func(platform.Snapshot) identity.Directory { return f })
			got, err := search.Search(t.Context(), org.Membership{}, "alice")
			if got != nil || !errors.Is(err, org.ErrNotFound) {
				t.Fatalf("Search = %v, %v; want nil and wrapped error", got, err)
			}
		})
	}
}
