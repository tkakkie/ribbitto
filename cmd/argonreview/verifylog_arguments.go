package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"
)

type verifyLogArguments struct {
	run, stream, name string
	start             int
}

// parseVerifyLogArguments is stateless so the future guard can share exactly
// this syntax boundary. Existence, ambiguity and line ranges belong to reads.
func parseVerifyLogArguments(raw []byte) (verifyLogArguments, error) {
	bad := errors.New("malformed verify-log arguments")
	a := verifyLogArguments{}
	if !utf8.Valid(raw) {
		return a, bad
	}
	fields, err := verifyObject(raw)
	if err != nil {
		return a, bad
	}
	var start string
	for key, raw := range fields {
		var target *string
		switch key {
		case "run":
			target = &a.run
		case "stream":
			target = &a.stream
		case "name":
			target = &a.name
		case "start":
			target = &start
		default:
			return a, bad
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, target) != nil {
			return a, bad
		}
	}
	_, named := fields["name"]
	wantName := a.stream == "test" || a.stream == "package"
	if len(fields) != 3+boolVerifyLog(wantName) || named != wantName || !verifyLogRunID(a.run) || verifyLogStream(a.stream) < 0 || !verifyLogDigits(start, verifyLogStartDigits) {
		return a, bad
	}
	if named {
		if a.name == "" || len(a.name) > verifyLogNameBytes || !utf8.ValidString(a.name) {
			return a, bad
		}
		if strings.HasPrefix(a.name, "#") {
			if !verifyLogDigits(a.name[1:], verifyLogOrdinalDigits) {
				return a, bad
			}
		} else {
			for _, r := range a.name {
				if verifyLogInvisible(r) {
					return a, bad
				}
			}
		}
	}
	a.start, _ = strconv.Atoi(start)
	return a, nil
}

func boolVerifyLog(b bool) int {
	if b {
		return 1
	}
	return 0
}

func verifyLogDigits(s string, limit int) bool {
	if len(s) == 0 || len(s) > limit || s[0] == '0' {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func verifyLogRunID(id string) bool {
	if len(id) != verifyLogIDBytes*2 {
		return false
	}
	for _, c := range id {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}

func verifyLogStream(s string) int {
	switch s {
	case "index":
		return 0
	case "test":
		return 1
	case "package":
		return 2
	case "stderr":
		return 3
	case "output":
		return 4
	}
	return -1
}
