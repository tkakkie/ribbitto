package message_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/tkakkie/ribbitto/internal/app/authz"
	"github.com/tkakkie/ribbitto/internal/app/member"
	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/domain"
)

type directoryHistory struct {
	t     *testing.T
	fail  string
	calls []string
}

func (f *directoryHistory) step(s string) error {
	f.calls = append(f.calls, s)
	if f.fail == s {
		return errors.New(s)
	}
	return nil
}
func (f *directoryHistory) ListMessagesBefore(_ context.Context, org, ch domain.ID, before *int64, limit int32) ([]domain.Message, error) {
	if org != (domain.ID{1}) || ch != (domain.ID{2}) || before != nil || limit != 50 {
		f.t.Fatal("wrong history scope or limit")
	}
	return []domain.Message{{ID: domain.ID{4}, MemberID: domain.ID{3}}, {ID: domain.ID{5}, MemberID: domain.ID{3}}}, f.step("history")
}
func (f *directoryHistory) LookupMembers(_ context.Context, org domain.ID, ids []domain.ID) (map[domain.ID]member.Identity, error) {
	if org != (domain.ID{1}) || !reflect.DeepEqual(ids, []domain.ID{{3}, {3}}) {
		f.t.Fatal("wrong author scope")
	}
	if f.fail == "missing member" {
		return nil, nil
	}
	return map[domain.ID]member.Identity{{3}: {AccountID: domain.ID{6}, Handle: "author"}}, f.step("members")
}
func (f *directoryHistory) LookupDisplayNames(_ context.Context, ids []domain.ID) (map[domain.ID]string, error) {
	if f.fail == "missing member" || f.fail == "missing account" {
		return nil, nil
	}
	if !reflect.DeepEqual(ids, []domain.ID{{6}}) {
		f.t.Fatal("accounts did not come from org lookup")
	}
	return map[domain.ID]string{{6}: "Author"}, f.step("names")
}
func TestLatest(t *testing.T) {
	for _, failure := range []string{"", "history", "members", "names", "missing member", "missing account"} {
		t.Run("failure="+failure, func(t *testing.T) {
			f := &directoryHistory{t: t, fail: failure}
			got, err := (message.Reader{History: f, Members: f, Accounts: f}).Latest(t.Context(), authz.Membership{Organization: domain.Organization{ID: domain.ID{1}}}, domain.ID{2})
			if (err != nil) != (failure != "") {
				t.Fatalf("error: %v", err)
			}
			if failure == "" && (len(got) != 2 || got[0].ID != (domain.ID{5}) || got[1].ID != (domain.ID{4}) || got[0].DisplayName != "Author" || got[0].Handle != "author" || !reflect.DeepEqual(f.calls, []string{"history", "members", "names"})) {
				t.Fatalf("entries: %+v, calls: %v", got, f.calls)
			}
		})
	}
}
