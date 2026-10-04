package postgres_test

import (
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/tkakkie/ribbitto/internal/app/member"
	"github.com/tkakkie/ribbitto/internal/app/message"
	"github.com/tkakkie/ribbitto/internal/app/topic"
	"github.com/tkakkie/ribbitto/internal/domain"
	"github.com/tkakkie/ribbitto/internal/infra/postgres/pgtest"
)

// The publishers' encoders replaced SQL's jsonb_build_object (#398). Each
// must produce the same JSON value, so new rows have the stored shape, and
// rows the SQL wrote must still decode. A round trip alone would pass with an
// encoder and decoder that are wrong in the same way.
func TestEventPayloadCompatibility(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	id := func(n byte) domain.ID { return domain.ID{0: 0xab, 6: 0x7c, 8: 0x9d, 15: n} }
	uuid := func(n byte) pgtype.UUID { return pgtype.UUID{Bytes: id(n), Valid: true} }
	posted := message.EncodePosted(id(1), id(2), id(3))
	joined := member.EncodeJoined(id(4))
	moved := topic.EncodeMoved(topic.Moved{ChannelID: id(1), FromTopicID: id(3), ToTopicID: id(5), MessageIDs: []domain.ID{id(2), id(6)}})
	for _, tt := range []struct {
		name    string
		encoded []byte
		sql     string
		args    []any
		decode  func([]byte) (any, error)
		want    any
	}{
		{
			"message.posted", posted,
			"SELECT jsonb_build_object('channel_id', $1::uuid, 'message_id', $2::uuid, 'topic_id', $3::uuid)",
			[]any{uuid(1), uuid(2), uuid(3)},
			func(b []byte) (any, error) { return message.DecodePosted(b) },
			message.Posted{ChannelID: id(1), MessageID: id(2), TopicID: &[]domain.ID{id(3)}[0]},
		},
		{
			"legacy message.posted", nil,
			"SELECT jsonb_build_object('channel_id', $1::uuid, 'message_id', $2::uuid)",
			[]any{uuid(1), uuid(2)},
			func(b []byte) (any, error) { return message.DecodePosted(b) },
			message.Posted{ChannelID: id(1), MessageID: id(2)},
		},
		{
			"member.joined", joined,
			"SELECT jsonb_build_object('member_id', $1::uuid)",
			[]any{uuid(4)},
			func(b []byte) (any, error) { return member.DecodeJoined(b) },
			member.Joined{MemberID: id(4)},
		},
		{
			"messages.moved", moved,
			`SELECT jsonb_build_object('channel_id', $1::uuid, 'from_topic_id', $2::uuid,
				'to_topic_id', $3::uuid, 'message_ids', to_jsonb($4::uuid[]))`,
			[]any{uuid(1), uuid(3), uuid(5), []pgtype.UUID{uuid(2), uuid(6)}},
			func(b []byte) (any, error) { return topic.DecodeMoved(b) },
			topic.Moved{ChannelID: id(1), FromTopicID: id(3), ToTopicID: id(5), MessageIDs: []domain.ID{id(2), id(6)}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stored []byte
			requireNoError(t, pool.QueryRow(t.Context(), tt.sql, tt.args...).Scan(&stored))
			got, err := tt.decode(stored)
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("decoding SQL-built %s = %+v, %v; want %+v", stored, got, err, tt.want)
			}
			if tt.encoded == nil {
				return // No writer produces legacy rows any more.
			}
			var equal bool
			requireNoError(t, pool.QueryRow(t.Context(), "SELECT $1::jsonb = $2::jsonb", tt.encoded, stored).Scan(&equal))
			if !equal {
				t.Fatalf("encoder wrote %s; SQL built %s", tt.encoded, stored)
			}
		})
	}
}
