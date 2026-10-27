package view

import (
	"bytes"
	"strings"
	"testing"
)

func TestPageScriptUndeclared(t *testing.T) {
	var out bytes.Buffer
	if err := pageScript("undeclared").Render(t.Context(), &out); err == nil || !strings.Contains(err.Error(), "undeclared page script") {
		t.Fatalf("undeclared rendering: %v, %q", err, out.String())
	}
	if out.Len() != 0 {
		t.Fatalf("undeclared script rendered %q", out.String())
	}
}
