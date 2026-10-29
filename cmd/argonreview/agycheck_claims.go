package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const agyCheckClaimBytes = 64

type agyCheckClaim struct {
	N         int
	Word, Tag string
}
type agyCheckStubRecord struct {
	N         int
	Kind, Tag string
}

// Records are an ordinary-agent regression check, not tamper resistance: an
// allow-everything agent could write them itself. Missing, contradictory or
// malformed evidence is an error; a match never approves trust of a version.
// The explicit isolation argument prevents callers from reading state elsewhere.
func agyCheckReadRecords(isolation, dir, sub string, cap int, f agyCheckFiles) ([][]byte, error) {
	bad := func() ([][]byte, error) { return nil, errors.New(agyCheckPrintable("invalid call records")) }
	if err := f.path(isolation, dir); err != nil {
		return bad()
	}
	if err := f.private(dir, true); err != nil {
		return bad()
	}
	calls := filepath.Join(dir, sub)
	if err := f.path(dir, calls); err != nil {
		return bad()
	}
	_, err := f.lstat(calls)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || f.private(calls, true) != nil {
		return bad()
	}
	entries, err := f.entries(calls)
	if err != nil || len(entries) > cap {
		return bad()
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var records [][]byte
	for i, entry := range entries {
		if entry.Name() != fmt.Sprintf("%06d", i+1) {
			return bad()
		}
		path := filepath.Join(calls, entry.Name())
		if err := f.private(path, false); err != nil {
			return bad()
		}
		data, err := f.readInside(dir, path, agyCheckClaimBytes)
		if err != nil || len(data) > agyCheckClaimBytes || !strings.HasSuffix(string(data), "\n") {
			return bad()
		}
		records = append(records, data)
	}
	return records, nil
}

func agyCheckDecodeClaims(isolation, dir string, stub bool, f agyCheckFiles) ([]agyCheckClaim, error) {
	records, err := agyCheckReadRecords(isolation, dir, "calls", agyCheckHookCallCap, f)
	if err != nil {
		return nil, err
	}
	bad := func() ([]agyCheckClaim, error) { return nil, errors.New("invalid call records") }
	var claims []agyCheckClaim
	for i, data := range records {
		word, tag, ok := strings.Cut(strings.TrimSuffix(string(data), "\n"), " ")
		if !ok || tag != "-" && !agyCheckHookTag(tag) {
			return bad()
		}
		valid := false
		if stub {
			valid = word == "start" || word == "request" || word == "response"
		} else {
			mode, known := agyCheckHookFindMode(word)
			valid = known && mode.name != "alternate"
		}
		if !valid {
			return bad()
		}
		claims = append(claims, agyCheckClaim{i + 1, word, tag})
	}
	return claims, nil
}

func agyCheckReadClaims(isolation, dir string, f agyCheckFiles) ([]agyCheckClaim, error) {
	return agyCheckDecodeClaims(isolation, dir, false, f)
}

// Stub records are tokens, not structure. B3-2 must require start first and pair
// each response <tag> with the immediately preceding request <tag>. ANY request
// on a denied MCP row is a breach, with or without a response; an unpaired
// request on an allowed row is a difference. Requests precede handling and
// responses follow written reply bytes, so a missing response can only lose
// confirmation. Success must come from the transcript: response does not
// distinguish a result from a JSON-RPC error.
func agyCheckReadStubRecords(isolation, dir string, f agyCheckFiles) ([]agyCheckStubRecord, error) {
	claims, err := agyCheckDecodeClaims(isolation, dir, true, f)
	if err != nil {
		return nil, err
	}
	var records []agyCheckStubRecord
	for _, claim := range claims {
		records = append(records, agyCheckStubRecord{claim.N, claim.Word, claim.Tag})
	}
	return records, nil
}

func agyCheckClaimReplies(claims []agyCheckClaim, tag string) ([]string, error) {
	ordered := append([]agyCheckClaim(nil), claims...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].N < ordered[j].N })
	var replies []string
	for i, claim := range ordered {
		if claim.N != i+1 || claim.Word != "allow" && claim.Word != "deny" {
			return nil, errors.New("invalid decision records")
		}
		if claim.Tag == tag {
			replies = append(replies, claim.Word)
		}
	}
	return replies, nil
}
