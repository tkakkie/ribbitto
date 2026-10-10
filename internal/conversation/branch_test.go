package conversation_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
)

func TestBrancherValidates(t *testing.T) {
	from, to := kernel.ID{1}, kernel.ID{2}
	many := make([]kernel.ID, conversation.MaxBranchMessages+1)
	for i := range many {
		many[i] = kernel.ID{byte(i), 0xff}
	}
	for _, tt := range []struct {
		name string
		b    conversation.Branch
		want error
	}{
		{"no messages", conversation.Branch{From: from, To: &to}, conversation.ErrInvalidBranch},
		{"too many", conversation.Branch{Messages: many, From: from, To: &to}, conversation.ErrInvalidBranch},
		{"a message twice", conversation.Branch{Messages: []kernel.ID{{5}, {5}}, From: from, To: &to}, conversation.ErrInvalidBranch},
		{"two destinations", conversation.Branch{Messages: []kernel.ID{{5}}, From: from, To: &to, NewName: "x"}, conversation.ErrInvalidBranch},
		{"source as destination", conversation.Branch{Messages: []kernel.ID{{5}}, From: from, To: &from}, conversation.ErrInvalidBranch},
		{"blank new name", conversation.Branch{Messages: []kernel.ID{{5}}, From: from, NewName: " "}, conversation.ErrInvalidTopicName},
		{"long new name", conversation.Branch{Messages: []kernel.ID{{5}}, From: from, NewName: strings.Repeat("x", 81)}, conversation.ErrInvalidTopicName},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &branchFake{postingFake: postingFake{t: t}}
			_, err := f.brancher(nil).Branch(t.Context(), org.Membership{}, kernel.ID{3}, tt.b, func(conversation.Topic) string { return "n" })
			if !errors.Is(err, tt.want) || len(f.calls) != 0 {
				t.Fatalf("Branch = %v (runner called %t), want %v before the runner", err, len(f.calls) != 0, tt.want)
			}
		})
	}
}

func TestBrancherRunsAndRaises(t *testing.T) {
	failure := errors.New("statement or commit failed")
	m := org.Membership{Organization: org.Organization{ID: kernel.ID{1}}, Member: org.Member{ID: kernel.ID{2}, JoinedEventSeq: 11}}
	for _, existing := range []bool{false, true} {
		destination := "create"
		b := conversation.Branch{Messages: []kernel.ID{{5}}, From: kernel.ID{4}, NewName: "  設計 "}
		if existing {
			destination, b.To, b.NewName = "destination", &kernel.ID{9}, ""
		}
		order := []string{"begin", "move sequence", "notice sequence", "source", destination, "move", "moved append", "notice", "notice insert", "previous message", "notice read", "posted append", "commit", "raise"}
		for _, fail := range append([]string{"", "conflict", "nil notifier"}, order[:len(order)-1]...) {
			if fail == "notice" {
				continue
			}
			t.Run(destination+"/"+fail, func(t *testing.T) {
				f := &branchFake{postingFake: postingFake{t: t, fail: fail, err: failure}, short: fail == "conflict"}
				var notifier conversation.Notifier = f
				if fail == "nil notifier" {
					notifier = nil
				}
				got, err := f.brancher(notifier).Branch(t.Context(), m, kernel.ID{3}, b, func(d conversation.Topic) string {
					f.scope(m.Organization.ID, kernel.ID{3})
					if d.ID != (kernel.ID{9}) || d.Name != "設計" {
						t.Fatalf("notice destination: %+v", d)
					}
					_ = f.step("notice")
					return "to " + d.Name
				})
				want, calls := failure, order
				switch fail {
				case "", "nil notifier":
					want = nil
					if got.ID != (kernel.ID{9}) || got.Name != "設計" {
						t.Fatalf("destination: %+v", got)
					}
					if notifier == nil {
						calls = order[:len(order)-1]
					}
				case "conflict":
					want, calls = conversation.ErrBranchConflict, order[:6]
				default:
					for i, step := range order {
						if step == fail {
							calls = order[:i+1]
							break
						}
					}
				}
				if !errors.Is(err, want) || err != nil && got != (conversation.Topic{}) || !reflect.DeepEqual(f.calls, calls) {
					t.Fatalf("Branch = %+v, %v; calls %v; want %v, %v", got, err, f.calls, want, calls)
				}
				if f.active || f.committed != (want == nil) {
					t.Fatalf("transaction active %t, committed %t; want success %t", f.active, f.committed, want == nil)
				}
				if fail == "notice insert" {
					if err == failure {
						t.Fatal("notice error was not wrapped")
					}
					for _, mapped := range []error{conversation.ErrTopicNotFound, conversation.ErrChannelNotFound, org.ErrNotFound, conversation.ErrBranchConflict, conversation.ErrInvalidBranch, conversation.ErrInvalidTopicName, conversation.ErrTopicNameTaken} {
						if errors.Is(err, mapped) {
							t.Fatalf("notice failure mapped to %v", mapped)
						}
					}
				}
			})
		}
	}
}
