package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func verifyLogArgsJSON(run, stream, name, start string) []byte {
	a := map[string]string{"run": run, "stream": stream, "start": start}
	if name != "" {
		a["name"] = name
	}
	b, _ := json.Marshal(a)
	return b
}

func TestVerifyLogArguments(t *testing.T) {
	run := strings.Repeat("a", verifyLogIDBytes*2)
	for _, stream := range []string{"index", "test", "package", "stderr", "output"} {
		name := ""
		if stream == "test" || stream == "package" {
			name = "#1"
		}
		for _, start := range []string{"1", "10", "1234567", strings.Repeat("9", verifyLogStartDigits)} {
			if _, err := parseVerifyLogArguments(verifyLogArgsJSON(run, stream, name, start)); err != nil {
				t.Fatalf("valid %s/%s: %v", stream, start, err)
			}
		}
	}
	for _, start := range []string{"0", "00", "01", "007", "-1", "+1", "1e1", "１", strings.Repeat("9", verifyLogStartDigits+1), ""} {
		t.Run("start="+start, func(t *testing.T) {
			if _, err := parseVerifyLogArguments(verifyLogArgsJSON(run, "index", "", start)); err == nil {
				t.Fatal("bad start accepted")
			}
		})
	}
	for _, name := range []string{"#0", "#01", "#-1", "#99999999", "#１", "#١", "#1 ", strings.Repeat("x", summaryIdentityLimit), "x\x00", "x\x1b[31m", "x\u202e", "x\u200b"} {
		t.Run(fmt.Sprintf("name=%q", name[:min(len(name), 12)]), func(t *testing.T) {
			if _, err := parseVerifyLogArguments(verifyLogArgsJSON(run, "test", name, "1")); err == nil {
				t.Fatal("bad name accepted")
			}
		})
	}
	for _, width := range []int{verifyLogNameBytes, verifyLogNameBytes + 1} {
		_, err := parseVerifyLogArguments(verifyLogArgsJSON(run, "test", strings.Repeat("x", width), "1"))
		if (err == nil) != (width <= verifyLogNameBytes) {
			t.Fatal("name byte boundary")
		}
	}
	for _, width := range []int{verifyLogOrdinalDigits, verifyLogOrdinalDigits + 1} {
		_, err := parseVerifyLogArguments(verifyLogArgsJSON(run, "test", "#"+strings.Repeat("9", width), "1"))
		if (err == nil) != (width <= verifyLogOrdinalDigits) {
			t.Fatal("ordinal digit boundary")
		}
	}
	base := string(verifyLogArgsJSON(run, "index", "", "1"))
	for _, raw := range []string{
		"null", "[]", base + "{}", strings.TrimSuffix(base, "}") + `,"start":"1"}`,
		strings.TrimSuffix(base, "}") + `,"\u0073tart":"1"}`, strings.TrimSuffix(base, "}") + `,"extra":"x"}`,
		strings.Replace(base, `"start":"1"`, `"Start":"1"`, 1), strings.Replace(base, `"start":"1"`, `"start":null`, 1),
		strings.Replace(base, `"start":"1"`, `"start":1`, 1), strings.Replace(base, `"index"`, `"other"`, 1),
		string(verifyLogArgsJSON(run, "index", "unexpected", "1")), string(verifyLogArgsJSON(run, "test", "", "1")),
		strings.Replace(string(verifyLogArgsJSON(run, "test", "empty", "1")), `"empty"`, `""`, 1),
		strings.Replace(base, run, strings.ToUpper(run), 1), strings.Replace(base, run, run+"a", 1),
		strings.Replace(base, "index", "\xff", 1), string(verifyLogArgsJSON(run[:len(run)-1], "index", "", "1")),
		strings.Replace(string(verifyLogArgsJSON(run, "test", "invalid", "1")), "invalid", "bad\xffname", 1),
	} {
		if _, err := parseVerifyLogArguments([]byte(raw)); err == nil {
			t.Fatalf("malformed JSON arguments accepted: %q", raw)
		}
	}
}

func TestVerifyLogParsersSeparate(t *testing.T) {
	for _, check := range verifyCheckNames() {
		raw, _ := json.Marshal(map[string]string{"check": check, "package": "."})
		if _, err := parseVerifyLogArguments(raw); err == nil {
			t.Fatal("verify-log accepted verify arguments")
		}
	}
	if _, err := parseVerifyArguments(verifyLogArgsJSON(strings.Repeat("a", verifyLogIDBytes*2), "test", "#1", "1")); err == nil {
		t.Fatal("verify accepted log arguments")
	}
}
