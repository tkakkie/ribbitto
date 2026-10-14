package conversation

import (
	"context"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// WitnessRange is a persisted member range, not an effective count gap.
type WitnessRange struct {
	ChannelID kernel.ID
	Lo, Hi    int64
}

// SidebarWitness is versioned member-specific evidence; absent floors stay absent.
type SidebarWitness struct {
	Version                             int
	OrganizationID, MemberID, ChannelID kernel.ID
	Selected                            *kernel.ID
	EventSeq, JoinedEventSeq            int64
	Ranges                              []WitnessRange
	Floors                              map[kernel.ID]int64
}

// WitnessIn binds optional capture to the existing snapshot and validated scope.
// Implementations must not log state or errors or read through another transaction.
type WitnessIn func(context.Context, platform.Snapshot, org.Membership, kernel.ID, []kernel.ID, []kernel.ID) ([]WitnessRange, map[kernel.ID]int64, error)

// WithWitness returns a configured copy; the ordinary reader stays disabled.
// Install only in disposable measurement composition; no runtime switch exists here.
func (s *Reader) WithWitness(capture WitnessIn) *Reader {
	copy := *s
	copy.witness = capture
	return &copy
}

func (s *Reader) capture(ctx context.Context, snapshot platform.Snapshot, m org.Membership, channelID kernel.ID, channels, topics []kernel.ID, selected *kernel.ID) *SidebarWitness {
	if s.witness == nil {
		return nil
	}
	w := &SidebarWitness{Version: 1, OrganizationID: m.Organization.ID, MemberID: m.Member.ID, ChannelID: channelID, Selected: selected, JoinedEventSeq: m.Member.JoinedEventSeq}
	channels = append(append([]kernel.ID(nil), channels...), channelID)
	topics = append([]kernel.ID(nil), topics...)
	if selected != nil {
		topics = append(topics, *selected)
	}
	if !platform.CaptureSnapshot(ctx, snapshot, func(bound platform.Snapshot) (err error) {
		w.EventSeq, err = s.cursor(bound).EventSeq(ctx, m.Organization.ID)
		if err != nil {
			return err
		}
		w.Ranges, w.Floors, err = s.witness(ctx, bound, m, channelID, channels, topics)
		return err
	}) {
		return nil
	}
	return w
}
