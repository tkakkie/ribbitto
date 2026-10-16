package conversation_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// referenceDirectory holds members of two organisations and records each
// batch posting looks up, which must run inside its own snapshot.
type referenceDirectory struct {
	t       *testing.T
	members map[kernel.ID][]kernel.ID
	err     error
	open    bool
	lookups [][]kernel.ID
}

func (d *referenceDirectory) InSnapshot(_ context.Context, fn func(platform.Snapshot) error) error {
	d.open = true
	defer func() { d.open = false }()
	return fn(platform.Snapshot{})
}
func (d *referenceDirectory) LookupMembers(_ context.Context, organizationID kernel.ID, ids []kernel.ID) (map[kernel.ID]org.DirectoryEntry, error) {
	if !d.open {
		d.t.Fatal("lookup outside the snapshot")
	}
	d.lookups = append(d.lookups, ids)
	found := map[kernel.ID]org.DirectoryEntry{}
	for _, id := range d.members[organizationID] {
		found[id] = org.DirectoryEntry{Handle: "member"}
	}
	return found, d.err
}
func (d *referenceDirectory) ListMembers(context.Context, kernel.ID, *kernel.ID, int32) ([]org.ListedMember, error) {
	panic("unexpected ListMembers")
}

func TestPostingReferences(t *testing.T) {
	m := org.Membership{Organization: org.Organization{ID: kernel.ID{1}}, Member: org.Member{ID: kernel.ID{2}, OrganizationID: kernel.ID{1}}}
	ref := func(id kernel.ID) string { return "<@" + realtime.FormatPayloadID(id) + ">" }
	member, other, foreign, unknown := kernel.ID{0xa1}, kernel.ID{0xa2}, kernel.ID{0xb1}, kernel.ID{0xee}
	failure := errors.New("directory failed")
	for _, tt := range []struct {
		name, body string
		err        error
		lookups    [][]kernel.ID
		wantErr    error
	}{
		{name: "no reference", body: "**hi**"},
		{name: "members, one batch", body: ref(member) + " and _" + ref(other) + "_ " + ref(member), lookups: [][]kernel.ID{{member, other, member}}},
		{name: "unknown member", body: ref(member) + " " + ref(unknown), lookups: [][]kernel.ID{{member, unknown}}, wantErr: conversation.ErrUnknownReference},
		{name: "another organisation's member", body: ref(foreign), lookups: [][]kernel.ID{{foreign}}, wantErr: conversation.ErrUnknownReference},
		{name: "malformed tokens stay text", body: "<@nope> " + strings.ToUpper(ref(unknown)) + " `" + ref(unknown) + "`"},
		{name: "lookup failure", body: ref(member), err: failure, lookups: [][]kernel.ID{{member}}, wantErr: failure},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &postingFake{t: t, topic: kernel.ID{6}}
			d := &referenceDirectory{t: t, err: tt.err, members: map[kernel.ID][]kernel.ID{{1}: {member, other}, {9}: {foreign}}}
			posting := conversation.NewPagePosting(f,
				func(tx platform.Tx) conversation.Writer { f.bound(tx); return f },
				func(tx platform.Tx) conversation.EventSequence { f.bound(tx); return f },
				func(tx platform.Tx) conversation.EventAppender { f.bound(tx); return f },
				nil, d, func(platform.Snapshot) conversation.MemberDirectory { return d }, nil, nil)
			posted, err := posting.Post(t.Context(), m, kernel.ID{3}, tt.body)
			if !reflect.DeepEqual(d.lookups, tt.lookups) {
				t.Fatalf("lookups %v, want %v", d.lookups, tt.lookups)
			}
			if tt.wantErr == nil {
				if err != nil || posted.Body != tt.body {
					t.Fatalf("posted %q, %v", posted.Body, err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) || len(f.calls) != 0 {
				t.Fatalf("error %v, calls %v", err, f.calls)
			}
			// Only a reference the directory did not return is a validation error.
			if refused := errors.Is(err, conversation.ErrInvalidBody); refused != (tt.wantErr == conversation.ErrUnknownReference) {
				t.Fatalf("error %v as a validation error: %t", err, refused)
			}
		})
	}
	// Posting without a directory cannot check a reference, so it refuses it
	// as a server error rather than storing it unchecked.
	f := &postingFake{t: t, topic: kernel.ID{6}}
	if _, err := f.posting(nil).Post(t.Context(), m, kernel.ID{3}, ref(member)); err == nil || errors.Is(err, conversation.ErrInvalidBody) || len(f.calls) != 0 {
		t.Fatalf("unchecked reference: %v, calls %v", err, f.calls)
	}
}

// An escaped text reads back as exactly the text, all of it literal.
func TestEscapeChatV1(t *testing.T) {
	for _, text := range []string{
		"**Deploy**", "<@" + realtime.FormatPayloadID(kernel.ID{0xb1}) + ">", "_x_ `c` \\*", "> quote", "```go", "see https://example.com/a", "設計 **重要**",
	} {
		blocks := conversation.ParseChatV1(conversation.EscapeChatV1(text))
		want := []conversation.BodyBlock{{Kind: conversation.BlockText, Lines: [][]conversation.BodyInline{{{Kind: conversation.InlineText, Text: text}}}}}
		if !reflect.DeepEqual(blocks, want) {
			t.Errorf("%q parsed as %+v", text, blocks)
		}
	}
}
