package conversation

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestTypedBodyProof(t *testing.T) {
	for _, tc := range []struct {
		body, want string
	}{
		{"abc", "00000003ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
		{"界", "00000003" + "e89b8e5fa2ab010e2bf4d9245b9e0fc2dbd5213500605240ec8a768cbdbd96f2"},
	} {
		t.Run(tc.body, func(t *testing.T) {
			proof := proveTypedBody(tc.body)
			if got := hex.EncodeToString(proof[:]); got != tc.want {
				t.Fatalf("proof %s, want %s", got, tc.want)
			}
		})
	}
	proof := proveTypedBody(strings.Repeat("a", 256))
	if got := hex.EncodeToString(proof[:4]); got != "00000100" {
		t.Fatalf("length is not big-endian: %s", got)
	}
}

func TestSameTypedBody(t *testing.T) {
	const reference = "<@01900000-0000-7000-8000-000000000001>"
	for _, tc := range []struct {
		name, stored, typed string
		format              Format
		proof               bool
		want                bool
	}{
		{"equal", "hello", "hello", FormatChatV1, true, true},
		{"same length", "hello", "world", FormatChatV1, true, false},
		{"case", "hello", "Hello", FormatChatV1, true, false},
		{"raw input", "hello", " hello\r\n", FormatChatV1, true, false},
		{"prefix", "hello", "hello more", FormatChatV1, true, false},
		{"shorter", "hello", "hell", FormatChatV1, true, false},
		{"legacy plain", "hello", "hello", FormatPlainText, false, true},
		{"legacy plain token", reference, reference, FormatPlainText, false, true},
		{"legacy literal", "hello", "hello", FormatChatV1, false, true},
		{"legacy different", "hello", "world", FormatChatV1, false, false},
		{"legacy reference", reference, reference, FormatChatV1, false, false},
		{"legacy code", "`" + reference + "`", "`" + reference + "`", FormatChatV1, false, true},
		{"proven reference", reference, reference, FormatChatV1, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := Message{Body: tc.stored, Format: tc.format}
			if tc.proof {
				m.TypedBodyProof = proveTypedBody(tc.stored)
			}
			if got := m.sameTypedBody(tc.typed); got != tc.want {
				t.Fatalf("sameTypedBody = %t, want %t", got, tc.want)
			}
		})
	}
	normalized, err := ValidateMessageBody(" hello\r\n")
	m := Message{Body: "stored independently", TypedBodyProof: proveTypedBody(normalized)}
	if err != nil || !m.sameTypedBody("hello") {
		t.Fatalf("normalized input differs: %q, %v", normalized, err)
	}
}

func TestTypedBodyPrefix(t *testing.T) {
	const typed = "界 hi @tomoya"
	m := Message{Body: "converted independently", TypedBodyProof: proveTypedBody(typed)}
	for _, tc := range []struct{ body, want string }{
		{typed + " more", typed}, {typed, typed}, {"界 hi @thomas more", ""},
		{"界 hi @TOMOYA more", ""}, {"short", ""}, {m.Body + " more", ""},
	} {
		if got := m.TypedBodyPrefix(tc.body); got != tc.want {
			t.Errorf("prefix(%q) = %q, want %q", tc.body, got, tc.want)
		}
	}
}

func TestIdempotencyKey(t *testing.T) {
	// Distinct bytes across all five 8-4-4-4-12 segments, including hex digits a–f.
	sample := IdempotencyKey{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0x4c, 0xde, 0xaf, 0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd}
	for _, tc := range []struct {
		name  string
		check func(t *testing.T)
	}{
		{
			name: "String is 8-4-4-4-12 lower-case form",
			check: func(t *testing.T) {
				const want = "01234567-89ab-4cde-af01-23456789abcd"
				if got := sample.String(); got != want {
					t.Fatalf("String() = %q, want %q", got, want)
				}
			},
		},
		{
			name: "version nibble is 4",
			check: func(t *testing.T) {
				for range 32 {
					k := NewIdempotencyKey()
					if got := k[6] >> 4; got != 4 {
						t.Fatalf("version nibble = %#x in %s, want 0x4", got, k)
					}
				}
			},
		},
		{
			name: "variant bits are 10",
			check: func(t *testing.T) {
				for range 32 {
					k := NewIdempotencyKey()
					if got := k[8] >> 6; got != 0b10 {
						t.Fatalf("variant bits = %02b in %s, want 10", got, k)
					}
				}
			},
		},
		{
			name: "two keys differ",
			check: func(t *testing.T) {
				first, second := NewIdempotencyKey(), NewIdempotencyKey()
				if first == (IdempotencyKey{}) || second == (IdempotencyKey{}) || first == second {
					t.Fatalf("NewIdempotencyKey() = %s and %s, want distinct non-zero keys", first, second)
				}
			},
		},
	} {
		t.Run(tc.name, tc.check)
	}
}
