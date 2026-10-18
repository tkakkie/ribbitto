package conversationpg_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationtest"
	"github.com/tkakkie/ribbitto/internal/identity/identitytest"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgtest"
	"github.com/tkakkie/ribbitto/internal/platform/postgres/pgtest"
)

// TestSendIdempotencyKey proves the key's scope and repeats against the
// real unique index: a repeat writes no message, takes no event_seq and
// appends no event, and the key is the author's within the organisation.
func TestSendIdempotencyKey(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	acme := conversationtest.OrganizationWithOwner(t, pool, "acme", conversation.DefaultChannelName)
	globex := conversationtest.OrganizationWithOwner(t, pool, "globex", conversation.DefaultChannelName)
	_, err := pool.Exec(ctx, "UPDATE organization SET event_log_boundary_seq = event_seq")
	requireNoError(t, err)
	bob := orgtest.Member(t, pool, acme.OrganizationID, identitytest.Account(t, pool, "bob@example.org", "bob"), org.RoleMember, "bob", 1)
	other := conversationtest.Channel(t, pool, acme.OrganizationID, "other", false)
	topicA := conversationtest.Topic(t, pool, acme.OrganizationID, acme.Channel.ID, "a")
	alice := membership(acme.OrganizationID, acme.MemberID)
	service := newPosting(pool)
	state := func(organizationID kernel.ID) (seq int64, messages int) {
		t.Helper()
		requireNoError(t, pool.QueryRow(ctx, "SELECT o.event_seq, (SELECT count(*) FROM message m WHERE m.organization_id = o.id) FROM organization o WHERE id = $1", organizationID).Scan(&seq, &messages))
		assertEventLog(t, pool, organizationID, 1, seq)
		return seq, messages
	}
	key := conversation.NewIdempotencyKey()
	send := func(m org.Membership, channelID kernel.ID, topicID *kernel.ID, body string) (conversation.Sent, error) {
		return service.Send(ctx, m, channelID, conversation.Send{Topic: topicID, Body: body, Key: &key})
	}

	first, err := send(alice, acme.Channel.ID, &topicA.ID, " hello\r\n")
	if err != nil || first.Repeat || first.Topic != topicA.ID || first.Message.TopicID != topicA.ID {
		t.Fatalf("first send: %+v, %v", first, err)
	}
	var proof []byte
	var stored [16]byte
	requireNoError(t, pool.QueryRow(ctx, "SELECT idempotency_key, typed_body_proof FROM message WHERE id = $1", first.Message.ID).Scan(&stored, &proof))
	if stored != key {
		t.Fatal("the key was not stored")
	}
	digest := sha256.Sum256([]byte("hello"))
	if len(proof) != 36 || binary.BigEndian.Uint32(proof[:4]) != 5 || !bytes.Equal(proof[4:], digest[:]) || !bytes.Equal(first.Message.TypedBodyProof[:], proof) {
		t.Fatalf("proof not persisted and read back: %x, %x", proof, first.Message.TypedBodyProof)
	}
	read, err := conversationpg.NewReader(pool, lookupMembers, lookupAccounts, eventCursor, emptyPageCountsIn).One(ctx, alice, acme.Channel.ID, first.Message.EventSeq)
	requireNoError(t, err)
	if read.TypedBodyProof != first.Message.TypedBodyProof {
		t.Fatal("history read lost proof")
	}
	seq, messages := state(acme.OrganizationID)

	// The same body after normalisation is a repeat.
	again, err := send(alice, acme.Channel.ID, &topicA.ID, " hello\r\n")
	if err != nil || !again.Repeat || again.Message != first.Message || again.Topic != topicA.ID {
		t.Fatalf("repeat: %+v, %v", again, err)
	}
	for name, body := range map[string]string{"another body": "changed", "same length": "world", "case": "Hello", "prefix": "hello more", "another channel": "hello"} {
		channelID := acme.Channel.ID
		if name == "another channel" {
			channelID = other.ID
		}
		reused, err := send(alice, channelID, nil, body)
		if !errors.Is(err, conversation.ErrPostKeyReused) || reused.Message != first.Message || !reused.Repeat {
			t.Fatalf("%s: %+v, %v", name, reused, err)
		}
	}
	if s, n := state(acme.OrganizationID); s != seq || n != messages {
		t.Fatalf("repeats wrote: event_seq %d→%d, messages %d→%d", seq, s, messages, n)
	}

	// The key is scoped to the author and the organisation: anyone else
	// posts their own message with it and never sees the first.
	for name, m := range map[string]org.Membership{
		"another member":       membership(acme.OrganizationID, bob),
		"another organisation": membership(globex.OrganizationID, globex.MemberID),
	} {
		channelID := acme.Channel.ID
		if m.Organization.ID == globex.OrganizationID {
			channelID = globex.Channel.ID
		}
		own, err := send(m, channelID, nil, "hello")
		if err != nil || own.Repeat || own.Message.ID == first.Message.ID || own.Message.MemberID != m.Member.ID {
			t.Fatalf("%s: %+v, %v", name, own, err)
		}
	}

	// Branching moves the stored message from A to B. A repeat on A returns
	// the moved message with the requested topic A, and the next post, with
	// a fresh key, lands in A.
	moved, err := conversationpg.NewBrancher(pool, eventSequence, appendEvents, branchReads, nil).Branch(ctx, alice, acme.Channel.ID,
		conversation.Branch{Messages: []kernel.ID{first.Message.ID}, From: topicA.ID, NewName: "b"}, func(conversation.Topic) string { return "moved" })
	requireNoError(t, err)
	repeat, err := send(alice, acme.Channel.ID, &topicA.ID, "hello")
	if err != nil || !repeat.Repeat || repeat.Message.ID != first.Message.ID || repeat.Message.TopicID != moved.ID || repeat.Topic != topicA.ID {
		t.Fatalf("repeat after the move: %+v, %v", repeat, err)
	}
	key = conversation.NewIdempotencyKey()
	next, err := send(alice, acme.Channel.ID, &topicA.ID, "next")
	if err != nil || next.Repeat || next.Message.TopicID != topicA.ID {
		t.Fatalf("next send after the move: %+v, %v", next, err)
	}
	// Branching's notice and unkeyed posts keep NULL.
	_, err = service.Post(ctx, alice, acme.Channel.ID, "unkeyed")
	requireNoError(t, err)
	var unexpected int
	requireNoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM message WHERE organization_id = $1 AND idempotency_key IS NULL AND typed_body_proof IS NOT NULL", acme.OrganizationID).Scan(&unexpected))
	if unexpected != 0 {
		t.Fatal("unkeyed post or notice stored a proof")
	}
	var keyed, unkeyed int
	requireNoError(t, pool.QueryRow(ctx, "SELECT count(idempotency_key), count(*) - count(idempotency_key) FROM message WHERE organization_id = $1", acme.OrganizationID).Scan(&keyed, &unkeyed))
	if keyed != 3 || unkeyed != 2 {
		t.Fatalf("keyed rows %d, unkeyed %d; want 3 (alice twice, bob) and two unkeyed rows", keyed, unkeyed)
	}
}

// TestSendIdempotencyKeyConcurrent sends one key at the same time: the
// organisation lock serialises the sends and the index keeps one row.
func TestSendIdempotencyKeyConcurrent(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx := t.Context()
	acme := conversationtest.OrganizationWithOwner(t, pool, "acme", conversation.DefaultChannelName)
	_, err := pool.Exec(ctx, "UPDATE organization SET event_log_boundary_seq = event_seq")
	requireNoError(t, err)
	service := newPosting(pool)
	key := conversation.NewIdempotencyKey()
	const sends = 8
	results := make(chan conversation.Sent, sends)
	errs := make(chan error, sends)
	for range sends {
		go func() {
			sent, err := service.Send(ctx, membership(acme.OrganizationID, acme.MemberID), acme.Channel.ID, conversation.Send{Body: "once", Key: &key})
			errs <- err
			results <- sent
		}()
	}
	var firsts int
	var id kernel.ID
	for i := range sends {
		requireNoError(t, <-errs)
		sent := <-results
		if !sent.Repeat {
			firsts++
		}
		if i > 0 && sent.Message.ID != id {
			t.Fatal("concurrent sends returned different messages")
		}
		id = sent.Message.ID
	}
	var seq int64
	var messages int
	requireNoError(t, pool.QueryRow(ctx, "SELECT o.event_seq, (SELECT count(*) FROM message m WHERE m.organization_id = o.id) FROM organization o WHERE id = $1", acme.OrganizationID).Scan(&seq, &messages))
	assertEventLog(t, pool, acme.OrganizationID, 1, seq)
	if firsts != 1 || messages != 1 || seq != 2 {
		t.Fatalf("first sends %d, messages %d, event_seq %d; want 1, 1, 2", firsts, messages, seq)
	}
}
