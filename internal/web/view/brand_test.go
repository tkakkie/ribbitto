package view

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestBrandLogoScheme(t *testing.T) {
	source, err := os.ReadFile("../../../web/static/" + strings.TrimPrefix(logoURL, "/static/"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`.brand{fill:#0f766e}`,
		`@media (prefers-color-scheme: dark){.brand{fill:#4cc9b8}}`,
		`<g class="brand">`,
	} {
		if !bytes.Contains(source, []byte(want)) {
			t.Errorf("logo lacks %q", want)
		}
	}

	var body bytes.Buffer
	if err := Hello("", Viewer{}).Render(t.Context(), &body); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<link rel="icon" type="image/svg+xml" href="` + logoURL + `">`,
		`<img src="` + logoURL + `"`,
	} {
		if !strings.Contains(body.String(), want) {
			t.Errorf("welcome page lacks shared logo reference %q", want)
		}
	}
}
