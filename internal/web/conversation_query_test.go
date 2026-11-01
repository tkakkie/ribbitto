package web

import "testing"

// FuzzConversationQuery checks that every position a link accepts survives
// conversationState unchanged: panel links and redirects carry a page's
// position only through conversationState, so a position it encodes
// differently, or that conversationQuery then refuses, would send the reader
// to another page or to a 400 (decision 36).
func FuzzConversationQuery(f *testing.F) {
	for _, raw := range []string{
		"before=5", "around=7&unread=3", "before=1&around=2", "unread=1", "before=+5", "before=05",
		"around=9223372036854775807&unread=0", "before=5;x", "%zz", "before=5&before=6", "before=5&x=1", "around=7&unread=%2B3",
	} {
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		_, at, ok := conversationQuery(raw)
		if !ok {
			return
		}
		state := conversationState(at)
		_, again, ok := conversationQuery(state)
		if !ok || again.before != at.before || again.around != at.around || (again.firstUnread == nil) != (at.firstUnread == nil) ||
			again.firstUnread != nil && *again.firstUnread != *at.firstUnread {
			t.Fatalf("query %q gives %+v, encoded as %q, which gives %+v, %t", raw, at, state, again, ok)
		}
	})
}
