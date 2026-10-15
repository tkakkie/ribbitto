package postgres_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/conversation/internal/postgres"
	"github.com/tkakkie/ribbitto/internal/kernel"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

func TestPinConstraints(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx, f := t.Context(), newFixtures(t, pool)
	m, err := postgres.NewWriterForTest(pool).InsertNotice(ctx, f.acme, f.general, f.generalTopic, f.alice, "current", 1)
	requireNoError(t, err)
	for _, tc := range []struct {
		pinner           any
		pinTime          bool
		code, constraint string
	}{
		{f.alice, false, "23514", "message_pin_pair_check"},
		{nil, true, "23514", "message_pin_pair_check"},
		{f.bob, true, "23503", "message_pinner_fkey"},
	} {
		_, err := pool.Exec(ctx, "UPDATE message SET pinned_by_member_id = $2, pinned_at = CASE WHEN $3 THEN now() END WHERE id = $1", m.ID, tc.pinner, tc.pinTime)
		if !isConstraint(err, tc.code, tc.constraint) {
			t.Fatalf("%s: %v", tc.constraint, err)
		}
	}
}

func TestPinStorageBoundaries(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx, f := t.Context(), newFixtures(t, pool)
	other := conversationtest.Channel(t, pool, f.acme, "other", false)
	requireNoError(t, platform.InTx(ctx, pool, func(tx platform.Tx) error {
		w := postgres.WriterIn(tx)
		m, err := w.InsertNotice(ctx, f.acme, f.general, f.generalTopic, f.alice, "current", 1)
		requireNoError(t, err)
		count, err := w.CountChannelPins(ctx, f.acme, f.general)
		if err != nil || count != 0 {
			t.Fatalf("unpinned count: %d, %v", count, err)
		}
		requireNoError(t, w.SetMessagePin(ctx, f.acme, f.general, m.ID, f.alice))
		state, err := w.GetMessagePin(ctx, f.acme, f.general, m.ID)
		if err != nil || state.PinnedByMemberID != f.alice || state.PinnedAt.IsZero() {
			t.Fatalf("pinned: %+v, %v", state, err)
		}
		for _, tc := range []struct {
			name                  string
			org, channel, message kernel.ID
			want                  int64
		}{
			{"current", f.acme, f.general, m.ID, 1},
			{"organisation only", f.globex, f.general, m.ID, 0},
			{"channel only", f.acme, other.ID, m.ID, 0},
			{"missing message", f.acme, f.general, kernel.ID{0xee}, 1},
		} {
			t.Run(tc.name, func(t *testing.T) {
				n, err := w.CountChannelPins(ctx, tc.org, tc.channel)
				if err != nil || n != tc.want {
					t.Fatalf("count: %d, %v; want %d", n, err, tc.want)
				}
				if tc.name == "current" {
					return
				}
				_, err = w.GetMessagePin(ctx, tc.org, tc.channel, tc.message)
				for op, err := range map[string]error{
					"inspect": err,
					"set":     w.SetMessagePin(ctx, tc.org, tc.channel, tc.message, f.alice),
					"clear":   w.ClearMessagePin(ctx, tc.org, tc.channel, tc.message),
				} {
					if !errors.Is(err, conversation.ErrMessageNotFound) {
						t.Fatalf("%s: %v", op, err)
					}
				}
			})
		}
		unchanged, err := w.GetMessagePin(ctx, f.acme, f.general, m.ID)
		if err != nil || unchanged != state {
			t.Fatalf("scope changed metadata: %+v, %v", unchanged, err)
		}
		requireNoError(t, w.ClearMessagePin(ctx, f.acme, f.general, m.ID))
		state, err = w.GetMessagePin(ctx, f.acme, f.general, m.ID)
		if err != nil || state != (conversation.PinState{}) {
			t.Fatalf("cleared: %+v, %v", state, err)
		}
		return nil
	}))
	var newest kernel.ID
	requireNoError(t, platform.InTx(ctx, pool, func(tx platform.Tx) error {
		w := postgres.WriterIn(tx)
		for i := 0; i <= conversation.MaxChannelPins; i++ {
			m, err := w.InsertNotice(ctx, f.acme, f.general, f.generalTopic, f.alice, "current", int64(i+2))
			requireNoError(t, err)
			requireNoError(t, w.SetMessagePin(ctx, f.acme, f.general, m.ID, f.alice))
			newest = m.ID
		}
		return nil
	}))
	_, err := pool.Exec(ctx, "UPDATE message SET pinned_at = created_at + CASE WHEN id = $1 THEN interval '1 second' ELSE interval '0 seconds' END WHERE pinned_at IS NOT NULL", newest)
	requireNoError(t, err)
	requireNoError(t, platform.InSnapshot(ctx, pool, func(s platform.Snapshot) error {
		r := postgres.ReadStoreIn(s)
		for _, tc := range []struct {
			org, channel kernel.ID
			want         int
		}{
			{f.acme, f.general, conversation.MaxChannelPins}, {f.globex, f.general, 0}, {f.acme, other.ID, 0},
		} {
			pins, err := r.ListChannelPins(ctx, tc.org, tc.channel)
			if err != nil || len(pins) != tc.want {
				t.Fatalf("list: %d, %v; want %d", len(pins), err, tc.want)
			}
			for i, p := range pins {
				if i == 0 && p.ID != newest || i > 1 && bytes.Compare(pins[i-1].ID[:], p.ID[:]) >= 0 {
					t.Fatalf("pin order: %+v", pins)
				}
			}
		}
		return nil
	}))
}
