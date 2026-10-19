package main

import (
	"bufio"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode"
)

// This deliberately small scan is not a Make, YAML or shell parser. Its
// supported positions and remaining review obligations are in decision 42.
func checkCommands(name string, source io.Reader) []string {
	var problems []string
	scanner := bufio.NewScanner(source)
	runIndent := -1
	type mapping struct {
		indent int
		key    string
	}
	var parents []mapping
	blockHeader := regexp.MustCompile(`^[|>](?:[1-9][+-]?|[+-][1-9]?)?(?:[ \t]+#.*)?$`)
	for n := 1; scanner.Scan(); n++ {
		line := scanner.Text()
		text := strings.TrimSpace(line)
		var commands []string
		if name == "Makefile" {
			if strings.HasPrefix(line, "\t") {
				commands = append(commands, strings.TrimLeft(text, "@-+"))
				commands = append(commands, makeShellCommands(text)...)
			} else {
				commands = makeCommands(text)
			}
		} else {
			indent := len(line) - len(strings.TrimLeft(line, " "))
			if text == "" || strings.HasPrefix(text, "#") {
				continue
			}
			if runIndent >= 0 && indent > runIndent {
				// This line is part of the preceding run block.
			} else {
				runIndent = -1
				if strings.HasPrefix(text, "- ") {
					text = strings.TrimPrefix(text, "- ")
					indent += 2 // Sibling step keys align with the key after the dash.
				}
				for len(parents) > 0 && parents[len(parents)-1].indent >= indent {
					parents = parents[:len(parents)-1]
				}
				key, value, ok := strings.Cut(text, ":")
				if !ok {
					continue
				}
				text = strings.TrimSpace(value)
				step := len(parents) > 0 && parents[len(parents)-1].key == "steps"
				defaultRun := len(parents) >= 2 && parents[len(parents)-2].key == "defaults" && parents[len(parents)-1].key == "run"
				if text == "" {
					parents = append(parents, mapping{indent, key})
				}
				if key == "uses" && step {
					action := yamlScalar(text)
					base, ref, _ := strings.Cut(action, "@")
					if strings.HasPrefix(action, "docker://") {
						problems = append(problems, fmt.Sprintf("%q:%d: unapproved container action %q", name, n, action))
					}
					if ref != "" {
						switch base {
						case "actions/setup-node", "actions/setup-python", "actions/setup-java", "actions/setup-ruby", "ruby/setup-ruby",
							"oven-sh/setup-bun", "denoland/setup-deno", "dtolnay/rust-toolchain",
							"astral-sh/setup-uv", "pnpm/action-setup":
							problems = append(problems, fmt.Sprintf("%q:%d: unapproved runtime setup %q", name, n, action))
						}
					}
					continue
				}
				if key == "shell" && (step || defaultRun) {
					text = yamlScalar(text)
				} else if key != "run" {
					continue
				} else if blockHeader.MatchString(text) {
					runIndent = indent
					continue
				} else {
					text = yamlScalar(text)
				}
			}
			commands = append(commands, text)
		}
		for _, command := range commands {
			for _, word := range runtimeCommands(command) {
				problems = append(problems, fmt.Sprintf("%q:%d: unapproved runtime command %q", name, n, word))
			}
		}
	}
	if err := scanner.Err(); err != nil {
		problems = append(problems, fmt.Errorf("scanning commands in %q: %w", name, err).Error())
	}
	return problems
}

func yamlScalar(text string) string {
	// Quoted single-line YAML scalars add one layer beyond shell quoting.
	text, _, _ = strings.Cut(text, " #")
	text = strings.TrimSpace(text)
	if len(text) >= 2 && (text[0] == '\'' || text[0] == '"') && text[len(text)-1] == text[0] {
		return text[1 : len(text)-1]
	}
	return text
}

func makeCommands(text string) []string {
	// Restrict non-recipe scanning to simple assignments, excluding comments.
	assignment := regexp.MustCompile(`^(?:(?:export|override)[ \t]+)?([A-Za-z_][A-Za-z0-9_]*)[ \t]*(::=|[:?+!]?=)[ \t]*(.*)$`).FindStringSubmatch(text)
	if assignment == nil {
		return nil
	}
	value, _, _ := strings.Cut(assignment[3], "#")
	var commands []string
	if assignment[1] == "SHELL" || assignment[2] == "!=" {
		commands = append(commands, value)
	}
	return append(commands, makeShellCommands(value)...)
}

func makeShellCommands(text string) []string {
	var commands []string
	for _, shell := range regexp.MustCompile(`\$\([ \t]*shell[ \t]+([^)]*)\)`).FindAllStringSubmatch(text, -1) {
		commands = append(commands, shell[1])
	}
	return commands
}

func runtimeCommands(line string) []string {
	var words []string
	var word strings.Builder
	quote := rune(0)
	escaped := false
	flush := func() {
		if word.Len() > 0 {
			words = append(words, word.String())
			word.Reset()
		}
	}
	for _, c := range line {
		if escaped {
			word.WriteRune(c)
			escaped = false
			continue
		}
		if c == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			} else {
				word.WriteRune(c)
			}
			continue
		}
		switch {
		case c == '"' || c == '\'':
			quote = c
		case c == '#' && word.Len() == 0:
			goto done
		case strings.ContainsRune(";&|()", c):
			flush()
			words = append(words, ";")
		case unicode.IsSpace(c):
			flush()
		default:
			word.WriteRune(c)
		}
	}
done:
	flush()
	var refused []string
	command := true
	duration := false
	for _, word := range words {
		if word == ";" {
			command = true
			duration = false
			continue
		}
		if !command {
			continue
		}
		if duration {
			duration = false
			if regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?[smhd]?$`).MatchString(word) {
				continue
			}
			command = false
			continue
		}
		if word == "timeout" {
			duration = true
			continue
		}
		if strings.Contains(word, "=") {
			continue
		}
		switch word {
		case "if", "then", "else", "do", "!", "env", "exec", "command", "sudo", "time", "nohup", "xargs", "while", "until", "elif", "{":
			continue
		}
		command = false
		base := regexp.MustCompile(`[0-9]+(?:\.[0-9]+)*$`).ReplaceAllString(path.Base(word), "")
		switch base {
		case "node", "nodejs", "npm", "npx", "python", "pip", "cargo", "rustc", "deno", "bun", "ruby", "perl", "bash", "zsh", "sh", "yarn", "pnpm", "tsc", "tsx", "uv", "poetry", "php", "java", "dash", "ksh", "fish", "pwsh":
			refused = append(refused, word)
		}
	}
	return refused
}
