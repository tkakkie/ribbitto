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
	"unicode/utf8"
)

const (
	writebackEntryLimit   = 200
	writebackFileLimit    = 1 << 20
	writebackPayloadLimit = 4 << 20
	// Accept at most one extra GNU tar record of zero padding after the marker.
	writebackPaddingLimit = 10240
	// Reserve headers/PAX metadata and block padding separately from payloads.
	writebackArchiveLimit = writebackPayloadLimit + writebackEntryLimit*1536 + 1024 + writebackPaddingLimit
	// Part B's runner still caps stdout at containerOutputLimit (4 MiB).
)

type writebackEntry struct {
	name    string
	content []byte
}

// No entries escape until every header, payload and trailing byte is validated.
func parseWritebackArchive(r io.Reader) ([]writebackEntry, error) {
	data, err := io.ReadAll(io.LimitReader(r, writebackArchiveLimit+1))
	if err != nil {
		return nil, fmt.Errorf("reading generated archive: %w", err)
	}
	if len(data) > writebackArchiveLimit {
		return nil, errors.New("archive byte cap exceeded")
	}
	counted := &writebackTarReader{r: bytes.NewReader(data)}
	tr := tar.NewReader(counted)
	var entries []writebackEntry
	var total int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			if err := counted.finish(); err != nil {
				return nil, err
			}
			return entries, nil
		}
		if err != nil {
			return nil, fmt.Errorf("reading archive header (invalid or truncated): %w", err)
		}
		if err := validateWritebackHeader(h); err != nil {
			return nil, err
		}
		if len(entries) >= writebackEntryLimit {
			return nil, errors.New("archive entry count exceeds 200")
		}
		total += h.Size
		if total > writebackPayloadLimit {
			return nil, errors.New("archive payload total exceeds 4 MiB")
		}
		for _, entry := range entries {
			// EqualFold uses Unicode SimpleFold, including Kelvin sign and long s;
			// lowercasing alone misses folds. NFC/NFD equivalence is out of scope.
			if strings.EqualFold(entry.name, h.Name) {
				return nil, writebackNameError(h.Name, "duplicate archive name (case folded)")
			}
		}
		// PAX size overrides the raw header size; use the decoded size before
		// any payload reads, so payload bytes never become raw header fields.
		counted.remaining = (h.Size + 511) / 512 * 512
		content := make([]byte, int(h.Size))
		if _, err := io.ReadFull(tr, content); err != nil {
			return nil, fmt.Errorf("reading entry %s (truncated data): %w", writebackQuotedName(h.Name), err)
		}
		entries = append(entries, writebackEntry{h.Name, content})
	}
}

func validateWritebackHeader(h *tar.Header) error {
	if h.Typeflag != tar.TypeReg {
		return writebackNameError(h.Name, "entry type is not a regular file")
	}
	if h.Linkname != "" {
		return writebackNameError(h.Name, "non-empty link name")
	}
	// Reader.next intersects all headers' formats, including invisible GNU L/K
	// metadata. Part B's producer must use tar --format=ustar: no PAX records
	// other than path/size may be emitted, so ustar is the supported format;
	// FormatPAX stays accepted because the reader reports it for path/size-only PAX.
	if h.Format != tar.FormatUSTAR && h.Format != tar.FormatPAX {
		return writebackNameError(h.Name, "archive format must be USTAR or PAX")
	}
	// Reader.mergePAX retains ALL records, including consumed path and size.
	// Permit only those two: timestamps, xattrs and sparse metadata are refused.
	for key := range h.PAXRecords {
		if key != "path" && key != "size" {
			return writebackNameError(h.Name, "unsupported PAX record")
		}
	}
	if h.Size < 0 || h.Size > writebackFileLimit {
		return writebackNameError(h.Name, "entry size exceeds 1 MiB")
	}
	return validateWritebackName(h.Name)
}

func validateWritebackName(name string) error {
	switch {
	case !utf8.ValidString(name):
		return writebackNameError(name, "name is not valid UTF-8")
	case strings.ContainsRune(name, 0):
		return writebackNameError(name, "NUL in name")
	case strings.ContainsRune(name, '\\'):
		return writebackNameError(name, "backslash in name")
	case strings.HasPrefix(name, "/"):
		return writebackNameError(name, "absolute name")
	case name == "":
		return writebackNameError(name, "empty name")
	}
	for _, component := range strings.Split(name, "/") {
		if component == "" || component == "." || component == ".." {
			return writebackNameError(name, "empty, dot or dotdot path component")
		}
	}
	if path.Clean(name) != name {
		return writebackNameError(name, "name differs from path.Clean")
	}
	return nil
}

func writebackQuotedName(name string) string {
	return cutGeneratedText(strconv.Quote(name), 200)
}

func writebackNameError(name, reason string) error {
	return fmt.Errorf("entry %s: %s", writebackQuotedName(name), reason)
}

// This wrapper deliberately exposes no Seeker. It observes even headers that
// tar.Reader consumes internally, and distinguishes end blocks from zero data.
type writebackTarReader struct {
	r          io.Reader
	block      [512]byte
	pending    []byte
	remaining  int64
	consumed   int64
	endBlocks  int
	paxPending bool
}

func (r *writebackTarReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(r.pending) == 0 && r.remaining == 0 {
		if err := r.header(); err != nil {
			return 0, err
		}
	}
	if len(r.pending) != 0 {
		n := copy(p, r.pending)
		r.pending = r.pending[n:]
		r.consumed += int64(n)
		return n, nil
	}
	n, err := r.r.Read(p[:min(int64(len(p)), r.remaining)])
	r.remaining -= int64(n)
	r.consumed += int64(n)
	return n, err
}

func (r *writebackTarReader) header() error {
	if _, err := io.ReadFull(r.r, r.block[:]); err != nil {
		return err
	}
	r.pending = r.block[:]
	if writebackAllZero(r.block[:]) {
		r.endBlocks++
		return nil
	}
	// tar.Reader truncates these fields at NUL. Refuse hidden suffixes before
	// that truncation can turn a hostile name into an apparently safe one.
	for _, field := range [][]byte{r.block[:100], r.block[157:257], r.block[345:500]} {
		if i := bytes.IndexByte(field, 0); i >= 0 && !writebackAllZero(field[i:]) {
			return writebackNameError(string(field), "non-zero bytes after NUL in raw header field")
		}
	}
	// tar.Reader normalizes TypeRegA to TypeReg and may read sparse metadata
	// before returning a header; refuse these raw types before either happens.
	if r.block[156] == byte(0) || r.block[156] == tar.TypeGNUSparse {
		return writebackNameError(string(r.block[:100]), "refused raw entry type")
	}
	// Next overwrites, rather than merges, successive local PAX headers.
	// Refuse that ambiguity so earlier unsupported records cannot disappear.
	if r.block[156] == tar.TypeXHeader {
		if r.paxPending {
			return errors.New("successive PAX headers are not supported")
		}
		if !writebackAllZero(r.block[157:257]) {
			return writebackNameError(string(r.block[:100]), "non-empty PAX header link name")
		}
		r.paxPending = true
	} else {
		r.paxPending = false
	}
	size, err := strconv.ParseInt(strings.Trim(string(r.block[124:136]), " \x00"), 8, 64)
	if err != nil || size < 0 || size > writebackArchiveLimit {
		return writebackNameError(string(r.block[:100]), "invalid or oversized raw header size")
	}
	r.remaining = (size + 511) / 512 * 512
	return nil
}

func (r *writebackTarReader) finish() error {
	if r.endBlocks != 2 || r.consumed < 1024 {
		return errors.New("truncated archive: missing two end blocks")
	}
	if r.paxPending {
		return errors.New("truncated archive: PAX header without an entry")
	}
	padding, err := io.ReadAll(r.r)
	if err != nil {
		return fmt.Errorf("reading archive padding: %w", err)
	}
	if len(padding) > writebackPaddingLimit {
		return errors.New("archive trailing zero padding exceeds 10240 bytes")
	}
	if !writebackAllZero(padding) {
		return errors.New("non-zero trailing archive data")
	}
	return nil
}

func writebackAllZero(data []byte) bool {
	for _, b := range data {
		if b != 0 {
			return false
		}
	}
	return true
}
