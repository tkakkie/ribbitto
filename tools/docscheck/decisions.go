package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/yuin/goldmark/ast"
)

const decisionsDir = "docs/decisions"

// decisionsSplit reports whether docs/decisions/ exists, enabling both the
// decision checks and the index's size limit. Until then DECISIONS.md is exempt.
func decisionsSplit(root string) (bool, error) {
	info, err := os.Stat(filepath.Join(root, decisionsDir))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking decisions directory: %w", err)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("%s: must be a directory", decisionsDir)
	}
	return true, nil
}

// checkDecisions validates decisions once docs/decisions/ exists. The directory
// contains only regular files named NN-slug.md (01–99; lowercase ASCII words or
// digits separated by hyphens), with # N. Title on the first line. Decision 100
// will require widening the two-digit pattern in a later change.
// DECISIONS.md reserves list items for entries in this one-line format:
//
//   - [N. Title](docs/decisions/NN-slug.md)
//
// Each file must be listed exactly once, and each entry must target an existing
// decision file. File and index numbers are unique; N is unpadded and matches
// the file and heading. Gaps are allowed; titles and slugs are not compared.
// Nested entries are checked; fenced examples do not count as entries.
func checkDecisions(root string) ([]string, error) {
	split, err := decisionsSplit(root)
	if err != nil || !split {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(root, decisionsDir))
	if err != nil {
		return nil, fmt.Errorf("reading decisions directory: %w", err)
	}
	filePattern := regexp.MustCompile(`^([0-9]{2})-[a-z0-9]+(-[a-z0-9]+)*\.md$`)
	headingPattern := regexp.MustCompile(`^# ([1-9][0-9]*)\. \S.*$`)
	indexPattern := regexp.MustCompile(`^- \[([1-9][0-9]*)\. \S[^\]]*\]\((docs/decisions/[^)]+)\)$`)
	files := map[string]int{}
	numbers := map[int]string{}
	var problems []string
	for _, entry := range entries {
		path := decisionsDir + "/" + entry.Name()
		m := filePattern.FindStringSubmatch(entry.Name())
		if !entry.Type().IsRegular() || m == nil || m[1] == "00" {
			problems = append(problems, fmt.Sprintf("%s: want a regular NN-slug.md file (01–99)", path))
			continue
		}
		number, _ := strconv.Atoi(m[1])
		files[path] = number
		if previous, ok := numbers[number]; ok {
			problems = append(problems, fmt.Sprintf("%s: duplicate decision number %d, also in %s", path, number, previous))
		}
		numbers[number] = path
		source, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			return nil, fmt.Errorf("reading decision %s: %w", path, err)
		}
		first, _, _ := strings.Cut(string(source), "\n")
		heading := headingPattern.FindStringSubmatch(strings.TrimSuffix(first, "\r"))
		if heading == nil || heading[1] != strconv.Itoa(number) {
			problems = append(problems, fmt.Sprintf("%s: first line must be # %d. Title, matching the file number", path, number))
		}
	}
	index, err := os.ReadFile(filepath.Join(root, "DECISIONS.md"))
	if os.IsNotExist(err) {
		return append(problems, "DECISIONS.md: decision index does not exist"), nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading decision index: %w", err)
	}
	listed := map[string]bool{}
	indexNumbers := map[string]bool{}
	// Only rendered entries count: a fenced example cannot list a decision.
	d := parse(index)
	lines := strings.Split(string(index), "\n")
	var candidates []int
	_ = ast.Walk(d.root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.ListItem:
			candidates = append(candidates, d.lineOfNode(n))
		case *ast.Link:
			for parent := n.Parent(); parent != nil; parent = parent.Parent() {
				if parent.Kind() == ast.KindListItem {
					return ast.WalkContinue, nil
				}
			}
			if strings.HasPrefix(string(n.Destination), decisionsDir+"/") {
				candidates = append(candidates, d.lineOfNode(n))
			}
		}
		return ast.WalkContinue, nil
	})
	for _, line := range candidates {
		var m []string
		if line > 0 {
			m = indexPattern.FindStringSubmatch(strings.TrimSpace(lines[line-1]))
		}
		if m == nil {
			problems = append(problems, fmt.Sprintf("DECISIONS.md:%d: want - [N. Title](docs/decisions/NN-slug.md)", line))
			continue
		}
		if indexNumbers[m[1]] {
			problems = append(problems, fmt.Sprintf("DECISIONS.md:%d: duplicate index number %s", line, m[1]))
		}
		indexNumbers[m[1]] = true
		if number, ok := files[m[2]]; !ok {
			problems = append(problems, fmt.Sprintf("DECISIONS.md:%d: %s is not an existing decision file", line, m[2]))
		} else if m[1] != strconv.Itoa(number) {
			problems = append(problems, fmt.Sprintf("DECISIONS.md:%d: index number %s differs from file number %d in %s", line, m[1], number, m[2]))
		}
		listed[m[2]] = true
	}
	for path := range files {
		if !listed[path] {
			problems = append(problems, fmt.Sprintf("%s: not listed in DECISIONS.md", path))
		}
	}
	sort.Strings(problems)
	return problems, nil
}
