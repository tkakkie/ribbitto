package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"
)

func stage2EvidenceDriver(t *testing.T, n int) *stage2Driver {
	t.Helper()
	d := &stage2Driver{}
	if err := json.Unmarshal([]byte(stage2CredentialJSON()), &d.data); err != nil {
		t.Fatal(err)
	}
	// Virtual members need neither credentials nor network access.
	d.data.Accounts = append(d.data.Accounts, make([]struct {
		MemberID string   `json:"member_id"`
		Handle   string   `json:"handle"`
		Tokens   []string `json:"tokens"`
	}, n-len(d.data.Accounts))...)
	d.initMembers()
	return d
}

func TestStage2StreamPools(t *testing.T) {
	for _, g := range fixedStage2().Groups {
		d := stage2EvidenceDriver(t, g.Streams)
		counts := map[string]int{}
		for i, m := range d.members {
			page := stage2StreamPage(uint64(i))
			if page.Operation != "page" || page.LogicalID != uint64(i) || m.streamScope != d.readScope(page) {
				t.Fatal("establishment page differs from stream scope")
			}
			counts[m.streamScope]++
		}
		if counts["feed"] != g.Streams/2 || counts[d.data.Stage2.NamedTopics[0].ID] != g.Streams/4 || counts[d.data.Stage2.NamedTopics[1].ID] != g.Streams/4 {
			t.Fatalf("stream pools do not match read shares: %v", counts)
		}
	}
}

type stage2Simulation struct{ reads, refused, smallest, minReads, maxReads int }

// Advance virtual time along the unchanged schedule. Only matching durable post
// frames are applied, at post time + lag. HTTP has zero duration; contention is
// tested separately. Event runs never use scheduled pages or hidden GETs.
func simulateStage2ReadsExhaustive(t *testing.T, g stage2Group, seed uint64, lag time.Duration, events bool) stage2Simulation {
	t.Helper()
	d := stage2EvidenceDriver(t, g.Streams)
	start := time.Unix(0, 0)
	schedule, err := newStage2Schedule(g, seed, start)
	if err != nil {
		t.Fatal(err)
	}
	const initial = uint64(10000)
	pool := map[string]int{}
	for _, m := range d.members {
		m.applyPage(m.streamScope, stage2ReadPage{initial, true})
		pool[m.streamScope]++
	}
	// Track pool changes with the pure predicate rather than recount N at every
	// read. Each delivery still visits every covering member, as actual streams do.
	update := func(m *stage2ReadMember, mutate func()) {
		_, before := m.eligible(m.streamScope)
		mutate()
		_, after := m.eligible(m.streamScope)
		if before != after {
			if after {
				pool[m.streamScope]++
			} else {
				pool[m.streamScope]--
			}
		}
	}
	type delivery struct {
		slot     stage2Slot
		sequence uint64
		at       time.Time
	}
	var pending []delivery
	next := 0
	sequence := initial
	counts := make([]int, g.Streams)
	result := stage2Simulation{smallest: g.Streams}
	for _, slot := range schedule.slots {
		for next < len(pending) && !pending[next].at.After(slot.At) {
			post := pending[next]
			topic := d.data.Stage2.DefaultTopicIDs[0]
			if post.slot.Endpoint == "topic" {
				topic = d.readScope(post.slot)
			}
			marker := stage2PostMarker(post.slot)
			for member, m := range d.members {
				if m.streamScope == "feed" || m.streamScope == topic {
					update(m, func() { d.applyFrame(uint64(member), post.sequence, marker) })
				}
			}
			next++
		}
		m, scope := d.readMember(slot)
		switch slot.Operation {
		case "post":
			update(m, func() { m.reserve(scope, m.applied) })
			sequence++
			if events {
				pending = append(pending, delivery{slot, sequence, slot.At.Add(lag)})
			}
		case "page":
			if !events {
				update(m, func() { m.applyPage(scope, stage2ReadPage{sequence, true}) })
			}
		case "read":
			result.smallest = min(result.smallest, pool[scope])
			var member, cursor uint64
			var found bool
			if events {
				member, cursor, found = d.selectRead(slot)
			} else {
				member, found = selectStage2Member(slot.LogicalID, len(d.members), func(i uint64) bool {
					if d.members[i].streamScope != scope {
						return false
					}
					var eligible bool
					cursor, eligible = d.members[i].eligible(scope)
					return eligible
				})
			}
			if !found {
				result.refused++
				if events {
					t.Fatalf("scheduled read has no eligible member: N=%d posts=%d seed=%d L=%s read=%d at=%s scope=%s pool=%d", g.Streams, g.PostsPerSecond, seed, lag, slot.LogicalID, slot.At.Sub(start), scope, pool[scope])
				}
				continue
			}
			m = d.members[member]
			update(m, func() { m.reserve(scope, cursor) })
			if events {
				m.mu.Unlock()
				m.admission.Unlock()
			}
			counts[member]++
			result.reads++
		}
	}
	result.minReads, result.maxReads = slices.Min(counts), slices.Max(counts)
	if result.reads+result.refused != g.ReadsPerSecond*fixedStage2().WindowSeconds {
		t.Fatal("simulation omitted scheduled reads")
	}
	// Independently reconcile incremental pool tracking with the pure predicate.
	actual := map[string]int{}
	for _, m := range d.members {
		if _, ok := m.eligible(m.streamScope); ok {
			actual[m.streamScope]++
		}
	}
	for scope, count := range pool {
		if actual[scope] != count {
			t.Fatalf("eligible pool tracking: %d want %d", count, actual[scope])
		}
	}
	return result
}

// Only the newest applied cursor and newest other-author post per topic can
// affect eligibility. Keep the newest two distinct authors per topic and apply
// them lazily, in sequence order, when a member is examined. This is equivalent
// to exhaustive intake, without replaying hundreds of millions of overwritten
// frames under the race detector. No driver state or criterion is bypassed.
func simulateStage2Reads(t *testing.T, g stage2Group, seed uint64, lag time.Duration, events bool) stage2Simulation {
	t.Helper()
	if !events {
		return simulateStage2ReadsExhaustive(t, g, seed, lag, false)
	}
	d := stage2EvidenceDriver(t, g.Streams)
	start := time.Unix(0, 0)
	schedule, err := newStage2Schedule(g, seed, start)
	if err != nil {
		t.Fatal(err)
	}
	const initial = uint64(10000)
	pool, size := map[string]int{}, map[string]int{}
	for _, m := range d.members {
		m.applyPage(m.streamScope, stage2ReadPage{initial, true})
		pool[m.streamScope]++
		size[m.streamScope]++
	}
	type delivery struct {
		slot     stage2Slot
		sequence uint64
		at       time.Time
	}
	var pending []delivery
	latest := map[string][2]delivery{}
	next := 0
	sequence := initial
	refresh := func(member uint64) {
		m := d.members[member]
		var frames []delivery
		for topic, pair := range latest {
			if m.streamScope == "feed" || m.streamScope == topic {
				for _, frame := range pair {
					if frame.sequence != 0 {
						frames = append(frames, frame)
					}
				}
			}
		}
		slices.SortFunc(frames, func(a, b delivery) int {
			if a.sequence < b.sequence {
				return -1
			}
			if a.sequence > b.sequence {
				return 1
			}
			return 0
		})
		for _, frame := range frames {
			d.applyFrame(member, frame.sequence, stage2PostMarker(frame.slot))
		}
	}
	update := func(member uint64, mutate func()) {
		refresh(member)
		m := d.members[member]
		_, before := m.eligible(m.streamScope)
		mutate()
		_, after := m.eligible(m.streamScope)
		if before != after {
			if after {
				pool[m.streamScope]++
			} else {
				pool[m.streamScope]--
			}
		}
	}
	counts := make([]int, g.Streams)
	result := stage2Simulation{smallest: g.Streams}
	for _, slot := range schedule.slots {
		for next < len(pending) && !pending[next].at.After(slot.At) {
			post := pending[next]
			topic := d.data.Stage2.DefaultTopicIDs[0]
			if post.slot.Endpoint == "topic" {
				topic = d.readScope(post.slot)
			}
			pair := latest[topic]
			if pair[0].sequence != 0 && pair[0].slot.LogicalID%uint64(g.Streams) != post.slot.LogicalID%uint64(g.Streams) {
				pair[1] = pair[0]
			}
			pair[0] = post
			latest[topic] = pair
			// A delivered sequence exceeds every submitted bound: event runs have
			// no page GETs, and writes submit only an already-applied cursor. Thus
			// it refreshes the entire covering pool except possibly its own author.
			for _, scope := range []string{"feed", topic} {
				if size[scope] == 0 {
					continue
				}
				pool[scope] = size[scope]
				author := post.slot.LogicalID % uint64(g.Streams)
				if d.members[author].streamScope == scope {
					refresh(author)
					if _, ok := d.members[author].eligible(scope); !ok {
						pool[scope]--
					}
				}
			}
			next++
		}
		member := slot.LogicalID % uint64(g.Streams)
		scope := d.readScope(slot)
		switch slot.Operation {
		case "post":
			update(member, func() { m := d.members[member]; m.reserve(scope, m.applied) })
			sequence++
			pending = append(pending, delivery{slot, sequence, slot.At.Add(lag)})
		case "read":
			result.smallest = min(result.smallest, pool[scope])
			var cursor uint64
			selected, found := selectStage2Member(slot.LogicalID, len(d.members), func(i uint64) bool {
				if d.members[i].streamScope != scope {
					return false
				}
				refresh(i)
				var eligible bool
				cursor, eligible = d.members[i].eligible(scope)
				return eligible
			})
			if !found {
				t.Fatalf("scheduled read has no eligible member: N=%d posts=%d seed=%d L=%s read=%d at=%s scope=%s pool=%d", g.Streams, g.PostsPerSecond, seed, lag, slot.LogicalID, slot.At.Sub(start), scope, pool[scope])
			}
			update(selected, func() { d.members[selected].reserve(scope, cursor) })
			counts[selected]++
			result.reads++
		}
	}
	result.minReads, result.maxReads = slices.Min(counts), slices.Max(counts)
	if result.reads != g.ReadsPerSecond*fixedStage2().WindowSeconds {
		t.Fatal("simulation omitted scheduled reads")
	}
	actual := map[string]int{}
	for i, m := range d.members {
		refresh(uint64(i))
		if _, ok := m.eligible(m.streamScope); ok {
			actual[m.streamScope]++
		}
	}
	for scope, count := range pool {
		if actual[scope] != count {
			t.Fatalf("eligible pool tracking: %d want %d", count, actual[scope])
		}
	}
	return result
}

func TestStage2ReadSimulationEquivalence(t *testing.T) {
	group := fixedStage2().Groups[0]
	for _, lag := range []time.Duration{0, 5 * time.Second} {
		exhaustive := simulateStage2ReadsExhaustive(t, group, 0, lag, true)
		lazy := simulateStage2Reads(t, group, 0, lag, true)
		if lazy != exhaustive {
			t.Fatalf("lazy intake differs from exhaustive 600s proof at L=%s: %+v want %+v", lag, lazy, exhaustive)
		}
	}
}

func TestStage2ReadScheduleEvidence(t *testing.T) {
	for _, g := range fixedStage2().Groups {
		for _, seed := range []uint64{0, 1, 42, 865} {
			for _, lag := range []time.Duration{0, time.Second, 5 * time.Second} {
				t.Run(fmt.Sprintf("N%d/posts%d/seed%d/L%d", g.Streams, g.PostsPerSecond, seed, int(lag/time.Second)), func(t *testing.T) {
					t.Parallel()
					r := simulateStage2Reads(t, g, seed, lag, true)
					if r.refused != 0 || r.reads != g.ReadsPerSecond*600 || r.smallest <= 0 {
						t.Fatalf("read demand shortfall: %+v", r)
					}
					t.Logf("N=%d posts/s=%d seed=%d L=%ds reads=%d smallest_pool=%d reads/member=%d..%d", g.Streams, g.PostsPerSecond, seed, int(lag/time.Second), r.reads, r.smallest, r.minReads, r.maxReads)
				})
			}
			t.Run(fmt.Sprintf("page-only/N%d/posts%d/seed%d", g.Streams, g.PostsPerSecond, seed), func(t *testing.T) {
				t.Parallel()
				r := simulateStage2Reads(t, g, seed, 0, false)
				supply := g.Streams + g.PagesPerSecond*600
				if r.refused == 0 || r.reads > supply || supply >= g.ReadsPerSecond*600 {
					t.Fatalf("page-only control hid original shortfall: %+v supply=%d", r, supply)
				}
				t.Logf("page-only N=%d posts/s=%d seed=%d reads=%d refused=%d supply<=%d", g.Streams, g.PostsPerSecond, seed, r.reads, r.refused, supply)
			})
		}
	}
}
