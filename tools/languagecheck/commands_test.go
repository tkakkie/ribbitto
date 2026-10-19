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
		{"dash block siblings", ".github/workflows/ci.yml", "steps:\n  - run: | # commands\n      pnpm build\n    name: python\n    with:\n      shell: bash\n", 1},
		{"shell assignment", "Makefile", "SHELL := /bin/bash\n", 1},
		{"shell equals", "Makefile", "export SHELL = pwsh\n", 1},
		{"make shell", "Makefile", "VALUE := $(shell python3.12 build.py)\n", 1},
		{"recipe make shell", "Makefile", "target:\n\t@echo $(shell node build.js)\n", 1},
		{"recipe make shell argument", "Makefile", "target:\n\t@echo $(shell echo node)\n", 0},
		{"recipe make shell comment", "Makefile", "target:\n\t@echo ok # $(shell node build.js)\n", 1},
		{"double colon assignment", "Makefile", "VALUE ::= $(shell node build.js)\n", 1},
		{"double colon argument", "Makefile", "VALUE ::= $(shell echo node)\n", 0},
		{"shell execution assignment", "Makefile", "VALUE != node build.js\n", 1},
		{"shell execution argument", "Makefile", "VALUE != echo node\n", 0},
		{"container action", ".github/workflows/ci.yml", "steps:\n  - uses: docker://example/image:latest\n", 1},
		{"container action input", ".github/workflows/ci.yml", "steps:\n  - uses: example/action@v1\n    with:\n      uses: docker://example/image:latest\n", 0},
		{"container action argument", ".github/workflows/ci.yml", "steps:\n  - run: echo docker://example/image:latest\n", 0},
		{"make shell chain", "Makefile", "VALUE = $(shell go version; uv run build.py)\n", 1},
		{"make escaped hash", "Makefile", `X := $(shell echo "\#"; python x)` + "\n", 1},
		{"make escaped hash comment", "Makefile", `X := $(shell \#; python x)` + "\n", 0},
		{"make quoted hash comment", "Makefile", `X := " # $(shell python x)"` + "\n", 0},
		{"make hash comment", "Makefile", `X := $(shell go version) # $(shell python x)` + "\n", 0},
		{"make allowed shell", "Makefile", "TOOL_PATHS := $(shell go -C tools run ./download -paths)\n", 0},
		{"make shell arguments", "Makefile", "VALUE := $(shell echo python)\n", 0},
		{"make assignments", "Makefile", "TEXT := python\nSHELL_TOOL := bash\nSHELL := ./approved-shell\n", 0},
		{"make shell comments", "Makefile", "# SHELL := bash\n# VALUE := $(shell python)\nVALUE := go # $(shell python)\n", 0},
		{"non-step workflow keys", ".github/workflows/ci.yml", "env:\n  shell: bash\n  uses: actions/setup-node@v1\nsteps:\n  - uses: example/action@v1\n    with:\n      shell: python\n      uses: actions/setup-python@v1\n", 0},
		{"allowed workflow shell", ".github/workflows/ci.yml", "steps:\n  - run: go version\n    shell: ./approved-shell {0}\n", 0},
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

func TestAddedCommandWords(t *testing.T) {
	for _, word := range []string{"yarn", "pnpm", "tsc", "tsx", "uv", "poetry", "php", "java", "dash", "ksh", "fish", "pwsh", "python3.12", "pip3.12"} {
		t.Run(word, func(t *testing.T) {
			for _, source := range []string{word + " build", "/usr/bin/" + word + " build"} {
				if got := runtimeCommands(source); len(got) != 1 {
					t.Errorf("%q: got %v; want one command", source, got)
				}
			}
			for _, source := range []string{"echo " + word, word + "-helper build", "go run ./" + word, "echo '" + word + "'"} {
				if got := runtimeCommands(source); len(got) != 0 {
					t.Errorf("%q: unexpected %v", source, got)
				}
			}
		})
	}
	for _, source := range []string{"python3.12-helper build", "python3.x build", "pip3..12 install", "python.12 build", "python3. build"} {
		if got := runtimeCommands(source); len(got) != 0 {
			t.Errorf("%q: unexpected %v", source, got)
		}
	}
}

func TestCommandPrefixes(t *testing.T) {
	for _, prefix := range []string{"time", "nohup", "xargs", "timeout 30", "timeout 0.5s", "while", "until", "elif", "{"} {
		t.Run(prefix, func(t *testing.T) {
			if got := runtimeCommands(prefix + " python3.12 build"); len(got) != 1 {
				t.Errorf("got %v; want one command", got)
			}
			for _, source := range []string{"echo " + prefix + " python", prefix + " go run ./python"} {
				if got := runtimeCommands(source); len(got) != 0 {
					t.Errorf("%q: unexpected %v", source, got)
				}
			}
		})
	}
	if got := runtimeCommands("timeout python echo"); len(got) != 0 {
		t.Errorf("invalid timeout duration: %v", got)
	}
}

func TestWorkflowBlockHeaders(t *testing.T) {
	for _, header := range []string{"| # comment", "|+", ">+", "|2", ">-", "|2- # comment", ">+2", "|", ">", "|-"} {
		t.Run(header, func(t *testing.T) {
			for _, tt := range []struct {
				command string
				count   int
			}{{"python build.py", 1}, {"echo python", 0}, {"go version # python", 0}} {
				source := "steps:\n  - run: " + header + "\n      " + tt.command + "\n    name: python\n"
				if got := checkCommands(".github/workflows/ci.yml", strings.NewReader(source)); len(got) != tt.count {
					t.Errorf("%q: got %v; want %d", source, got, tt.count)
				}
			}
		})
	}
	if got := checkCommands(".github/workflows/ci.yml", strings.NewReader("run: echo '|+'\n  name: python\n")); len(got) != 0 {
		t.Errorf("quoted block indicator: %v", got)
	}
}

func TestWorkflowBlockLines(t *testing.T) {
	for _, header := range []string{"|", ">", "|-"} {
		t.Run(header, func(t *testing.T) {
			source := "steps:\n  - run: " + header + "\n      python first.py\n        python nested.py\n      python last.py\n    name: python\n    env:\n      VALUE: python\n  - run: echo python\n"
			got := checkCommands(".github/workflows/ci.yml", strings.NewReader(source))
			if len(got) != 3 {
				t.Fatalf("got %v; want three block commands", got)
			}
			for i, problem := range got {
				if !strings.Contains(problem, []string{":3:", ":4:", ":5:"}[i]) || !strings.Contains(problem, `command "python"`) {
					t.Errorf("unexpected block finding: %s", problem)
				}
			}
		})
	}
}

func TestYAMLScalarComments(t *testing.T) {
	for _, tt := range []struct{ source, want string }{
		{`echo " #"; python build.py`, `echo " #"; python build.py`},
		{`'echo " #"; python build.py' # comment`, `echo " #"; python build.py`},
		{`"echo \" # still quoted" # comment`, `echo \" # still quoted`},
		{`'echo '' # still quoted' # comment`, `echo '' # still quoted`},
		{`go version # python build.py`, `go version`},
		{"go version\t# python build.py", `go version`},
		{`echo value#suffix; python build.py`, `echo value#suffix; python build.py`},
	} {
		t.Run(tt.source, func(t *testing.T) {
			if got := yamlScalar(tt.source); got != tt.want {
				t.Errorf("got %q; want %q", got, tt.want)
			}
		})
	}
	for _, tt := range []struct {
		source string
		count  int
	}{
		{`echo " #"; python build.py`, 1},
		{`echo ' #'; python build.py`, 1},
		{`'echo " #"; python build.py' # comment`, 1},
		{`echo value#suffix; python build.py`, 1},
		{`go version # python build.py`, 0},
		{`echo " #; python build.py"`, 0},
	} {
		t.Run("run "+tt.source, func(t *testing.T) {
			got := checkCommands(".github/workflows/ci.yml", strings.NewReader("steps:\n  - run: "+tt.source+"\n"))
			if len(got) != tt.count {
				t.Errorf("got %v; want %d", got, tt.count)
			}
		})
	}
}

func TestMakeAssignmentComments(t *testing.T) {
	for _, tt := range []struct{ source, want string }{
		{`echo "#"; python x`, `echo "`},
		{`echo "\#"; python x`, `echo "#"; python x`},
		{`echo \\#; python x`, `echo \`},
		{`echo \\\#; python x # comment`, `echo \#; python x `},
		{`echo \\value`, `echo \\value`},
	} {
		if got := makeAssignmentValue(tt.source); got != tt.want {
			t.Errorf("%q: got %q; want %q", tt.source, got, tt.want)
		}
	}
}

func TestPathQualifiedWrappers(t *testing.T) {
	for _, prefix := range []string{"/usr/bin/env", "/bin/exec", "/usr/bin/command", "/usr/bin/sudo", "/usr/bin/time", "/usr/bin/nohup", "/usr/bin/xargs", "/usr/bin/timeout 30", "/usr/bin/timeout 0.5s"} {
		t.Run(prefix, func(t *testing.T) {
			if got := runtimeCommands(prefix + " python build.py"); len(got) != 1 || got[0] != "python" {
				t.Errorf("got %v; want python", got)
			}
			for _, source := range []string{prefix, "echo " + prefix + " python", prefix + " go run ./python"} {
				if got := runtimeCommands(source); len(got) != 0 {
					t.Errorf("%q: unexpected %v", source, got)
				}
			}
		})
	}
	for _, word := range []string{"if", "then", "else", "do", "while", "until", "elif", "!", "{"} {
		if got := runtimeCommands("/usr/bin/" + word + " python"); len(got) != 0 {
			t.Errorf("path-qualified control word %q: unexpected %v", word, got)
		}
	}
	if got := runtimeCommands("/usr/bin/timeout python echo"); len(got) != 0 {
		t.Errorf("invalid timeout duration: %v", got)
	}
}

func TestWorkflowRuntimeKeys(t *testing.T) {
	for _, action := range []string{"actions/setup-node", "actions/setup-python", "actions/setup-java", "actions/setup-ruby", "ruby/setup-ruby",
		"oven-sh/setup-bun", "denoland/setup-deno", "dtolnay/rust-toolchain",
		"astral-sh/setup-uv", "pnpm/action-setup",
	} {
		t.Run(action, func(t *testing.T) {
			for _, value := range []string{action + "@v1 # pinned", "'" + action + "@v1'"} {
				source := "steps:\n  - uses: " + value + "\n"
				if got := checkCommands(".github/workflows/ci.yml", strings.NewReader(source)); len(got) != 1 {
					t.Errorf("%q: got %v; want one setup", source, got)
				}
			}
			for _, value := range []string{action + "-helper@v1", "example/" + action + "@v1", "actions/setup-go@v1"} {
				source := "steps:\n  - uses: " + value + "\n"
				if got := checkCommands(".github/workflows/ci.yml", strings.NewReader(source)); len(got) != 0 {
					t.Errorf("%q: unexpected %v", source, got)
				}
			}
		})
	}
	for _, shell := range []string{"bash", "sh", "python", "pwsh", "dash", "ksh", "fish", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			for _, source := range []string{
				"steps:\n  - run: go version\n    shell: '" + shell + " {0}' # custom\n",
				"defaults:\n  run:\n    shell: " + shell + "\n",
			} {
				if got := checkCommands(".github/workflows/ci.yml", strings.NewReader(source)); len(got) != 1 {
					t.Errorf("%q: got %v; want one shell", source, got)
				}
			}
			source := "steps:\n  - name: " + shell + "\n    run: echo " + shell + "\n    with:\n      shell: " + shell + "\n"
			if got := checkCommands(".github/workflows/ci.yml", strings.NewReader(source)); len(got) != 0 {
				t.Errorf("%q: unexpected %v", source, got)
			}
		})
	}
}
