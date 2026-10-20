package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestVerifyArguments(t *testing.T) {
	for _, raw := range []string{
		`{"check":"test","package":"./..."}`, `{"check":"build","package":"."}`,
		`{"check":"vet","package":"./cmd/argonreview"}`, `{"check":"test","package":"./internal/web/..."}`,
		`{"check":"te\u0073t","pack\u0061ge":"./..."}`,
		`{"check":"test","package":"./` + strings.Repeat("a", 198) + `"}`,
	} {
		if _, err := parseVerifyArguments([]byte(raw)); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
	}
	for _, raw := range []string{
		`null`, `[]`, `"object"`, `{}`, `{"check":"test"}`, `{"package":"."}`,
		`{"check":null,"package":"."}`, `{"check":1,"package":"."}`, `{"check":true,"package":"."}`,
		`{"check":[],"package":"."}`, `{"check":{},"package":"."}`,
		`{"check":"test","package":null}`, `{"check":"test","package":1}`, `{"check":"test","package":true}`,
		`{"check":"test","package":[]}`, `{"check":"test","package":{}}`,
		`{"Check":"test","package":"."}`, `{"check":"test","Package":"."}`,
		`{"check":"test","package":".","extra":"x"}`, `{"check":"test","check":"build","package":"."}`,
		`{"check":"test","ch\u0065ck":"build","package":"."}`, `{"check":"test","package":".","package":"./..."}`,
		`{"check":"test","package":"."} {}`, `{"check":"` + strings.Repeat("x", 201) + `","package":"."}`,
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := parseVerifyArguments([]byte(raw)); err == nil {
				t.Fatal("accepted invalid arguments")
			}
		})
	}
	for _, check := range []string{"bench", "shell", "-x", ""} {
		raw, _ := json.Marshal(map[string]string{"check": check, "package": "."})
		if _, err := parseVerifyArguments(raw); err == nil {
			t.Errorf("accepted check %q", check)
		}
	}
	for _, pattern := range []string{"-x", "/absolute", "../x", "./a/../b", "./..", "./a//b", "./a b", "./a\nb", "./a\x00b", "./a/", "all", "./.../x", "./a;true", "./a\\b", "./é", "./" + strings.Repeat("a", 199), ""} {
		raw, _ := json.Marshal(map[string]string{"check": "test", "package": pattern})
		if _, err := parseVerifyArguments(raw); err == nil {
			t.Errorf("accepted pattern %q", pattern)
		}
	}
	// Escaped characters are validated after decoding, including escapes for unsafe values.
	for _, raw := range []string{`{"check":"test","package":"\u002dflag"}`, `{"check":"test","package":"./a\u002f../b"}`, `{"check":"test","package":"./\u00e9"}`} {
		if _, err := parseVerifyArguments([]byte(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}

func TestVerifySchema(t *testing.T) {
	schema := verifyInputSchema()
	if schema["additionalProperties"] != false {
		t.Fatal("schema must explicitly reject extra properties")
	}
	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Type       string
		Properties map[string]struct {
			Type      string
			Enum      []string
			Pattern   string
			MaxLength int
		}
		Required             []string
		AdditionalProperties bool
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != "object" || len(got.Properties) != 2 || len(got.Required) != 2 || got.AdditionalProperties {
		t.Fatalf("schema: %s", data)
	}
	if strings.Join(got.Required, ",") != "check,package" {
		t.Fatal("required arguments changed")
	}
	checks := got.Properties[verifyCheckProperty]
	if checks.Type != "string" || strings.Join(checks.Enum, ",") != "build,vet,test" {
		t.Fatalf("checks: %+v", checks)
	}
	for _, check := range checks.Enum {
		if err := validateContainerCheck("tree", check, "."); err != nil {
			t.Fatal(err)
		}
	}
	for _, check := range []string{"bench", "shell", "-x", ""} {
		if validateContainerCheck("tree", check, ".") == nil {
			t.Fatal("runner accepts an unlisted check")
		}
	}
	pattern := got.Properties[verifyPackageProperty]
	if pattern.Type != "string" || pattern.Pattern != containerPattern.String() || pattern.MaxLength != 200 || verifyPackageLimit != 200 || verifyToolName != "verify" {
		t.Fatalf("pattern: %+v", pattern)
	}
}
