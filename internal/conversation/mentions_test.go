package conversation_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

func TestPostingTypedHandles(t *testing.T) {
	member := kernel.ID{0xa1}
	ref := "<@" + realtime.FormatPayloadID(member) + ">"
	failure := errors.New("listing failed")
	for _, tc := range []struct {
		name, body, want string
		handle           string
		others           []string
		foreignOnly      bool
		lists            int
		listErr, wantErr error
	}{
		{name: "repeated, case insensitive", body: "@ToMoYa (@tomoya)", want: ref + " (" + ref + ")", lists: 1},
		{name: "punctuation", body: "@tomoya.", want: ref + ".", lists: 1},
		{name: "quote", body: "> @tomoya", want: "> " + ref, lists: 1},
		{name: "quote without space", body: ">@tomoya", want: ">" + ref, lists: 1},
		{name: "double quote marker is literal", body: ">>@tomoya", want: ">>@tomoya"},
		{name: "underscore", body: "@tom_oya", want: ref, handle: "tom_oya", lists: 1},
		{name: "unclosed delimiter is literal", body: "@tom._oya", want: ref, handle: "tom._oya", lists: 1},
		{name: "paired underscore after prefix", body: "_hi @tom_.oya_", want: "_hi " + ref + "_", handle: "tom_.oya", others: []string{"tom"}, lists: 1},
		{name: "paired underscores inside handle", body: "@tom._a_.oya", want: ref, handle: "tom._a_.oya", others: []string{"tom"}, lists: 1},
		{name: "two handles crossing delimiters", body: "@tom._oya @bob_.smith", want: ref + " <@" + realtime.FormatPayloadID(kernel.ID{0xc2}) + ">", handle: "tom._oya", others: []string{"tom", "bob_.smith", "bob"}, lists: 1},
		{name: "handle inside bold with italic", body: "**@tom._oya _hi_**", want: "**@tom._oya _hi_**", handle: "tom._oya", others: []string{"tom"}},
		{name: "single letter before underscore", body: "_hi @j_.doe_", want: "_hi " + ref + "_", handle: "j_.doe", lists: 1},
		{name: "only prefix exists", body: "_hi @tom_.oya_", want: "_hi @tom_.oya_", handle: "tom", lists: 1},
		{name: "after italic", body: "_x_ @tomoya", want: "_x_ " + ref, lists: 1},
		{name: "after bold", body: "**b** @tomoya", want: "**b** " + ref, lists: 1},
		{name: "dot and hyphen", body: "(@tom-oya.dev).", want: "(" + ref + ").", handle: "tom-oya.dev", lists: 1},
		{name: "bold text run", body: "**hi @tomoya**", want: "**hi " + ref + "**", lists: 1},
		{name: "code span", body: "`@tomoya`", want: "`@tomoya`"},
		{name: "closed fence", body: "```go\n@tomoya\n```", want: "```go\n@tomoya\n```"},
		{name: "unclosed fence", body: "```go\n@tomoya", want: "```go\n" + ref, lists: 1},
		{name: "escape", body: `\@tomoya`, want: `\@tomoya`},
		{name: "URL", body: "https://example.org/@tomoya", want: "https://example.org/@tomoya"},
		{name: "email", body: "a@tomoya.dev", want: "a@tomoya.dev"},
		{name: "foreign only", body: "@foreign", want: "@foreign", handle: "foreign", foreignOnly: true, lists: 1},
		{name: "unknown and foreign", body: "@unknown @foreign", want: "@unknown @foreign", lists: 1},
		{name: "reserved", body: "@everyone @here @channel @all", want: "@everyone @here @channel @all"},
		{name: "no candidate", body: "hello @x @1bad @日本", want: "hello @x @1bad @日本"},
		{name: "too long handle", body: "@" + strings.Repeat("a", 33), want: "@" + strings.Repeat("a", 33)},
		{name: "expanded limit", body: "@tomoya " + strings.Repeat("界", conversation.MaxMessageBodyLength-8), lists: 1, wantErr: conversation.ErrInvalidBody},
		{name: "listing failure", body: "@tomoya", lists: 1, listErr: failure, wantErr: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &postingFake{t: t, topic: kernel.ID{6}}
			d := &referenceDirectory{t: t, err: tc.listErr, handle: tc.handle, members: map[kernel.ID][]kernel.ID{{1}: {member}, {9}: {{0xb1}}}}
			d.handles = make(map[kernel.ID]string)
			for i, handle := range tc.others {
				id := kernel.ID{byte(0xc1 + i)}
				d.members[kernel.ID{1}] = append(d.members[kernel.ID{1}], id)
				d.handles[id] = handle
			}
			if tc.foreignOnly {
				delete(d.members, kernel.ID{1})
			}
			p := conversation.NewPagePosting(f,
				func(platform.Tx) conversation.Writer { return f },
				func(platform.Tx) conversation.EventSequence { return f },
				func(platform.Tx) conversation.EventAppender { return f },
				nil, d, func(platform.Snapshot) conversation.MemberDirectory { return d }, nil, nil)
			m := org.Membership{Organization: org.Organization{ID: kernel.ID{1}}, Member: org.Member{ID: kernel.ID{2}}}
			got, err := p.Post(t.Context(), m, kernel.ID{3}, tc.body)
			if !errors.Is(err, tc.wantErr) || d.lists != tc.lists || err == nil && got.Body != tc.want {
				t.Fatalf("body %q, error %v, lists %d; want %q, %v, %d", got.Body, err, d.lists, tc.want, tc.wantErr, tc.lists)
			}
			if tc.wantErr != nil && len(f.calls) != 0 {
				t.Fatalf("invalid post reached transaction: %v", f.calls)
			}
			if errors.Is(err, conversation.ErrInvalidBody) && !strings.Contains(err.Error(), "1–4000 Unicode code points") {
				t.Fatalf("wrong length error: %v", err)
			}
			if err == nil && strings.Contains(tc.want, ref) && len(d.lookups) != 1 {
				t.Fatalf("converted references were not checked: %v", d.lookups)
			}
		})
	}
}
