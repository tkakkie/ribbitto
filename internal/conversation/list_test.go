package conversation_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
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
func (f *directoryHistory) GetMessage(context.Context, kernel.ID, kernel.ID, int64) (conversation.Message, error) {
	f.t.Fatal("unexpected single-message read")
	return conversation.Message{}, nil
}
func (f *directoryHistory) ListMessagesBefore(_ context.Context, org, ch kernel.ID, topicID *kernel.ID, before *int64, limit int32) ([]conversation.Message, error) {
	if org != (kernel.ID{1}) || ch != (kernel.ID{2}) || topicID == nil || *topicID != (kernel.ID{7}) || before == nil || *before != 9 || limit != conversation.PageSize+1 {
		f.t.Fatal("wrong history scope or limit")
	}
	return []conversation.Message{{ID: kernel.ID{4}, MemberID: kernel.ID{3}}, {ID: kernel.ID{5}, MemberID: kernel.ID{3}}}, f.step("history")
}
func (f *directoryHistory) LookupMembers(_ context.Context, orgID kernel.ID, ids []kernel.ID) (map[kernel.ID]org.DirectoryEntry, error) {
	if orgID != (kernel.ID{1}) || !reflect.DeepEqual(ids, []kernel.ID{{3}, {3}}) {
		f.t.Fatal("wrong author scope")
	}
	if f.fail == "missing member" {
		return nil, nil
	}
	return map[kernel.ID]org.DirectoryEntry{{3}: {AccountID: kernel.ID{6}, Handle: "author"}}, f.step("members")
}
func (f *directoryHistory) LookupDisplayNames(_ context.Context, ids []kernel.ID) (map[kernel.ID]string, error) {
	if f.fail == "missing member" || f.fail == "missing account" {
		return nil, nil
	}
	if !reflect.DeepEqual(ids, []kernel.ID{{6}}) {
		f.t.Fatal("accounts did not come from org lookup")
	}
	return map[kernel.ID]string{{6}: "Author"}, f.step("names")
}
func (f *directoryHistory) LookupTopics(_ context.Context, org, ch kernel.ID, ids []kernel.ID) (map[kernel.ID]conversation.Topic, error) {
	if org != (kernel.ID{1}) || ch != (kernel.ID{2}) || !reflect.DeepEqual(ids, []kernel.ID{{}, {}}) {
		f.t.Fatal("wrong topic scope")
	}
	if f.fail == "missing topic" {
		return nil, nil
	}
	return map[kernel.ID]conversation.Topic{{}: {Name: "Design"}}, f.step("topics")
}
func TestBefore(t *testing.T) {
	for _, failure := range []string{"", "history", "members", "names", "topics", "missing topic", "missing member", "missing account"} {
		t.Run("failure="+failure, func(t *testing.T) {
			f := &directoryHistory{t: t, fail: failure}
			before := int64(9)
			got, err := (conversation.Reader{History: f, Members: f, Accounts: f, Topics: f}).Before(t.Context(), org.Membership{Organization: org.Organization{ID: kernel.ID{1}}}, kernel.ID{2}, &kernel.ID{7}, &before)
			if (err != nil) != (failure != "") {
				t.Fatalf("error: %v", err)
			}
			if failure == "" && (len(got.Entries) != 2 || got.Older || got.Entries[0].ID != (kernel.ID{5}) || got.Entries[1].ID != (kernel.ID{4}) || got.Entries[0].DisplayName != "Author" || got.Entries[0].Handle != "author" || got.Entries[0].TopicName != "Design" || !reflect.DeepEqual(f.calls, []string{"history", "members", "names", "topics"})) {
				t.Fatalf("page: %+v, calls: %v", got, f.calls)
			}
		})
	}
}

// fullHistory returns n newest-first messages by one author, as many as asked.
type fullHistory struct{ n int }

func (fullHistory) GetMessage(context.Context, kernel.ID, kernel.ID, int64) (conversation.Message, error) {
	return conversation.Message{}, errors.New("unexpected single-message read")
}

func (f fullHistory) ListMessagesBefore(_ context.Context, _, _ kernel.ID, _ *kernel.ID, before *int64, limit int32) ([]conversation.Message, error) {
	var out []conversation.Message
	for seq := int64(f.n); seq >= 1 && len(out) < int(limit); seq-- {
		if before == nil || seq < *before {
			out = append(out, conversation.Message{ID: kernel.ID{byte(seq)}, EventSeq: seq, MemberID: kernel.ID{3}})
		}
	}
	return out, nil
}
func (fullHistory) LookupMembers(context.Context, kernel.ID, []kernel.ID) (map[kernel.ID]org.DirectoryEntry, error) {
	return map[kernel.ID]org.DirectoryEntry{{3}: {AccountID: kernel.ID{6}}}, nil
}
func (fullHistory) LookupDisplayNames(context.Context, []kernel.ID) (map[kernel.ID]string, error) {
	return map[kernel.ID]string{{6}: "Author"}, nil
}

func (fullHistory) LookupTopics(context.Context, kernel.ID, kernel.ID, []kernel.ID) (map[kernel.ID]conversation.Topic, error) {
	return map[kernel.ID]conversation.Topic{{}: {}}, nil
}

func TestBeforePages(t *testing.T) {
	for _, tt := range []struct {
		messages int
		pages    []int
	}{
		{0, []int{0}},
		{1, []int{1}},
		{conversation.PageSize, []int{conversation.PageSize}},
		{conversation.PageSize + 1, []int{conversation.PageSize, 1}},
		{2 * conversation.PageSize, []int{conversation.PageSize, conversation.PageSize}},
	} {
		f := fullHistory{tt.messages}
		reader := conversation.Reader{History: f, Members: f, Accounts: f, Topics: f}
		var before *int64
		var sizes []int
		for {
			page, err := reader.Before(t.Context(), org.Membership{}, kernel.ID{2}, nil, before)
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

func (directoryHistory) GetMessages(context.Context, kernel.ID, kernel.ID, []kernel.ID) ([]conversation.Message, error) {
	return nil, errors.New("unexpected batch read")
}

func (fullHistory) GetMessages(context.Context, kernel.ID, kernel.ID, []kernel.ID) ([]conversation.Message, error) {
	return nil, errors.New("unexpected batch read")
}
