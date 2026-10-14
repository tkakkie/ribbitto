package main

import (
	"fmt"
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
	stream      sync.Mutex // Held for the caller-owned stream lifetime.
	admission   sync.Mutex
	mu          sync.Mutex // Short evidence lock; never held across HTTP or verification.
	streamScope string     // Immutable after construction.
	applied     uint64
	pages       map[string]stage2ReadPage
	other       map[string]uint64
	// Used bounds are conservative reservations, not witnessed persisted state.
	used map[string]uint64
}

// Fixed groups divide evenly into pools matching the 50:25:25 read shares.
// The establishment GET is also the initial read evidence; no extra page GET.
func stage2StreamPage(member uint64) stage2Slot {
	return stage2Slot{Operation: "page", Endpoint: []string{"feed", "topic"}[member%2], LogicalID: member}
}

func (d *stage2Driver) initMembers() {
	d.members = make([]*stage2ReadMember, len(d.data.Accounts))
	for i := range d.members {
		d.members[i] = &stage2ReadMember{streamScope: d.readScope(stage2StreamPage(uint64(i))),
			pages: map[string]stage2ReadPage{}, other: map[string]uint64{}, used: map[string]uint64{}}
	}
}

func (d *stage2Driver) readMember(slot stage2Slot) (*stage2ReadMember, string) {
	return d.members[slot.LogicalID%uint64(len(d.members))], d.readScope(slot)
}

// selectStage2Member bounds the rotation scan independently of admission and
// evidence. The driver and virtual-time proof supply the same candidate check.
func selectStage2Member(start uint64, n int, candidate func(uint64) bool) (uint64, bool) {
	for offset := range n {
		member := (start%uint64(n) + uint64(offset)) % uint64(n)
		if candidate(member) {
			return member, true
		}
	}
	return 0, false
}

// Selection retains both locks; the caller releases evidence after reserving
// the cursor, and admission after HTTP/proof. No blocking reacquire on a read.
func (d *stage2Driver) selectRead(slot stage2Slot) (uint64, uint64, bool) {
	scope := d.readScope(slot)
	var cursor uint64
	member, found := selectStage2Member(slot.LogicalID, len(d.members), func(member uint64) bool {
		m := d.members[member]
		if m.streamScope != scope || !m.admission.TryLock() {
			return false
		}
		if !m.mu.TryLock() {
			m.admission.Unlock()
			return false
		}
		var eligible bool
		cursor, eligible = m.eligible(scope)
		if !eligible {
			m.mu.Unlock()
			m.admission.Unlock()
		}
		return eligible
	})
	return member, cursor, found
}

func (d *stage2Driver) readScope(slot stage2Slot) string {
	if slot.Endpoint == "topic" {
		return d.data.Stage2.NamedTopics[(slot.LogicalID/2)%uint64(len(d.data.Stage2.NamedTopics))].ID
	}
	return "feed"
}

func stage2PostMarker(slot stage2Slot) string {
	return fmt.Sprintf("stage2-post:%s:%d", slot.Endpoint, slot.LogicalID)
}

// applyFrame is called only after a complete durable frame has been applied,
// not on receipt. Feed streams receive all hot-channel posts; topic streams
// receive only their topic's posts (stream-scope.md). Ephemeral frames have no ID.
func (d *stage2Driver) applyFrame(member, sequence uint64, marker string) {
	if sequence == 0 || sequence > (1<<63)-1 || member >= uint64(len(d.members)) {
		return
	}
	m, _ := d.readMember(stage2Slot{LogicalID: member})
	m.mu.Lock()
	defer m.mu.Unlock()
	m.applied = max(m.applied, sequence)
	parts := strings.Split(marker, ":")
	if len(parts) != 3 || parts[0] != "stage2-post" || (parts[1] != "feed" && parts[1] != "topic") {
		return
	}
	id, err := strconv.ParseUint(parts[2], 10, 64)
	if err != nil || id%uint64(len(d.data.Accounts)) == member {
		return
	}
	topic := d.data.Stage2.DefaultTopicIDs[0]
	if parts[1] == "topic" {
		topic = d.readScope(stage2Slot{Endpoint: "topic", LogicalID: id})
	}
	if m.streamScope == "feed" || m.streamScope == topic {
		m.other[topic] = max(m.other[topic], sequence)
	}
}

func (m *stage2ReadMember) applyPage(scope string, page stage2ReadPage) {
	m.pages[scope] = page
	m.applied = max(m.applied, page.cursor)
}

// eligible is pure: both the driver and deterministic schedule proof use it.
// A feed read needs one qualifying topic; a topic read respects the feed bound.
func (m *stage2ReadMember) eligible(scope string) (uint64, bool) {
	for topic, sequence := range m.other {
		if (scope == "feed" || scope == topic) && sequence > max(m.used["feed"], m.used[topic]) && sequence <= m.applied {
			return m.applied, true
		}
	}
	page := m.pages[scope]
	return page.cursor, page.unread && page.cursor > max(m.used["feed"], m.used[scope])
}

func (m *stage2ReadMember) reserve(scope string, cursor uint64) {
	m.used[scope] = max(m.used[scope], cursor)
	clear(m.pages)
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
