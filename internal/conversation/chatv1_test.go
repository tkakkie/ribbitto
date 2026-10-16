package conversation_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

const memberUUID = "0190a0b1-2c3d-7e4f-8a9b-0c1d2e3f4a5b"

// chatV1Cases fix input → tree. Blocks print as T (text), Q (quote) or
// C:<language> (code) with lines split by " / " and ∅ for a blank one;
// inline nodes as quoted text, B(…) bold, I(…) italic, C(…) code span,
// L(…) link and R(…) reference.
var chatV1Cases = []struct{ name, in, want string }{
	// Decision 39's extraction table.
	{"bold and italic", "**Deploy** _today_", `T[B("Deploy") " " I("today")]`},
	{"code span", "use `a*b` here", `T["use " C("a*b") " here"]`},
	{"escaped delimiters", `\*\*not bold\*\*`, `T["**not bold**"]`},
	{"quote then text", "> quoted line\nreply", `Q["quoted line"] T["reply"]`},
	{"fence with tag", "```go\nx := 1\n```", `C:go["x := 1"]`},
	{"link trailing dot", "see https://example.com/a.", `T["see " L("https://example.com/a") "."]`},
	{"bold link", "**see https://example.com/x**", `T[B("see " L("https://example.com/x"))]`},
	{"crossing", "**a _b** c_", `T["**a " I("b** c")]`},
	{"reference", "hi <@" + memberUUID + ">", `T["hi " R(` + memberUUID + `)]`},
	// Literals the issue lists, and other constructs outside the grammar.
	{"snake_case", "snake_case", `T["snake_case"]`},
	{"Japanese underscores", "日本_語_", `T["日本_語_"]`},
	{"Japanese bold", "これは**重要**です", `T["これは" B("重要") "です"]`},
	{"single asterisks", "*single*", `T["*single*"]`},
	{"markdown link", "[text](url)", `T["[text](url)"]`},
	{"markdown link to URL", "[text](https://example.com)", `T["[text](" L("https://example.com") ")"]`},
	{"heading list table image", "# Title\n- item\n1. item\n| a | b |\n![alt](x.png)", `T["# Title" / "- item" / "1. item" / "| a | b |" / "![alt](x.png)"]`},
	{"raw HTML", "<b>x</b> <script>", `T["<b>x</b> <script>"]`},
	{"unclosed fence", "```go\nx := 1", `T["` + "```go" + `" / "x := 1"]`},
	{"nested quote", ">> a\n> > b", `Q["> a" / "> b"]`},
	{"link trailing punctuation", "https://example.com/a?!.,:;'*_ x", `T[L("https://example.com/a") "?!.,:;'*_ x"]`},
	{"link unbalanced paren", "(see https://example.com/x)", `T["(see " L("https://example.com/x") ")"]`},
	{"link balanced paren", "https://example.com/Go_(lang))", `T[L("https://example.com/Go_(lang)") ")"]`},
	{"link after paren", "(https://example.com)", `T["(" L("https://example.com") ")"]`},
	// LRI, RLI, FSI, PDI, LRM, RLM and ALM end a link.
	{"link ends at bidi marks", "https://a.b\u2066c https://a.b\u2067c https://a.b\u2068c https://a.b\u2069c https://a.b\u200ec https://a.b\u200fc https://a.b\u061cc", `T[L("https://a.b") "\u2066c " L("https://a.b") "\u2067c " L("https://a.b") "\u2068c " L("https://a.b") "\u2069c " L("https://a.b") "\u200ec " L("https://a.b") "\u200fc " L("https://a.b") "\u061cc"]`},
	{"link ends at brackets and quote", `http://a.b<c http://a.b>c http://a.b"c`, `T[L("http://a.b") "<c " L("http://a.b") ">c " L("http://a.b") "\"c"]`},
	{"not a link", "xhttps://a.b ftp://a.b https:// http:///a https://. HTTPS://a.b", `T["xhttps://a.b ftp://a.b https:// http:///a https://. HTTPS://a.b"]`},
	{"no host", "https://@ https://user@/path https://a@b@/x https://:80 https://-a http://?q", `T["https://@ https://user@/path https://a@b@/x https://:80 https://-a http://?q"]`},
	{"hosts", "https://u@example.com/a https://[::1]:8080/ http://例え.jp http://127.0.0.1 https://a/@", `T[L("https://u@example.com/a") " " L("https://[::1]:8080/") " " L("http://例え.jp") " " L("http://127.0.0.1") " " L("https://a/@")]`},
	{"malformed reference", "<@" + strings.ToUpper(memberUUID) + "> <@0190a0b1> <@" + memberUUID + " <" + memberUUID + ">", `T["<@` + strings.ToUpper(memberUUID) + `> <@0190a0b1> <@` + memberUUID + ` <` + memberUUID + `>"]`},
	{"code keeps white space", "` a  b\t`\n```\n  x\n\n\ty  \n```", `T[C(" a  b\t")] C:["  x" / "" / "\ty  "]`},
	// Escapes and code spans.
	{"escapes", `\` + "`a` \\a \\\\ \\。", "T[\"`a` \\\\a \\\\ \\\\。\"]"},
	{"backslash in code", "`a\\*b`", `T[C("a\\*b")]`},
	{"unclosed and empty code span", "``a\nb`", `T["` + "``a" + `" / "b` + "`" + `"]`},
	{"empty pair then span", "``a`", `T["` + "`" + `" C("a")]`},
	// Underscore word boundaries, for any script.
	{"underscore boundaries", "(_a_) x _y_. _z_", `T["(" I("a") ") x " I("y") ". " I("z")]`},
	{"underscore after letters", "слово_два_ v_x_ naïve_x_", `T["слово_два_ v_x_ naïve_x_"]`},
	{"underscore after digit and mark", "\u0663_\u0664_ e\u0301_c_ \u0915\u093f_x_", "T[\"\u0663_\u0664_ e\u0301_c_ \u0915\u093f_x_\"]"},
	{"underscore before word", "_a_b _a_日", `T["_a_b _a_日"]`},
	{"underscore next to symbols", "🙂_x_🙂 +_y_=", `T["🙂" I("x") "🙂 +" I("y") "="]`},
	// Delimiters: white space, nesting limit, unclosed at the line's end.
	{"white space blocks delimiters", "** a** _ b_ **c **", `T["** a** _ b_ **c **"]`},
	{"nesting limit", "**a _b **c** d_ e**", `T[B("a " I("b **c** d") " e")]`},
	{"same kind nested", "**a **b** c**", `T[B("a " B("b") " c")]`},
	{"crossing italic first", "_a **b_ c**", `T["_a " B("b_ c")]`},
	{"unclosed per line", "**a\nb** _c", `T["**a" / "b** _c"]`},
	{"triple asterisks", "***a***", `T[B("*a") "*"]`},
	// Blocks.
	{"blank lines kept", "a\n\n\nb", `T["a" / ∅ / ∅ / "b"]`},
	{"quote holds inline only", "> **a** `b`\n>\n> ```", `Q[B("a") " " C("b") / ∅ / "` + "```" + `"]`},
	{"fence needs exact closing", "```\na\n``` \n```go\nb\n```", `C:["a" / "` + "``` " + `" / "` + "```go" + `" / "b"]`},
	{"empty fence and text", "```\n```\nx", `C:[] T["x"]`},
	{"language tag characters", "```c++#.-\nx\n```", `C:c++#.-["x"]`},
	{"twenty character tag", "```" + strings.Repeat("a", 20) + "\nx\n```", `C:` + strings.Repeat("a", 20) + `["x"]`},
	{"not a fence opening", "```" + strings.Repeat("a", 21) + "\n```go x\n```", `T["` + "```" + strings.Repeat("a", 21) + `" / "` + "```go x" + `" / "` + "```" + `"]`},
}

func TestParseChatV1(t *testing.T) {
	for _, tc := range chatV1Cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatBlocks(conversation.ParseChatV1(tc.in)); got != tc.want {
				t.Fatalf("ParseChatV1(%q)\n got %s\nwant %s", tc.in, got, tc.want)
			}
		})
	}
}

// TestParseChatV1Linear parses inputs far over the body limit shaped to
// make a rescan quadratic: unclosed fences, backticks and failed links.
func TestParseChatV1Linear(t *testing.T) {
	for _, in := range []string{
		strings.Repeat("```go\n", 100_000),
		strings.Repeat("`", 1) + strings.Repeat("a", 500_000),
		strings.Repeat("(https://@", 60_000),
		strings.Repeat("**a _b ", 80_000),
	} {
		start := time.Now()
		conversation.ParseChatV1(in)
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Fatalf("parsing %q… took %s", in[:12], elapsed)
		}
	}
}

func FuzzParseChatV1(f *testing.F) {
	for _, tc := range chatV1Cases {
		f.Add(tc.in)
	}
	f.Fuzz(func(t *testing.T, input string) {
		body, err := conversation.ValidateMessageBody(input)
		if err != nil {
			return
		}
		lines := strings.Split(body, "\n")
		n := 0
		for _, b := range conversation.ParseChatV1(body) {
			if b.Kind == conversation.BlockCode {
				if n+len(b.Code)+1 >= len(lines) || lines[n] != "```"+b.Language || lines[n+len(b.Code)+1] != "```" ||
					strings.Join(lines[n+1:n+1+len(b.Code)], "\n") != strings.Join(b.Code, "\n") {
					t.Fatalf("code block %q does not match lines from %d of %q", b.Code, n, body)
				}
				n += len(b.Code) + 2
				continue
			}
			for _, nodes := range b.Lines {
				if n >= len(lines) {
					t.Fatalf("more lines than %q has", body)
				}
				a := accounting{t: t, source: lines[n]}
				if quote := a.take(">"); quote != (b.Kind == conversation.BlockQuote) {
					t.Fatalf("line %q in a block of kind %d", lines[n], b.Kind)
				} else if quote {
					a.take(" ")
				}
				a.nodes(nodes, 0)
				if a.pos != len(a.source) {
					t.Fatalf("%q: tree accounts only for %q", a.source, a.source[:a.pos])
				}
				n++
			}
		}
		if n != len(lines) {
			t.Fatalf("tree has %d of %q's %d lines", n, body, len(lines))
		}
	})
}

// accounting rebuilds a line from its tree, position by position: literal
// text (where a backslash before ASCII punctuation must be an escape), code
// content, links and reference IDs, between their syntax. It also checks
// that at most two delimiters are open around any content.
type accounting struct {
	t      *testing.T
	source string
	pos    int
}

func (a *accounting) take(s string) bool {
	if !strings.HasPrefix(a.source[a.pos:], s) {
		return false
	}
	a.pos += len(s)
	return true
}

func (a *accounting) expect(s string) {
	if !a.take(s) {
		a.t.Fatalf("%q: expected %q at %d", a.source, s, a.pos)
	}
}

func (a *accounting) nodes(nodes []conversation.BodyInline, depth int) {
	if depth > 2 {
		a.t.Fatalf("%q nests %d delimiters", a.source, depth)
	}
	for _, node := range nodes {
		switch node.Kind {
		case conversation.InlineBold:
			a.expect("**")
			a.nodes(node.Children, depth+1)
			a.expect("**")
		case conversation.InlineItalic:
			a.expect("_")
			a.nodes(node.Children, depth+1)
			a.expect("_")
		case conversation.InlineCode:
			a.expect("`" + node.Text + "`")
		case conversation.InlineLink:
			a.expect(node.Text)
		case conversation.InlineReference:
			a.expect("<@" + realtime.FormatPayloadID(node.Member) + ">")
		default:
			for _, r := range node.Text {
				if rest := a.source[a.pos:]; len(rest) > 1 && rest[0] == '\\' && strings.IndexByte("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", rest[1]) >= 0 {
					a.expect("\\")
				}
				a.expect(string(r))
			}
		}
	}
}

func formatBlocks(blocks []conversation.BodyBlock) string {
	var parts []string
	for _, b := range blocks {
		var lines []string
		switch b.Kind {
		case conversation.BlockCode:
			for _, line := range b.Code {
				lines = append(lines, fmt.Sprintf("%q", line))
			}
			parts = append(parts, "C:"+b.Language+"["+strings.Join(lines, " / ")+"]")
			continue
		case conversation.BlockQuote:
			parts = append(parts, "Q")
		default:
			parts = append(parts, "T")
		}
		for _, line := range b.Lines {
			lines = append(lines, formatInline(line))
		}
		parts[len(parts)-1] += "[" + strings.Join(lines, " / ") + "]"
	}
	return strings.Join(parts, " ")
}

func formatInline(nodes []conversation.BodyInline) string {
	if len(nodes) == 0 {
		return "∅"
	}
	var parts []string
	for _, node := range nodes {
		switch node.Kind {
		case conversation.InlineBold:
			parts = append(parts, "B("+formatInline(node.Children)+")")
		case conversation.InlineItalic:
			parts = append(parts, "I("+formatInline(node.Children)+")")
		case conversation.InlineCode:
			parts = append(parts, fmt.Sprintf("C(%q)", node.Text))
		case conversation.InlineLink:
			parts = append(parts, fmt.Sprintf("L(%q)", node.Text))
		case conversation.InlineReference:
			parts = append(parts, "R("+realtime.FormatPayloadID(node.Member)+")")
		default:
			parts = append(parts, fmt.Sprintf("%q", node.Text))
		}
	}
	return strings.Join(parts, " ")
}
