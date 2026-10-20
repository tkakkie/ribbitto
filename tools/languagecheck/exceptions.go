package main

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
)

func readExceptions(repo *os.Root, tracked map[string]bool) (map[string]bool, []string) {
	admitted := map[string]bool{}
	var problems []string
	if !tracked[exceptionsFile] {
		return admitted, []string{exceptionsFile + ": exceptions file is not tracked; an untracked file cannot approve anything"}
	}
	f, err := openRegular(repo, exceptionsFile)
	if err != nil {
		return admitted, []string{fmt.Errorf("reading %s: %w", exceptionsFile, err).Error()}
	}
	defer func() { _ = f.Close() }()
	linePattern := regexp.MustCompile(`^(.+) #([1-9][0-9]*) (https://github\.com/tkakkie/ribbitto/issues/([1-9][0-9]*)#issuecomment-[0-9]+)( -- (.*))?$`)
	seen := map[string]bool{}
	scanner := bufio.NewScanner(f)
	for n := 1; scanner.Scan(); n++ {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "# ") {
			continue
		}
		report := func(reason string) { problems = append(problems, fmt.Sprintf("%s:%d: %s", exceptionsFile, n, reason)) }
		m := linePattern.FindStringSubmatch(line)
		if m == nil {
			report("want <exact path> #<issue> <approving issue comment URL> [-- <reason>]")
			continue
		}
		name := m[1]
		reason, hasReason := m[6], m[5] != ""
		switch {
		case !validName(strings.TrimSuffix(name, "/")):
			report("invalid exception path")
		case m[2] != m[4]:
			report("approval URL must name the same issue")
		case seen[name]:
			report("duplicate exception path")
		case (strings.ContainsAny(name, "*?[]{}\\") || strings.HasSuffix(name, "/") || directoryPrefix(name, tracked)) && (!hasReason || strings.TrimSpace(reason) == ""):
			report("directory or glob metacharacter needs -- <reason>; exceptions still match one exact tracked file only")
		case hasReason && strings.TrimSpace(reason) == "":
			report("empty exception reason")
		case !tracked[name]:
			report("exception path is not tracked (stale or directory)")
		case allowedKind(name) != "":
			report("exception path is already allowed (stale)")
		default:
			admitted[name] = true
		}
		seen[name] = true
	}
	if err := scanner.Err(); err != nil {
		problems = append(problems, fmt.Errorf("reading %s: %w", exceptionsFile, err).Error())
	}
	return admitted, problems
}

func directoryPrefix(name string, tracked map[string]bool) bool {
	for candidate := range tracked {
		if strings.HasPrefix(candidate, strings.TrimSuffix(name, "/")+"/") {
			return true
		}
	}
	return false
}
