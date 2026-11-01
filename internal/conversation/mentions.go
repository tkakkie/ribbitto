package conversation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

// handleTokens uses the parser's source runs, so posting and replay share
// its exclusions for blocks, escapes, code and links, without extracting text.
// Bold/italic delimiters stay in the runs so underscores cannot split handles.
func handleTokens(body string) []textRun {
	var runs, tokens []textRun
	parseChatV1(body, &runs)
	for _, run := range runs {
		for i := run.start; i < run.end; i++ {
			if body[i] != '@' {
				continue
			}
			before, _ := utf8.DecodeLastRuneInString(body[run.line:i])
			if i != run.line && !unicode.IsSpace(before) && before != '(' {
				continue
			}
			end := i + 1
			for end < run.end && (isASCIIAlphanumeric(body[end]) || strings.ContainsRune("_.-", rune(body[end]))) {
				end++
			}
			last := end
			for last > i+1 && !isASCIIAlphanumeric(body[last-1]) {
				last--
			}
			if _, err := org.ValidateHandle(body[i+1 : last]); err == nil {
				tokens = append(tokens, textRun{start: i, end: last, line: run.line})
			}
			i = end - 1
		}
	}
	return tokens
}

func (p *Posting) convertHandles(ctx context.Context, organizationID kernel.ID, body string, tokens []textRun) (string, error) {
	if len(tokens) == 0 {
		return body, nil
	}
	if p.members == nil {
		return "", errors.New("posting has no member directory to resolve handles")
	}
	var members []org.ListedMember
	err := p.snapshots.InSnapshot(ctx, func(snapshot platform.Snapshot) error {
		var err error
		members, err = p.members(snapshot).ListSearchMembers(ctx, organizationID)
		return err
	})
	if err != nil {
		return "", fmt.Errorf("listing members for typed mentions: %w", err)
	}
	byHandle := make(map[string]kernel.ID, len(members))
	for _, member := range members {
		byHandle[strings.ToLower(member.Handle)] = member.ID
	}
	return replaceHandles(body, tokens, func(handle string) (kernel.ID, bool) {
		id, ok := byHandle[strings.ToLower(handle)]
		return id, ok
	}), nil
}

// replaceHandles writes a reference for each token whose handle lookup
// finds. A reference ends in ">", which is not a word character, so a "_"
// right after it can open italics where the typed handle's last letter
// stopped it. Unless that "_" closes italics in the converted line, it is
// escaped, so the stored body reads it as literal, as the typed body did.
func replaceHandles(body string, tokens []textRun, lookup func(handle string) (kernel.ID, bool)) string {
	var converted strings.Builder
	start, line := 0, 0 // line: where the current token's line content starts in converted
	for _, token := range tokens {
		if token.line >= start {
			line = converted.Len() + token.line - start
		}
		converted.WriteString(body[start:token.start])
		start = token.end
		id, ok := lookup(body[token.start+1 : token.end])
		if !ok {
			converted.WriteString(body[token.start:token.end])
			continue
		}
		converted.WriteString("<@" + realtime.FormatPayloadID(id) + ">")
		if token.end < len(body) && body[token.end] == '_' {
			end := strings.IndexByte(body[token.end:], '\n')
			if end < 0 {
				end = len(body) - token.end
			}
			content := converted.String()[line:] + body[token.end:token.end+end]
			if !closesItalic(content, converted.Len()-line) {
				converted.WriteByte('\\')
			}
		}
	}
	converted.WriteString(body[start:])
	return converted.String()
}
