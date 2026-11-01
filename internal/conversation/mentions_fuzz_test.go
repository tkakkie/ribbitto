package conversation

import (
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

// convertAll runs replaceHandles with a directory that knows every valid
// handle (its ID derived from the lower-case handle), so the property below
// is about the tokeniser, the conversion and the parser, not the directory.
func convertAll(body string, tokens []textRun) (string, map[kernel.ID]Reference) {
	refs := map[kernel.ID]Reference{}
	converted := replaceHandles(body, tokens, func(handle string) (kernel.ID, bool) {
		handle = strings.ToLower(handle)
		sum := sha256.Sum256([]byte(handle))
		var id kernel.ID
		copy(id[:], sum[:16])
		refs[id] = Reference{Handle: handle}
		return id, true
	})
	return converted, refs
}

func countReferences(blocks []BodyBlock) int {
	return len(appendReferences(nil, blocks))
}

// FuzzTypedHandlesRoundTrip: converting typed @handles to references must be
// invisible in the message's text (decision 38/39): the text of the stored
// body, with every reference resolved to its handle, equals the text of the
// typed body with the converted handles lower-cased, and the stored body has
// exactly one reference per token plus the ones the author typed literally.
func FuzzTypedHandlesRoundTrip(f *testing.F) {
	for _, s := range []string{
		"@tomoya", "@ToMoYa (@tomoya)", "_hi @tom_.oya_", "**hi @tomoya**", "> @bob", ">@bob",
		"```go\n@tomoya", "```go\n@a1\n```", "`@x1`", `\@tomoya`, "https://e.org/@tomoya", "a@b.dev",
		"_x @ab_ y", "@ab_x_", "@ab**x**", "**@ab**", "(@ab) _(@cd)_", "@ab.\n@cd-", "@ab<@cd",
		"_@ab_", "** @ab**", "@ab @cd @ef_gh_ _ij @kl_", "x _@ab_ y", "@ab _c_", "@ab　_x_",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, input string) {
		body, err := ValidateMessageBody(input)
		if err != nil {
			return
		}
		before := countReferences(ParseChatV1(body))
		tokens := handleTokens(body)
		converted, refs := convertAll(body, tokens)
		blocks := ParseChatV1(converted)
		if got := countReferences(blocks); got != before+len(tokens) {
			t.Fatalf("typed %q: %d tokens + %d typed references, converted %q parses %d references", body, len(tokens), before, converted, got)
		}
		// Expected text: the typed body's text with each token lower-cased.
		var lowered strings.Builder
		start := 0
		for _, token := range tokens {
			lowered.WriteString(body[start:token.start])
			lowered.WriteString(strings.ToLower(body[token.start:token.end]))
			start = token.end
		}
		lowered.WriteString(body[start:])
		typedRefs := map[kernel.ID]Reference{}
		for _, id := range appendReferences(nil, ParseChatV1(body)) {
			typedRefs[id] = Reference{Handle: "typed"}
			refs[id] = Reference{Handle: "typed"}
		}
		want := Entry{Message: Message{Format: FormatChatV1, Body: lowered.String()}, References: typedRefs}.Text("?")
		got := Entry{Message: Message{Format: FormatChatV1, Body: converted}, Blocks: blocks, References: refs}.Text("?")
		// A typed handle with "_" deliberately wins over italic delimiters
		// (TestPostingTypedHandles "paired underscore after prefix"), so the
		// typed text is only comparable when no token holds one.
		for _, token := range tokens {
			if strings.Contains(body[token.start:token.end], "_") {
				return
			}
		}
		if got != want {
			t.Fatalf("typed %q converted to %q\n text %q\n want %q", body, converted, got, want)
		}
	})
}
