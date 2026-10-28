package main

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"testing"
)

// Raw USTAR blocks let hostile names and sizes reach the parser unchanged.
func writebackRawHeader(name string, size int64, kind byte, link, prefix string) []byte {
	b := make([]byte, 512)
	copy(b[:100], name)
	for _, f := range []struct{ start, end int }{{100, 108}, {108, 116}, {116, 124}, {136, 148}, {329, 337}, {337, 345}} {
		copy(b[f.start:f.end], fmt.Sprintf("%0*o", f.end-f.start-1, 0))
	}
	copy(b[124:136], fmt.Sprintf("%011o", size))
	b[156] = kind
	copy(b[157:257], link)
	copy(b[257:263], "ustar\x00")
	copy(b[263:265], "00")
	copy(b[345:500], prefix)
	writebackRawChecksum(b)
	return b
}

func writebackRawChecksum(b []byte) {
	copy(b[148:156], "        ")
	sum := 0
	for _, v := range b {
		sum += int(v)
	}
	copy(b[148:156], fmt.Sprintf("%06o\x00 ", sum))
}

func writebackRawArchive(header, content []byte) []byte {
	b := append([]byte{}, header...)
	b = append(b, content...)
	b = append(b, make([]byte, (512-len(content)%512)%512+1024)...)
	return b
}

func writebackTestArchive(t *testing.T, entries []writebackEntry, format tar.Format, records map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Size: int64(len(e.content)), Mode: 0600, Typeflag: tar.TypeReg, Format: format, PAXRecords: records}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(e.content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writebackPAXArchive(key, value string, content []byte) []byte {
	record := " " + key + "=" + value + "\n"
	length := len(record) + 1
	for len(strconv.Itoa(length))+len(record) != length {
		length = len(strconv.Itoa(length)) + len(record)
	}
	payload := []byte(strconv.Itoa(length) + record)
	b := writebackRawArchive(writebackRawHeader("pax", int64(len(payload)), tar.TypeXHeader, "", ""), payload)
	b = b[:len(b)-1024]
	return append(b, writebackRawArchive(writebackRawHeader("file", int64(len(content)), tar.TypeReg, "", ""), content)...)
}

func writebackPAXOverride() []byte {
	b := writebackPAXArchive("size", "513", bytes.Repeat([]byte{'x'}, 513))
	// The decoded size must override a deliberately different raw size.
	copy(b[1024+124:1024+136], "00000000000\x00")
	writebackRawChecksum(b[1024:1536])
	return b
}

func TestWritebackArchive(t *testing.T) {
	raw := func(name string, kind byte) []byte {
		return writebackRawArchive(writebackRawHeader(name, 0, kind, "", ""), nil)
	}
	entry := func(name string, size int) writebackEntry {
		return writebackEntry{name, bytes.Repeat([]byte{'x'}, size)}
	}
	archive := func(entries ...writebackEntry) []byte {
		return writebackTestArchive(t, entries, tar.FormatUnknown, nil)
	}
	many := func(n, size int) []writebackEntry {
		var entries []writebackEntry
		for i := range n {
			entries = append(entries, entry(fmt.Sprintf("file%d", i), size))
		}
		return entries
	}
	one := archive(entry("a", 4))
	total := many(4, 1<<20)
	v7 := writebackRawHeader("file", 0, tar.TypeReg, "", "")
	clear(v7[257:265])
	writebackRawChecksum(v7)
	pax := writebackPAXArchive("path", "other", nil)
	danglingPAX := append(bytes.Clone(pax[:1024]), make([]byte, 1024)...)
	successivePAX := append(bytes.Clone(pax[:1024]), pax...)
	paxLink := bytes.Clone(pax)
	copy(paxLink[157:257], "link")
	writebackRawChecksum(paxLink[:512])
	longName := strings.Repeat("a", 90) + "/" + strings.Repeat("b", 40)
	prefix := func(name, prefix string) []byte {
		return writebackRawArchive(writebackRawHeader(name, 0, tar.TypeReg, "", prefix), nil)
	}
	for _, tc := range []struct {
		name string
		data []byte
		err  string
		n    int
	}{
		{"normal_order", archive(entry("b", 3), entry("a", 1)), "", 2},
		{"empty_archive", make([]byte, 1024), "", 0},
		{"absolute", raw("/x", tar.TypeReg), "absolute", 0},
		{"parent", raw("../x", tar.TypeReg), "component", 0},
		{"inner_parent", raw("a/../b", tar.TypeReg), "component", 0},
		{"end_parent", raw("a/..", tar.TypeReg), "component", 0},
		{"prefix_inner_parent", prefix("b", "a/.."), "component", 0},
		{"prefix_parent", prefix("x", ".."), "component", 0},
		{"prefix_end_parent", prefix("..", "a/b"), "component", 0},
		{"prefix_absolute", prefix("x", "/abs"), "absolute", 0},
		{"prefix_backslash", prefix("x", "a\\b"), "backslash", 0},
		{"long_USTAR", writebackTestArchive(t, []writebackEntry{entry(longName, 0)}, tar.FormatUSTAR, nil), "", 1},
		{"dot", raw("./a", tar.TypeReg), "component", 0},
		{"empty_component", raw("a//b", tar.TypeReg), "component", 0},
		{"trailing_slash", raw("a/", tar.TypeReg), "component", 0},
		{"empty_name", raw("", tar.TypeReg), "empty name", 0},
		{"backslash", raw("a\\b", tar.TypeReg), "backslash", 0},
		{"raw_NUL", raw("a\x00b", tar.TypeReg), "after NUL", 0},
		{"raw_link_NUL", writebackRawArchive(writebackRawHeader("a", 0, tar.TypeReg, "\x00b", ""), nil), "after NUL", 0},
		{"raw_prefix_NUL", writebackRawArchive(writebackRawHeader("a", 0, tar.TypeReg, "", "\x00b"), nil), "after NUL", 0},
		{"invalid_UTF8", raw("a\xff", tar.TypeReg), "archive format must be USTAR or PAX", 0},
		{"symlink", raw("a", tar.TypeSymlink), "regular file", 0},
		{"hard_link", raw("a", tar.TypeLink), "regular file", 0},
		{"directory", raw("a", tar.TypeDir), "regular file", 0},
		{"char_device", raw("a", tar.TypeChar), "regular file", 0},
		{"block_device", raw("a", tar.TypeBlock), "regular file", 0},
		{"FIFO", raw("a", tar.TypeFifo), "regular file", 0},
		{"GNU_sparse", raw("a", tar.TypeGNUSparse), "raw entry type", 0},
		{"legacy_regular", raw("a", byte(0)), "raw entry type", 0},
		{"unknown_type", raw("a", 'Z'), "regular file", 0},
		{"global_PAX", raw("a", tar.TypeXGlobalHeader), "regular file", 0},
		{"GNU_long_name", writebackTestArchive(t, []writebackEntry{entry(strings.Repeat("a", 120), 0)}, tar.FormatGNU, nil), "format", 0},
		{"GNU_regular", writebackTestArchive(t, []writebackEntry{entry("a", 0)}, tar.FormatGNU, nil), "format", 0},
		{"V7", writebackRawArchive(v7, nil), "format", 0},
		{"regular_link_name", writebackRawArchive(writebackRawHeader("a", 0, tar.TypeReg, "other", ""), nil), "link name", 0},
		{"duplicate", archive(entry("a", 0), entry("a", 0)), "duplicate", 0},
		{"ASCII_duplicate", archive(entry("A", 0), entry("a", 0)), "duplicate", 0},
		{"directory_duplicate", archive(entry("A/b", 0), entry("a/b", 0)), "duplicate", 0},
		{"Kelvin_duplicate", archive(entry("\u212a", 0), entry("k", 0)), "duplicate", 0},
		{"long_s_duplicate", archive(entry("\u017f", 0), entry("s", 0)), "duplicate", 0},
		{"file_at_limit", archive(entry("a", 1<<20)), "", 1},
		{"file_over_limit", writebackRawArchive(writebackRawHeader("a", 1<<20+1, tar.TypeReg, "", ""), nil), "1 MiB", 0},
		{"huge_header_size", writebackRawArchive(writebackRawHeader("a", 1<<30, tar.TypeReg, "", ""), nil), "raw header size", 0},
		{"200_entries", archive(many(200, 0)...), "", 200},
		{"201_entries", archive(many(201, 0)...), "entry count", 0},
		{"payload_at_limit", archive(total...), "", 4},
		{"payload_over_limit", archive(append(total, entry("extra", 1))...), "payload total", 0},
		{"archive_at_limit", make([]byte, writebackArchiveLimit), "padding exceeds", 0},
		{"archive_over_limit", make([]byte, writebackArchiveLimit+1), "byte cap", 0},
		{"short_header", one[:100], "truncated", 0},
		{"short_data", one[:514], "truncated data", 0},
		{"short_padding", one[:520], "truncated", 0},
		{"no_end_blocks", one[:len(one)-1024], "missing two end blocks", 0},
		{"one_end_block", one[:len(one)-512], "missing two end blocks", 0},
		{"zero_padding", append(bytes.Clone(one), make([]byte, writebackPaddingLimit)...), "", 1},
		{"too_much_padding", append(bytes.Clone(one), make([]byte, writebackPaddingLimit+1)...), "padding exceeds", 0},
		{"nonzero_trailing", append(bytes.Clone(one), 1), "non-zero trailing", 0},
		{"second_archive", append(bytes.Clone(one), one...), "non-zero trailing", 0},
		{"PAX_xattr", writebackTestArchive(t, []writebackEntry{entry("a", 0)}, tar.FormatPAX, map[string]string{"SCHILY.xattr.user.test": "x"}), "PAX record", 0},
		{"PAX_path_only", writebackPAXArchive("path", "other", []byte("data")), "", 1},
		{"PAX_path_invalid_UTF8", writebackPAXArchive("path", "a\xff", nil), "not valid UTF-8", 0},
		{"PAX_path_backslash", writebackPAXArchive("path", "a\\b", nil), "backslash in name", 0},
		{"PAX_size_only", writebackPAXArchive("size", "4", []byte("data")), "", 1},
		{"PAX_size_override", writebackPAXOverride(), "", 1},
		{"PAX_dangling", danglingPAX, "PAX header without an entry", 0},
		{"PAX_successive", successivePAX, "successive PAX", 0},
		{"PAX_header_link", paxLink, "PAX header link name", 0},
		{"PAX_mtime", writebackPAXArchive("mtime", "1", nil), "PAX record", 0},
		{"PAX_linkpath", writebackPAXArchive("linkpath", "x", nil), "link name", 0},
		{"PAX_NUL", writebackPAXArchive("path", "a\x00b", nil), "archive/tar: invalid tar header", 0},
		{"missing_generator_header_is_parser_only", archive(entry("a", 0)), "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseWritebackArchive(bytes.NewReader(tc.data))
			if tc.err != "" || tc.n < 0 {
				if err == nil || got != nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("got %d entries, error %v; want whole-archive refusal containing %q", len(got), err, tc.err)
				}
				return
			}
			if err != nil || len(got) != tc.n {
				t.Fatalf("got %d entries, error %v; want %d", len(got), err, tc.n)
			}
			if tc.name == "normal_order" && (got[0].name != "b" || string(got[0].content) != "xxx" || got[1].name != "a" || string(got[1].content) != "x") {
				t.Fatalf("order/content changed: %+v", got)
			}
			if tc.name == "PAX_path_only" && (got[0].name != "other" || string(got[0].content) != "data") {
				t.Fatalf("PAX override lost: %+v", got)
			}
			if tc.name == "long_USTAR" && got[0].name != longName {
				t.Fatalf("USTAR prefix/name join changed: got %q, want %q", got[0].name, longName)
			}
		})
	}
}

func TestWritebackName(t *testing.T) {
	for _, tc := range []struct{ name, reason string }{
		{"a", ""}, {"a/b", ""}, {"\u212a", ""},
		{"\xff", "UTF-8"}, {"a\x00b", "NUL"}, {"a\\b", "backslash"},
		{"/a", "absolute"}, {"", "empty name"}, {"a//b", "component"},
		{"a/./b", "component"}, {"a/../b", "component"},
		{"..", "component"}, {"../a", "component"}, {"a/..", "component"},
		{"a/b/..", "component"}, {"/..", "absolute"}, {"..\\a", "backslash"},
		{"a/", "component"}, {"./a", "component"}, {"\x00", "NUL"},
		{"a..b", ""}, {"..a", ""}, {"a../b", ""}, {"...", ""},
	} {
		t.Run(strconv.Quote(tc.name), func(t *testing.T) {
			err := validateWritebackName(tc.name)
			if tc.reason == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.reason) || !strings.Contains(err.Error(), strconv.Quote(tc.name)) {
				t.Fatalf("unsafe/missing error: %v", err)
			}
		})
	}
}

func TestWritebackNameCleanUnreachable(t *testing.T) {
	// Earlier refusals must explain every name Clean would change.
	var enumerate func(string)
	enumerate = func(name string) {
		err := validateWritebackName(name)
		if err != nil {
			if strings.Contains(err.Error(), "path.Clean") {
				t.Errorf("%q reached path.Clean refusal: %v", name, err)
			}
		} else {
			if name == "" || strings.HasPrefix(name, "/") || path.Clean(name) != name {
				t.Errorf("accepted unclean name %q", name)
			}
			for _, component := range strings.Split(name, "/") {
				if component == "" || component == "." || component == ".." {
					t.Errorf("accepted invalid component in %q", name)
				}
			}
		}
		if len(name) < 8 {
			for _, c := range []string{"a", ".", "/"} {
				enumerate(name + c)
			}
		}
	}
	enumerate("")
}

type writebackErrorReader struct{}

func (writebackErrorReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestWritebackArchiveReadError(t *testing.T) {
	entries, err := parseWritebackArchive(writebackErrorReader{})
	if entries != nil || err == nil || !strings.Contains(err.Error(), "reading generated archive: read failed") {
		t.Fatalf("got %v, %v", entries, err)
	}
}

func TestWritebackReportName(t *testing.T) {
	name := "a\n\x1b" + strings.Repeat("\u212a", 300)
	err := writebackNameError(name, "refused").Error()
	if strings.ContainsAny(err, "\n\x1b") || !strings.Contains(err, `a\n\x1b`) || len(err) > 220 {
		t.Fatalf("unbounded or unescaped report: %q", err)
	}
}

func TestWritebackTarShortReads(t *testing.T) {
	data := writebackTestArchive(t, []writebackEntry{{"a", []byte("data")}}, tar.FormatUSTAR, nil)
	entries, err := parseWritebackArchive(iotestWritebackReader{bytes.NewReader(data)})
	if err != nil || len(entries) != 1 || string(entries[0].content) != "data" {
		t.Fatalf("got %v, %v", entries, err)
	}
}

type iotestWritebackReader struct{ io.Reader }

func (r iotestWritebackReader) Read(p []byte) (int, error) { return r.Reader.Read(p[:min(len(p), 1)]) }

func TestWritebackHeaderRecords(t *testing.T) {
	for _, tc := range []struct {
		key, value string
		accept     bool
	}{
		{"path", "file", true}, {"size", "0", true},
		{"xattr", "x", false}, {"SCHILY.xattr.user.test", "x", false},
		{"mtime", "1", false}, {"GNU.sparse.major", "1", false},
		{"linkpath", "file", false}, {"uid", "1", false}, {"gid", "1", false},
		{"uname", "name", false}, {"gname", "name", false},
	} {
		t.Run(tc.key, func(t *testing.T) {
			h := &tar.Header{Name: "file", Typeflag: tar.TypeReg, Format: tar.FormatPAX, PAXRecords: map[string]string{tc.key: tc.value}}
			err := validateWritebackHeader(h)
			if (err == nil) != tc.accept {
				t.Fatalf("accept %v, error %v", tc.accept, err)
			}
		})
	}
}

func TestWritebackArchiveReadCap(t *testing.T) {
	r := bytes.NewReader(make([]byte, writebackArchiveLimit+20))
	entries, err := parseWritebackArchive(r)
	if entries != nil || err == nil || !strings.Contains(err.Error(), "byte cap") || r.Len() != 19 {
		t.Fatalf("entries %v, error %v, unread bytes %d", entries, err, r.Len())
	}
}

func TestWritebackRawHeader(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind byte
	}{
		{"legacy_regular", 0}, {"GNU_sparse", tar.TypeGNUSparse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &writebackTarReader{r: bytes.NewReader(writebackRawHeader("file", 0, tc.kind, "", ""))}
			if _, err := r.Read(make([]byte, 512)); err == nil || !strings.Contains(err.Error(), "raw entry type") {
				t.Fatalf("raw type escaped inspection: %v", err)
			}
		})
	}
}
