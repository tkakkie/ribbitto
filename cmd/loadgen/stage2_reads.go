package main

import (
	"regexp"
	"strconv"
	"strings"
	"sync"
)

type stage2ReadPage struct {
	cursor uint64
	unread bool
}

type stage2ReadMember struct {
	mu    sync.Mutex
	pages map[string]stage2ReadPage
	// Used bounds are conservative reservations, not witnessed persisted state.
	used map[string]uint64
}

func (d *stage2Driver) readMember(slot stage2Slot) (*stage2ReadMember, string) {
	member := slot.LogicalID % uint64(len(d.data.Accounts))
	scope := slot.Endpoint
	if scope == "topic" {
		scope = d.data.Stage2.NamedTopics[(slot.LogicalID/2)%uint64(len(d.data.Stage2.NamedTopics))].ID
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	m := d.members[member]
	if m == nil {
		m = &stage2ReadMember{pages: map[string]stage2ReadPage{}, used: map[string]uint64{}}
		d.members[member] = m
	}
	return m, scope
}

// Only the current full-page read form supplies an eligible snapshot. A visible
// submit button proves unread work in that scope at the snapshot; a larger
// organization cursor alone could refer only to unrelated events or own posts.
func stage2ReadPageFrom(raw []byte) stage2ReadPage {
	forms := regexp.MustCompile(`(?s)<form id="read-form"[^>]*>.*?</form>`).FindAll(raw, -1)
	if len(forms) != 1 {
		return stage2ReadPage{}
	}
	form := string(forms[0])
	cursors := regexp.MustCompile(`<input type="hidden" name="cursor" value="([0-9]+)"`).FindAllStringSubmatch(form, -1)
	if len(cursors) != 1 {
		return stage2ReadPage{}
	}
	cursor, err := strconv.ParseUint(cursors[0][1], 10, 63)
	if err != nil {
		return stage2ReadPage{}
	}
	return stage2ReadPage{cursor, strings.Contains(form, `type="submit"`)}
}
