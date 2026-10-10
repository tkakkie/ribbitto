package postgres

import (
	"testing"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

func TestTopicUnreadRejectsUnequalArraysBeforeStatement(t *testing.T) {
	for _, lengths := range [][2]int{{1, 0}, {0, 1}} {
		_, _, err := (topicUnread{}).Count(t.Context(), kernel.ID{}, kernel.ID{}, 1, make([]kernel.ID, lengths[0]), make([]int64, lengths[1]), nil, nil)
		if err == nil {
			t.Fatal("unequal arrays accepted")
		}
	}
}
