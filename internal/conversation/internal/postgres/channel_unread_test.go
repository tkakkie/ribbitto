package postgres

import (
	"testing"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

func TestChannelUnreadRejectsUnequalArraysBeforeStatement(t *testing.T) {
	for _, lengths := range [][3]int{{1, 0, 1}, {1, 1, 0}, {0, 1, 1}} {
		// Nil queries prove the rejection happens before executing any statement.
		_, _, err := (channelUnread{}).Count(t.Context(), kernel.ID{}, nil,
			make([]kernel.ID, lengths[0]), make([]int64, lengths[1]), make([]int64, lengths[2]))
		if err == nil {
			t.Fatal("unequal arrays accepted")
		}
	}
}
