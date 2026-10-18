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
// its decisions about blocks, escapes, code and links, without extracting text.
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
				tokens = append(tokens, textRun{start: i, end: last})
			}
			i = end - 1
		}
	}
	return tokens
}

func (p *Posting) convertHandles(ctx context.Context, organizationID kernel.ID, body string) (string, error) {
	tokens := handleTokens(body)
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
	var converted strings.Builder
	start := 0
	for _, token := range tokens {
		converted.WriteString(body[start:token.start])
		if id, ok := byHandle[strings.ToLower(body[token.start+1:token.end])]; ok {
			converted.WriteString("<@" + realtime.FormatPayloadID(id) + ">")
		} else {
			converted.WriteString(body[token.start:token.end])
		}
		start = token.end
	}
	converted.WriteString(body[start:])
	return converted.String(), nil
}
