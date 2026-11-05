package conversation

import "github.com/tkakkie/ribbitto/internal/kernel"

// ExtractMentions returns distinct referenced members in first-appearance order.
// Only chat_v1 carries mentions; code and escaped references remain text.
func ExtractMentions(format Format, body string) []kernel.ID {
	if format != FormatChatV1 {
		return nil
	}
	ids := appendReferences(nil, ParseChatV1(body))
	seen := make(map[kernel.ID]bool, len(ids))
	distinct := ids[:0]
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			distinct = append(distinct, id)
		}
	}
	return distinct
}
