package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func fixture(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	w := tar.NewWriter(gz)
	for name, body := range files {
		if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestBuild(t *testing.T) {
	const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 20 20"><path d="M1 2&amp;3"/></svg>`
	archive := fixture(t, map[string]string{
		"package/icons/sign_out_20_regular.svg": svg,
		"package/icons/sign_out_20_filled.svg":  strings.Replace(svg, "0 0 20 20", "0 0 24 24", 1),
		"package/icons/unused_20_regular.svg":   svg,
	})
	got, err := build(archive, []string{"sign_out"})
	if err != nil {
		t.Fatal(err)
	}
	want := `<svg xmlns="http://www.w3.org/2000/svg">
  <symbol id="sign-out-regular" viewBox="0 0 20 20" fill="currentColor">
    <path d="M1 2&amp;3" />
  </symbol>
  <symbol id="sign-out-filled" viewBox="0 0 24 24" fill="currentColor">
    <path d="M1 2&amp;3" />
  </symbol>
</svg>
`
	if string(got) != want {
		t.Fatalf("sprite =\n%s\nwant\n%s", got, want)
	}
}

func TestBuildRefuses(t *testing.T) {
	for _, tc := range []struct {
		name, svg, want string
	}{
		{"missing icon", "", "is not in the tarball"},
		{"no viewBox", `<svg xmlns="http://www.w3.org/2000/svg"><path d="M1"/></svg>`, "has no viewBox"},
		{"text content", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1">x<path d="M1"/></svg>`, "text content"},
		{"foreign element", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><x:p xmlns:x="urn:x"/></svg>`, "outside the SVG namespace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{}
			if tc.svg != "" {
				files["package/icons/home_20_regular.svg"] = tc.svg
				files["package/icons/home_20_filled.svg"] = tc.svg
			}
			_, err := build(fixture(t, files), []string{"home"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("build: err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestVerify(t *testing.T) {
	data := []byte("licence text")
	sum := sha256.Sum256(data)
	if err := verify(data, hex.EncodeToString(sum[:])); err != nil {
		t.Fatalf("matching hash refused: %v", err)
	}
	if err := verify(append(data, '!'), hex.EncodeToString(sum[:])); err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("changed data accepted: err = %v", err)
	}
}
