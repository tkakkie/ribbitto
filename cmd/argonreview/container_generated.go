package main

import (
	"fmt"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	generatedGenerationFailed = 21
	generatedTemplFailed      = 22
	generatedGofmtNames       = 23
	generatedGofmtFailed      = 24
	generatedDiffer           = 25
	generatedDiffFailed       = 26
	generatedDiffLimit        = 16 << 10
	generatedPathLimit        = 100
)

// Keep the summary ahead of output so MCP's tail truncation preserves it.
func generatedVerdict(code int, err error, stdout string, over bool) (verdict, stat, output string) {
	output = cutGeneratedDiff(stdout)
	switch {
	case over:
		return "NOT VERIFIED: output limit exceeded (output cut off)", "", output
	case err != nil:
		return "FAILED", "", output
	case code == 0 && stdout == "":
		return "verified", "", ""
	case code == generatedGenerationFailed:
		return "FAILED: generation failed", "", output
	case code == generatedTemplFailed:
		return "FAILED: formatting", "", output
	case code == generatedGofmtNames:
		names := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
		var listed []string
		for _, name := range names[:min(len(names), 50)] {
			listed = append(listed, cutGeneratedText(name, 200))
		}
		if len(names) > 50 {
			listed = append(listed, fmt.Sprintf("and %d more", len(names)-50))
		}
		return "FAILED: formatting", "", strings.Join(listed, "\n")
	case code == generatedDiffer:
		stat, ok := generatedDiffStat(stdout)
		if !ok {
			stat = "Diff stat: diff was not understood\n"
		}
		return "FAILED: generated files differ", stat, output
	default:
		return "FAILED", fmt.Sprintf("Generated check could not complete (status %d).\n", code), output
	}
}

func cutGeneratedText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	const marker = " [cut]"
	end := limit - len(marker)
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + marker
}

func cutGeneratedDiff(s string) string {
	var out strings.Builder
	cut := false
	for line := range strings.SplitSeq(s, "\n") {
		short := cutGeneratedText(line, 400)
		cut = cut || short != line
		if out.Len()+len(short)+1 > generatedDiffLimit {
			cut = true
			break
		}
		out.WriteString(short)
		out.WriteByte('\n')
	}
	if cut {
		out.WriteString("[diff cut off]\n")
	}
	return out.String()
}

func generatedRelative(s string) bool {
	return s != "" && s != "." && !path.IsAbs(s) && path.Clean(s) == s && s != ".." && !strings.HasPrefix(s, "../") && !strings.ContainsAny(s, "\x00\r\n")
}

func generatedName(s string) (string, bool) {
	if strings.HasPrefix(s, `"`) {
		name, err := strconv.Unquote(s)
		return name, err == nil
	}
	return s, true
}

func generatedDiffPair(line string) (string, bool) {
	body, ok := strings.CutPrefix(line, "diff -ru ")
	if !ok || !strings.HasPrefix(body, `"`) {
		return generatedPair(line, "diff -ru ", " /tmp/work/", "")
	}
	token, err := strconv.QuotedPrefix(body)
	if err != nil {
		return "", false
	}
	old, validOld := generatedName(token)
	rest, separated := strings.CutPrefix(body[len(token):], " ")
	next, validNext := generatedName(rest)
	rel, rooted := strings.CutPrefix(old, "/work/")
	return rel, validOld && separated && validNext && rooted && generatedRelative(rel) && next == "/tmp/work/"+rel
}

// Exact reconstruction permits spaces without guessing where a filename ends.
func generatedPair(line, prefix, middle, suffix string) (string, bool) {
	body, ok := strings.CutPrefix(line, prefix+"/work/")
	if !ok || !strings.HasSuffix(body, suffix) {
		return "", false
	}
	n := len(body) - len(middle) - len(suffix)
	if n <= 0 || n%2 != 0 {
		return "", false
	}
	rel := body[:n/2]
	return rel, generatedRelative(rel) && line == prefix+"/work/"+rel+middle+rel+suffix
}

func generatedRange(s string) (int, bool) {
	for _, c := range s {
		if (c < '0' || c > '9') && c != ',' {
			return 0, false
		}
	}
	start, count, comma := strings.Cut(s, ",")
	n, err := strconv.Atoi(start)
	if err != nil || n < 0 {
		return 0, false
	}
	if !comma {
		return 1, true
	}
	n, err = strconv.Atoi(count)
	return n, err == nil && n >= 0
}

func generatedHunk(line string) (int, int, bool) {
	body, ok := strings.CutPrefix(line, "@@ -")
	if !ok {
		return 0, 0, false
	}
	ranges, tail, ok := strings.Cut(body, " @@")
	if !ok || tail != "" {
		return 0, 0, false
	}
	old, next, ok := strings.Cut(ranges, " +")
	if !ok {
		return 0, 0, false
	}
	a, validA := generatedRange(old)
	b, validB := generatedRange(next)
	return a, b, validA && validB && (a > 0 || b > 0)
}

// generatedStat keeps original path identities so the MCP summary need not parse
// the host's already capped presentation. Totals include unlisted paths.
type generatedStat struct {
	entries               []generatedStatEntry
	paths, added, removed int
}

// generatedStatEntry preserves kind and line totals before host formatting.
type generatedStatEntry struct {
	path, kind     string
	added, removed int
}

// generatedDiffStat preserves the host report contract while exposing the facts
// separately for the unwired summary core.
func generatedDiffStat(data string) (string, bool) {
	stat, ok := parseGeneratedStat(data)
	if !ok {
		return "", false
	}
	var listed []string
	for _, entry := range stat.entries {
		kind := entry.kind
		if kind == "" {
			kind = fmt.Sprintf(" +%d -%d", entry.added, entry.removed)
		}
		listed = append(listed, cutGeneratedText(entry.path, 200)+kind)
	}
	listed = append(listed, fmt.Sprintf("%d paths, +%d -%d", stat.paths, stat.added, stat.removed))
	if stat.paths > generatedPathLimit {
		listed = append(listed, fmt.Sprintf("... %d more paths not listed", stat.paths-generatedPathLimit))
	}
	return strings.Join(listed, "\n") + "\n", true
}

// parseGeneratedStat parses complete hunks before another header: source lines
// may themselves start with --- or +++. Unknown or incomplete input loses the
// entire stat. Retention has the same 100-path cap as the host report.
func parseGeneratedStat(data string) (generatedStat, bool) {
	if data == "" || !strings.HasSuffix(data, "\n") {
		return generatedStat{}, false
	}
	lines := strings.Split(strings.TrimSuffix(data, "\n"), "\n")
	var stat generatedStat
	record := func(rel, kind string, a, r int) {
		stat.paths++
		stat.added += a
		stat.removed += r
		if len(stat.entries) < generatedPathLimit {
			stat.entries = append(stat.entries, generatedStatEntry{rel, kind, a, r})
		}
	}
	for i := 0; i < len(lines); {
		line := lines[i]
		if strings.HasPrefix(line, "Only in ") {
			dir, name, ok := strings.Cut(strings.TrimPrefix(line, "Only in "), ": ")
			kind, root := " (only in the worktree)", "/work"
			if dir == "/tmp/work" || strings.HasPrefix(dir, "/tmp/work/") {
				kind, root = " (new in the copy)", "/tmp/work"
			}
			if !ok || (dir != root && !strings.HasPrefix(dir, root+"/")) || !generatedRelative(name) || strings.Contains(name, "/") {
				return generatedStat{}, false
			}
			rel := strings.TrimPrefix(dir, root)
			rel = strings.TrimPrefix(rel, "/")
			if rel != "" {
				rel += "/"
			}
			rel += name
			if !generatedRelative(rel) {
				return generatedStat{}, false
			}
			record(rel, kind, 0, 0)
			i++
			continue
		}
		rel, header := generatedDiffPair(line)
		if header {
			i++
			if i == len(lines) {
				return generatedStat{}, false
			}
			line = lines[i]
		}
		binary, ok := generatedPair(line, "Binary files ", " and /tmp/work/", " differ")
		if ok {
			if header && rel != binary {
				return generatedStat{}, false
			}
			record(binary, " (binary)", 0, 0)
			i++
			continue
		}
		if !header || i+2 >= len(lines) {
			return generatedStat{}, false
		}
		for _, spec := range []struct{ prefix, root string }{{"--- ", "/work/"}, {"+++ ", "/tmp/work/"}} {
			file, stamp, ok := strings.Cut(strings.TrimPrefix(lines[i], spec.prefix), "\t")
			file, valid := generatedName(file)
			if !strings.HasPrefix(lines[i], spec.prefix) || !ok || !valid || stamp == "" || file != spec.root+rel {
				return generatedStat{}, false
			}
			i++
		}
		a, r, hunks := 0, 0, 0
		for i < len(lines) && strings.HasPrefix(lines[i], "@@ ") {
			old, next, ok := generatedHunk(lines[i])
			if !ok {
				return generatedStat{}, false
			}
			i++
			hunks++
			for old > 0 || next > 0 {
				if i == len(lines) || len(lines[i]) == 0 {
					return generatedStat{}, false
				}
				switch lines[i][0] {
				case ' ':
					old--
					next--
				case '-':
					old--
					r++
				case '+':
					next--
					a++
				default:
					return generatedStat{}, false
				}
				if old < 0 || next < 0 {
					return generatedStat{}, false
				}
				i++
				if i < len(lines) && lines[i] == `\ No newline at end of file` {
					i++
				}
			}
		}
		if hunks == 0 {
			return generatedStat{}, false
		}
		record(rel, "", a, r)
	}
	return stat, true
}
