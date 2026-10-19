package main

import (
	"strings"
	"testing"
)

func TestCommands(t *testing.T) {
	for _, tt := range []struct {
		name, file, source string
		count              int
	}{
		{"recipe", "Makefile", "target:\n\t@node build.js\n", 1},
		{"assignment", "Makefile", "\tMODE=test python3 tool.py\n", 1},
		{"chain", "Makefile", "\tgo version && npm run build; cargo build\n", 2},
		{"wrapper", "Makefile", "\tenv exec /usr/bin/python tool.py\n", 1},
		{"shell", "Makefile", "\tsh helper\n", 1},
		{"if", "Makefile", "\tif node a.js; then pip install x; fi\n", 2},
		{"quoted command", "Makefile", "\t'node' a.js\n", 1},
		{"inline run", ".github/workflows/ci.yml", "  - run: node build.js\n", 1},
		{"quoted YAML", ".github/workflows/ci.yml", "  - run: 'npm run build'\n", 1},
		{"block run", ".github/workflows/ci.yml", "    run: |\n      go version\n      cargo build\n    name: python\n", 1},
		{"comments", "Makefile", "# python build\n\t# node a.js\n\tgo version # npm\n", 0},
		{"prose", ".github/workflows/ci.yml", "name: node\nsteps:\n  - name: python\n    uses: example/npm@v1\n", 0},
		{"substrings", "Makefile", "\tnodecheck npm-config python-tool cargo-check\n", 0},
		{"arguments", "Makefile", "\tgo run ./python\n\techo node npm python\n", 0},
		{"quoted operators", "Makefile", "\techo 'a; node x' \"a && npm x\"\n", 0},
		{"scanner error", "Makefile", strings.Repeat("x", 100_000), 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			problems := checkCommands(tt.file, strings.NewReader(tt.source))
			if len(problems) != tt.count {
				t.Fatalf("got %v; want %d", problems, tt.count)
			}
		})
	}
}
