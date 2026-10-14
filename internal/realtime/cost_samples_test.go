package realtime_test

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

type costExactKind uint8

const (
	costNormal costExactKind = iota
	costDelta
	costPeriodic
)

type costExpectedMessage struct {
	channel, topic kernel.ID
	sequence       int64
}

// Capture fixture identities/history before the timed path. Counts below are
// independently enumerated from these messages and the successful post log;
// neither application counts nor later oracle SQL supply expected digests.
func (r *costRunner) prepareSamples(ctx context.Context, initial []sidebarOracleModel) error {
	r.initial = initial
	rows, err := r.truth.Query(ctx, `SELECT channel_id,topic_id,event_seq FROM message WHERE organization_id=$1`, r.subs[0].Organization)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var m costExpectedMessage
		if err := rows.Scan(&m.channel, &m.topic, &m.sequence); err != nil {
			return err
		}
		r.history = append(r.history, m)
	}
	return rows.Err()
}

type costSamples struct {
	byKind            [3][]time.Duration
	total, incomplete int
	overflowedStreams int
}

func (s costSamples) report(t *testing.T, arm string) {
	t.Helper()
	t.Logf("persist-to-exact: arm=%s condition=production-only samples=%d incomplete=%d overflowed-streams=%d", arm, s.total, s.incomplete, s.overflowedStreams)
	for kind, name := range []string{"normal-reconciliation", "delta", "periodic-repair"} {
		t.Log(s.categoryReport(costExactKind(kind), name))
	}
}

func (s costSamples) categoryReport(kind costExactKind, name string) string {
	p := percentiles(s.byKind[kind])
	return fmt.Sprintf("persist-to-exact: category=%s samples=%d p50=%s p95=%s p99=%s evidence=%t", name, len(s.byKind[kind]), p[0], p[1], p[2], len(s.byKind[kind]) > 0)
}

// Called after writers/computations join. End includes the allowed drain;
// late sends, missing commit stamps and truncated histories remain incomplete.
func (r *costRunner) samples(start, end time.Time) costSamples {
	channels, topics := map[kernel.ID][]int64{}, map[kernel.ID][]int64{}
	add := func(m costExpectedMessage) {
		channels[m.channel] = append(channels[m.channel], m.sequence)
		topics[m.topic] = append(topics[m.topic], m.sequence)
	}
	for _, m := range r.history {
		add(m)
	}
	for _, change := range r.changesPersisted {
		if change.stream == -1 {
			add(costExpectedMessage{r.subs[0].Channel, r.topic, change.sequence})
		}
	}
	for _, sequences := range []map[kernel.ID][]int64{channels, topics} {
		for _, seqs := range sequences {
			sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
		}
	}
	var result costSamples
	for stream, c := range r.models {
		if c.sentDropped > 0 {
			result.overflowedStreams++
		}
		var exact []costSentModel
		for _, sent := range c.sent {
			model := hybridClone(r.initial[stream])
			floor := r.members[stream].Member.JoinedEventSeq
			count := func(seqs []int64, read int64) sidebarOracleCount {
				through := max(floor, read)
				hi := sort.Search(len(seqs), func(i int) bool { return seqs[i] > sent.sequence })
				lo := sort.Search(len(seqs), func(i int) bool { return seqs[i] > through })
				return sidebarOracleDisplay(int64(max(0, hi-lo)))
			}
			for i := range model.channels {
				read := int64(0)
				if model.channels[i].id == model.current {
					read = sent.readThrough
				}
				model.channels[i].count = count(channels[model.channels[i].id], read)
			}
			for _, entries := range [][]sidebarOracleEntry{model.topics, model.selection} {
				for i := range entries {
					entries[i].count = count(topics[entries[i].id], sent.readThrough)
				}
			}
			if sent.digest == costModelDigest(model) && !c.sentStart.Add(sent.elapsed).After(end) {
				exact = append(exact, sent)
			}
		}
		for _, change := range r.changesPersisted {
			if change.stream != -1 && change.stream != stream || !change.persisted.IsZero() && change.persisted.Before(start) {
				continue
			}
			result.total++
			// Successful frames have monotonic snapshot coverage and send times.
			i := sort.Search(len(exact), func(i int) bool {
				return exact[i].sequence >= max(change.sequence, change.readThrough) && exact[i].readThrough >= change.readThrough && !c.sentStart.Add(exact[i].elapsed).Before(change.persisted)
			})
			if c.sentDropped > 0 || change.persisted.IsZero() || i == len(exact) || exact[i].kind > costPeriodic {
				result.incomplete++
				continue
			}
			sent := exact[i]
			result.byKind[sent.kind] = append(result.byKind[sent.kind], c.sentStart.Add(sent.elapsed).Sub(change.persisted))
		}
	}
	return result
}

func (s costSamples) String() string {
	return fmt.Sprintf("total=%d incomplete=%d overflowed=%d normal=%v delta=%v periodic=%v", s.total, s.incomplete, s.overflowedStreams, s.byKind[costNormal], s.byKind[costDelta], s.byKind[costPeriodic])
}
