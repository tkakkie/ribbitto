package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var implementRefusedNames = []string{
	".github", ".git", ".gitattributes", ".gitmodules", ".agents", ".agent",
	"_agents", "_agent", ".gemini", ".claude", ".codex", ".mcp.json",
	"hooks.json", "plugin.json", "skills.json", "plugins.json", "gemini.md",
	"agents.md", "agents.override.md", "claude.md", "claude.local.md",
}

// Safer-side changes from the Python reference (#1431): absent/non-directory
// roots, any walk/list/Lstat error (except a missing requested prefix), and
// oversized input always deny. Python's os.walk/islink can hide those errors,
// and its truncated input can be valid JSON; Go's cap counts bytes rather
// than Python text characters. Invalid UTF-8, unpaired JSON
// surrogates (even in unused fields), and nonstandard NaN/Infinity deny rather
// than accepting Python's permissive JSON; Go must not replace invalid text
// with a usable path. JSON nesting above 512 and integer tokens above 4300
// digits deny to stay within Python's default recursion/integer limits.
// strings.ToLower can map dotted capital I to ASCII i,
// unlike Python's expanding lower(), so refused names are stricter; path keys
// preserve that expansion to avoid admitting a call Python finds pathless.
// Kelvin sign lower-cases to k in both. Error diagnostics outside the recorded
// cases use Go errors rather than Python exception class names. All 33 recorded
// decisions, reasons and statuses are unchanged. A short stdout write also
// fails even if the writer incorrectly returns no error. Neither guard can
// make the later tool operation atomic with these filesystem checks.
func implementGuard(dir string, stdin io.Reader, stdout, stderr io.Writer) int {
	return implementGuardWithFS(dir, stdin, stdout, stderr, filepath.WalkDir, os.Lstat)
}

func implementGuardWithFS(dir string, stdin io.Reader, stdout, stderr io.Writer,
	walk func(string, fs.WalkDirFunc) error, lstat func(string) (fs.FileInfo, error),
) int {
	reason := implementGuardReason(dir, stdin, walk, lstat)
	decision := struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason,omitempty"`
	}{Decision: "allow"}
	if reason != "" {
		decision.Decision, decision.Reason = "deny", reason
	}
	output, err := json.Marshal(decision)
	if err == nil {
		output = append(output, '\n')
		var n int
		n, err = stdout.Write(output)
		if err == nil && n != len(output) {
			err = io.ErrShortWrite
		}
	}
	if err != nil {
		if reason != "" {
			_, _ = fmt.Fprintf(stderr, "implementer guard: %s\n", reason)
		}
		_, _ = fmt.Fprintf(stderr, "implementer guard: writing the decision: %v\n", err)
		return 1
	}
	if reason != "" {
		_, _ = fmt.Fprintf(stderr, "implementer guard: %s\n", reason)
		return 2
	}
	return 0
}

func implementGuardReason(dir string, stdin io.Reader, walk func(string, fs.WalkDirFunc) error,
	lstat func(string) (fs.FileInfo, error),
) string {
	data, err := os.ReadFile(filepath.Join(dir, "implementer-worktree"))
	if err != nil {
		return fmt.Errorf("guard error: reading worktree: %w", err).Error()
	}
	// Python strip() also includes the four ASCII information separators.
	root := strings.TrimFunc(string(data), func(r rune) bool {
		return unicode.IsSpace(r) || r >= '\x1c' && r <= '\x1f'
	})
	if !utf8.Valid(data) {
		return "worktree path is not a clean real path"
	}
	if err := implementRealDirectory(root, lstat); err != nil {
		switch {
		case errors.Is(err, errImplementRealPath):
			return "worktree path is not a clean real path"
		case errors.Is(err, errImplementDirectory):
			return "guard error: worktree is not a directory"
		default:
			return fmt.Errorf("guard error: checking worktree: %w", err).Error()
		}
	}
	input, err := io.ReadAll(io.LimitReader(stdin, maxGuardInput+1))
	if err != nil {
		return fmt.Errorf("guard error: reading hook input: %w", err).Error()
	}
	if len(input) > maxGuardInput {
		return "hook input too large"
	}
	if !implementGuardUnicode(input) {
		return "guard error: invalid Unicode input"
	}
	if !implementGuardJSONLimits(input) {
		return "guard error: JSON exceeds safe parsing limits"
	}
	paths, reason := implementGuardPaths(input)
	if reason != "" {
		return reason
	}
	errSymlink := errors.New("the worktree holds a symlink")
	err = walk(root, func(path string, _ fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walking worktree: %w", walkErr)
		}
		info, err := lstat(path)
		if err != nil {
			return fmt.Errorf("checking worktree entry: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errSymlink
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errSymlink) {
			return errSymlink.Error()
		}
		return fmt.Errorf("guard error: scanning worktree: %w", err).Error()
	}
	for _, path := range paths {
		if reason := implementGuardPath(root, path, lstat); reason != "" {
			return reason
		}
	}
	return ""
}

func implementGuardPaths(input []byte) ([]string, string) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(input, &envelope); err != nil {
		return nil, "guard error: unreadable hook input"
	}
	var call map[string]json.RawMessage
	if err := json.Unmarshal(envelope["toolCall"], &call); err != nil || call == nil {
		return nil, "guard error: missing or invalid toolCall"
	}
	var name string
	if err := json.Unmarshal(call["name"], &name); err != nil || bytes.Equal(call["name"], []byte("null")) {
		return nil, "tool name is not a string"
	}
	switch name {
	case "view_file", "list_dir", "grep_search", "find_by_name", "write_to_file", "replace_file_content", "multi_replace_file_content":
	default:
		return nil, name + " is not allowed in the implementer trial"
	}
	args := call["args"]
	// These are the false values for which Python uses an empty dictionary.
	if len(args) == 0 {
		args = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(args))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		if err == nil && (token == nil || token == false || token == "" || token == json.Delim('[') && !decoder.More()) {
			return nil, name + " names no path the guard can check"
		}
		// Python also treats numeric zero (including -0 and 0.0) as no args.
		if number, ok := token.(json.Number); ok {
			if value, err := number.Float64(); err == nil && value == 0 {
				return nil, name + " names no path the guard can check"
			}
		}
		return nil, "guard error: args is not an object"
	}
	// Map iteration would change which refusal wins. Duplicate keys retain
	// their first position and last value, as Python dictionaries do.
	var keys []string
	values := make(map[string]any)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, "guard error: unreadable args"
		}
		key, ok := token.(string)
		if !ok {
			return nil, "guard error: invalid argument key"
		}
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, "guard error: unreadable args"
		}
		if _, exists := values[key]; !exists {
			keys = append(keys, key)
		}
		values[key] = value
	}
	var paths []string
	for _, key := range keys {
		lower := strings.ToLower(strings.ReplaceAll(key, "İ", "i\u0307"))
		if !strings.Contains(lower, "path") && !strings.Contains(lower, "file") && !strings.Contains(lower, "dir") && !strings.Contains(lower, "uri") {
			continue
		}
		value := values[key]
		switch value.(type) {
		case nil, bool, json.Number:
			continue
		}
		list, ok := value.([]any)
		if !ok {
			list = []any{value}
		}
		for _, item := range list {
			path, ok := item.(string)
			if !ok {
				return nil, key + " is not a string"
			}
			paths = append(paths, path)
		}
	}
	if len(paths) == 0 {
		return nil, name + " names no path the guard can check"
	}
	return paths, ""
}

func implementGuardPath(root, path string, lstat func(string) (fs.FileInfo, error)) string {
	path = strings.TrimPrefix(path, "file://")
	full := path
	if !filepath.IsAbs(full) {
		full = filepath.Join(root, full)
	}
	// POSIX normpath preserves exactly two leading slashes; Clean does not.
	doubleSlash := strings.HasPrefix(full, "//") && !strings.HasPrefix(full, "///")
	full = filepath.Clean(full)
	if doubleSlash {
		full = "/" + full
	}
	if full != root && !strings.HasPrefix(full, root+string(filepath.Separator)) {
		return path + " leaves the worktree"
	}
	rel, err := filepath.Rel(root, full)
	if err != nil {
		return fmt.Errorf("guard error: making path relative: %w", err).Error()
	}
	cur := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		for _, refused := range implementRefusedNames {
			if strings.ToLower(part) == refused {
				return fmt.Sprintf("%s touches a refused name (%s)", path, part)
			}
		}
		cur = filepath.Join(cur, part)
		info, err := lstat(cur)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("guard error: checking requested path: %w", err).Error()
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return path + " goes through a symlink"
		}
	}
	return ""
}

// encoding/json replaces malformed Unicode with U+FFFD; rejecting it prevents
// a different, valid filename from being checked instead of the caller's text.
func implementGuardUnicode(input []byte) bool {
	if !utf8.Valid(input) {
		return false
	}
	for i := 0; i < len(input); i++ {
		if input[i] != '\\' {
			continue
		}
		i++
		if i+4 >= len(input) || input[i] != 'u' {
			continue
		}
		n, err := strconv.ParseUint(string(input[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if n < 0xd800 || n > 0xdfff {
			continue
		}
		if n > 0xdbff || i+6 >= len(input) || string(input[i+1:i+3]) != `\u` {
			return false
		}
		low, err := strconv.ParseUint(string(input[i+3:i+7]), 16, 16)
		if err != nil || low < 0xdc00 || low > 0xdfff {
			return false
		}
		i += 6
	}
	return true
}

// Go accepts deeper objects and longer integers than the reference Python
// runtime. Keep those inputs denied even when they occur in unused fields.
func implementGuardJSONLimits(input []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	depth := 0
	for {
		token, err := decoder.Token()
		if err != nil {
			// The main decoder supplies diagnostics for malformed JSON.
			return true
		}
		switch value := token.(type) {
		case json.Delim:
			if value == '{' || value == '[' {
				depth++
			} else {
				depth--
			}
			if depth > 512 {
				return false
			}
		case json.Number:
			number := strings.TrimPrefix(string(value), "-")
			if !strings.ContainsAny(number, ".eE") && len(number) > 4300 {
				return false
			}
		}
	}
}
