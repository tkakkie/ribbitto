package web

import (
	"strings"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

// viewBody converts a parsed chat_v1 body into its view model, nil for
// plain_text. References take their names only from entry.References, which
// the organisation's directory filled, and their display names go through
// memberDisplayName like an author's.
func viewBody(entry conversation.Entry) []view.BodyBlock {
	if entry.Blocks == nil {
		return nil
	}
	blocks := make([]view.BodyBlock, len(entry.Blocks))
	for i, block := range entry.Blocks {
		switch block.Kind {
		case conversation.BlockCode:
			blocks[i] = view.BodyBlock{Kind: view.BodyCodeBlock, Code: strings.Join(block.Code, "\n")}
			continue
		case conversation.BlockQuote:
			blocks[i].Kind = view.BodyQuote
		}
		for _, line := range block.Lines {
			blocks[i].Lines = append(blocks[i].Lines, viewInlines(entry, line))
		}
	}
	return blocks
}

var inlineKinds = map[conversation.InlineKind]view.BodyKind{
	conversation.InlineText: view.BodyText, conversation.InlineBold: view.BodyBold,
	conversation.InlineItalic: view.BodyItalic, conversation.InlineCode: view.BodyCode,
	conversation.InlineLink: view.BodyLink, conversation.InlineReference: view.BodyMember,
}

func viewInlines(entry conversation.Entry, nodes []conversation.BodyInline) []view.BodyInline {
	out := make([]view.BodyInline, len(nodes))
	for i, node := range nodes {
		// Only a reference has a Member; an unresolved one gets no names.
		member := entry.References[node.Member]
		out[i] = view.BodyInline{Kind: inlineKinds[node.Kind], Text: node.Text, Children: viewInlines(entry, node.Children),
			DisplayName: memberDisplayName(member.DisplayName), Handle: member.Handle}
	}
	return out
}
