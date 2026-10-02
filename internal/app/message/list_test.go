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
func (f *directoryHistory) GetMessage(context.Context, domain.ID, domain.ID, int64) (domain.Message, error) {
	f.t.Fatal("unexpected single-message read")
	return domain.Message{}, nil
}
func (f *directoryHistory) ListMessagesBefore(_ context.Context, org, ch domain.ID, before *int64, limit int32) ([]domain.Message, error) {
	if org != (domain.ID{1}) || ch != (domain.ID{2}) || before == nil || *before != 9 || limit != message.PageSize+1 {
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
func (f *directoryHistory) LookupTopics(_ context.Context, org, ch domain.ID, ids []domain.ID) (map[domain.ID]domain.Topic, error) {
	if org != (domain.ID{1}) || ch != (domain.ID{2}) || !reflect.DeepEqual(ids, []domain.ID{{}, {}}) {
		f.t.Fatal("wrong topic scope")
	}
	if f.fail == "missing topic" {
		return nil, nil
	}
	return map[domain.ID]domain.Topic{{}: {Name: "Design"}}, f.step("topics")
}
func TestBefore(t *testing.T) {
	for _, failure := range []string{"", "history", "members", "names", "topics", "missing topic", "missing member", "missing account"} {
		t.Run("failure="+failure, func(t *testing.T) {
			f := &directoryHistory{t: t, fail: failure}
			before := int64(9)
			got, err := (message.Reader{History: f, Members: f, Accounts: f, Topics: f}).Before(t.Context(), authz.Membership{Organization: domain.Organization{ID: domain.ID{1}}}, domain.ID{2}, &before)
			if (err != nil) != (failure != "") {
				t.Fatalf("error: %v", err)
			}
			if failure == "" && (len(got.Entries) != 2 || got.Older || got.Entries[0].ID != (domain.ID{5}) || got.Entries[1].ID != (domain.ID{4}) || got.Entries[0].DisplayName != "Author" || got.Entries[0].Handle != "author" || got.Entries[0].TopicName != "Design" || !reflect.DeepEqual(f.calls, []string{"history", "members", "names", "topics"})) {
				t.Fatalf("page: %+v, calls: %v", got, f.calls)
			}
		})
	}
}

// fullHistory returns n newest-first messages by one author, as many as asked.
type fullHistory struct{ n int }

func (fullHistory) GetMessage(context.Context, domain.ID, domain.ID, int64) (domain.Message, error) {
	return domain.Message{}, errors.New("unexpected single-message read")
}

func (f fullHistory) ListMessagesBefore(_ context.Context, _, _ domain.ID, before *int64, limit int32) ([]domain.Message, error) {
	var out []domain.Message
	for seq := int64(f.n); seq >= 1 && len(out) < int(limit); seq-- {
		if before == nil || seq < *before {
			out = append(out, domain.Message{ID: domain.ID{byte(seq)}, EventSeq: seq, MemberID: domain.ID{3}})
		}
	}
	return out, nil
}
func (fullHistory) LookupMembers(context.Context, domain.ID, []domain.ID) (map[domain.ID]member.Identity, error) {
	return map[domain.ID]member.Identity{{3}: {AccountID: domain.ID{6}}}, nil
}
func (fullHistory) LookupDisplayNames(context.Context, []domain.ID) (map[domain.ID]string, error) {
	return map[domain.ID]string{{6}: "Author"}, nil
}

func (fullHistory) LookupTopics(context.Context, domain.ID, domain.ID, []domain.ID) (map[domain.ID]domain.Topic, error) {
	return map[domain.ID]domain.Topic{{}: {}}, nil
}

func TestBeforePages(t *testing.T) {
	for _, tt := range []struct {
		messages int
		pages    []int
	}{
		{0, []int{0}},
		{1, []int{1}},
		{message.PageSize, []int{message.PageSize}},
		{message.PageSize + 1, []int{message.PageSize, 1}},
		{2 * message.PageSize, []int{message.PageSize, message.PageSize}},
	} {
		f := fullHistory{tt.messages}
		reader := message.Reader{History: f, Members: f, Accounts: f, Topics: f}
		var before *int64
		var sizes []int
		for {
			page, err := reader.Before(t.Context(), authz.Membership{}, domain.ID{2}, before)
			if err != nil {
				t.Fatal(err)
			}
			sizes = append(sizes, len(page.Entries))
			for i := 1; i < len(page.Entries); i++ {
				if page.Entries[i-1].EventSeq >= page.Entries[i].EventSeq {
					t.Fatalf("%d messages: page not oldest first", tt.messages)
				}
			}
			if !page.Older || len(sizes) > 5 {
				break
			}
			before = &page.Entries[0].EventSeq
		}
		if !reflect.DeepEqual(sizes, tt.pages) {
			t.Errorf("%d messages: pages %v, want %v", tt.messages, sizes, tt.pages)
		}
	}
}
