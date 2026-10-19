package main

import (
	"bufio"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode"
)

// This deliberately small scan checks command positions in Make recipes and
// workflow run steps. Review still checks configurations, indirect execution
// and dependency/runtime changes that a command-word scan cannot establish.
func checkCommands(name string, source io.Reader) []string {
	var problems []string
	scanner := bufio.NewScanner(source)
	runIndent := -1
	for n := 1; scanner.Scan(); n++ {
		line := scanner.Text()
		text := strings.TrimSpace(line)
		if name == "Makefile" {
			if !strings.HasPrefix(line, "\t") {
				continue
			}
			text = strings.TrimLeft(text, "@-+")
		} else {
			indent := len(line) - len(strings.TrimLeft(line, " "))
			if text == "" || strings.HasPrefix(text, "#") {
				continue
			}
			if runIndent >= 0 && indent > runIndent {
				// This line is part of the preceding run block.
			} else {
				runIndent = -1
				text = strings.TrimPrefix(text, "- ")
				if !strings.HasPrefix(text, "run:") {
					continue
				}
				text = strings.TrimSpace(strings.TrimPrefix(text, "run:"))
				if text == "|" || text == ">" || text == "|-" || text == ">-" {
					runIndent = indent
					continue
				}
				// A quoted single-line YAML scalar has one layer of quoting beyond shell.
				if len(text) >= 2 && (text[0] == '\'' || text[0] == '"') && text[len(text)-1] == text[0] {
					text = text[1 : len(text)-1]
				}
			}
		}
		for _, word := range runtimeCommands(text) {
			problems = append(problems, fmt.Sprintf("%q:%d: unapproved runtime command %q", name, n, word))
		}
	}
	if err := scanner.Err(); err != nil {
		problems = append(problems, fmt.Errorf("scanning commands in %q: %w", name, err).Error())
	}
	return problems
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
	for _, word := range words {
		if word == ";" {
			command = true
			continue
		}
		if !command {
			continue
		}
		if strings.Contains(word, "=") || word == "if" || word == "then" || word == "else" || word == "do" || word == "!" || word == "env" || word == "exec" || word == "command" || word == "sudo" {
			continue
		}
		command = false
		base := path.Base(word)
		switch base {
		case "node", "nodejs", "npm", "npx", "python", "python2", "python3", "pip", "pip2", "pip3", "cargo", "rustc", "deno", "bun", "ruby", "perl", "bash", "zsh", "sh":
			refused = append(refused, word)
		}
	}
	return refused
}
