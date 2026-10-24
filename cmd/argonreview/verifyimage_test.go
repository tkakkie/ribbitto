package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func checkVerifyImageRecipe(recipe, baseDigest, tailwindVersion string) []string {
	var problems, lines []string
	pending := ""
	started := false
	for line := range strings.SplitSeq(recipe, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			if !started && regexp.MustCompile(`(?i)^#\s*(syntax|escape|check)\s*=`).MatchString(line) {
				problems = append(problems, "f: parser directive before first instruction")
			}
			continue
		}
		if line == "" {
			continue
		}
		started = true
		if strings.HasSuffix(line, `\`) {
			pending += strings.TrimSuffix(line, `\`) + " "
			continue
		}
		lines = append(lines, pending+line)
		pending = ""
	}
	if pending != "" {
		problems = append(problems, "unfinished continuation")
	}
	fromCount, fromOK, downloadCount := 0, true, 0
	for _, line := range lines {
		fields := strings.Fields(line)
		instruction := strings.ToUpper(fields[0])
		body := strings.TrimSpace(line[len(fields[0]):])
		switch instruction {
		case "FROM":
			fromCount++
			image := regexp.MustCompile(`^[^@\s]+@(sha256:[0-9a-f]{64})(\s+(?i:AS)\s+[\w-]+)?$`).FindStringSubmatch(body)
			fromOK = fromOK && len(image) == 3 && image[1] == baseDigest
		case "COPY":
			args := strings.Fields(body)
			if len(args) != 3 || strings.HasPrefix(args[2], "--") ||
				(args[0] != "tools/go.mod" || args[1] != "tools/go.sum") && (args[1] != "tools/go.mod" || args[0] != "tools/go.sum") {
				problems = append(problems, "c: COPY needs only tools/go.mod, tools/go.sum and one destination, without flags")
			}
		case "RUN":
			if strings.HasPrefix(body, "--") {
				problems = append(problems, "b: RUN flags are forbidden")
			}
			if regexp.MustCompile(`\bapt-get\b|\bapt\s`).MatchString(body) {
				problems = append(problems, "b: apt commands are forbidden")
			}
			urls := regexp.MustCompile(`https?://[^\s"';]+`).FindAllString(body, -1)
			architecture := body
			for _, url := range urls {
				architecture = strings.ReplaceAll(architecture, url, "")
				downloadCount++
				if url != "https://github.com/tailwindlabs/tailwindcss/releases/download/"+tailwindVersion+"/tailwindcss-linux-arm64" {
					problems = append(problems, "d: Tailwind URL must use the pinned version, host and arm64 asset")
				}
			}
			if len(urls) > 0 {
				if !regexp.MustCompile(`\bsha256sum\s+-c\b`).MatchString(body) || !regexp.MustCompile(`(^|[^[:alnum:]_])[0-9a-f]{64}([^[:alnum:]_]|$)`).MatchString(body) {
					problems = append(problems, "e: Tailwind RUN needs sha256sum -c and a lowercase 64-hex literal")
				}
				if (!strings.Contains(architecture, "dpkg --print-architecture") && !strings.Contains(architecture, "uname -m")) || !strings.Contains(architecture, "arm64") {
					problems = append(problems, "g: Tailwind RUN needs an arm64 architecture check")
				}
			}
		default:
			problems = append(problems, "b: instruction "+instruction+" is forbidden")
		}
	}
	if fromCount == 0 || !fromOK {
		problems = append(problems, "a: every FROM needs the pinned lowercase sha256 digest; at least one FROM is required")
	}
	if downloadCount == 0 {
		problems = append(problems, "d: missing Tailwind download URL")
	}
	return problems
}

func TestVerifyImageRecipe(t *testing.T) {
	digest, hash := "sha256:"+strings.Repeat("a", 64), strings.Repeat("b", 64)
	base := "FROM golang@" + digest + "\n"
	copyLine := "COPY tools/go.mod tools/go.sum /build/tools/\n"
	arch := `test "$(dpkg --print-architecture)" = arm64; `
	url := "https://github.com/tailwindlabs/tailwindcss/releases/download/v1.2.3/tailwindcss-linux-arm64"
	valid := base + copyLine + "RUN " + arch + "curl " + url + `; echo "` + hash + `  /usr/local/bin/tailwindcss" | sha256sum -c -` + "\n"
	change := func(old, replacement string) string { return strings.Replace(valid, old, replacement, 1) }
	for _, tt := range []struct{ name, recipe, want string }{
		{"valid", valid, ""},
		{"case-continuation-comments", "# apt-get VOLUME\n" + change("RUN ", "run \\\n# ignored inside continuation\n"), ""},
		{"reverse-copy", change("tools/go.mod tools/go.sum", "tools/go.sum tools/go.mod"), ""},
		{"uname", change("dpkg --print-architecture", "uname -m"), ""},
		{"stages", change(base, base+strings.TrimSpace(base)+" AS builder\n"), ""},
		{"a/no-from", change(base, ""), "a:"},
		{"a/no-digest", change("golang@"+digest, "golang"), "a:"},
		{"a/tag-only", change("golang@"+digest, "golang:1.27.3"), "a:"},
		{"a/wrong-digest", change(digest, "sha256:"+strings.Repeat("c", 64)), "a:"},
		{"a/scratch", change("golang@"+digest, "scratch"), "a:"},
		{"a/stage", change("golang@"+digest, "builder"), "a:"},
		{"a/short", change(digest, "sha256:aaaa"), "a:"},
		{"a/uppercase", change(digest, "sha256:"+strings.Repeat("A", 64)), "a:"},
		{"a/second-from", valid + "FROM scratch\n", "a:"},
		{"b/ADD", valid + "ADD x /x\n", "b:"},
		{"b/VOLUME", valid + "VOLUME /x\n", "b:"},
		{"b/ENTRYPOINT", valid + "ENTRYPOINT x\n", "b:"},
		{"b/CMD", valid + "CMD x\n", "b:"},
		{"b/USER", valid + "USER x\n", "b:"},
		{"b/EXPOSE", valid + "EXPOSE 80\n", "b:"},
		{"b/HEALTHCHECK", valid + "HEALTHCHECK NONE\n", "b:"},
		{"b/ONBUILD", valid + "ONBUILD RUN true\n", "b:"},
		{"b/ARG", valid + "ARG x\n", "b:"},
		{"b/SHELL", valid + "SHELL x\n", "b:"},
		{"b/ENV", valid + "ENV x=y\n", "b:"},
		{"b/flag", change("RUN ", "RUN --mount=type=secret "), "b:"},
		{"b/apt-get", valid + "RUN apt-get update\n", "b:"},
		{"b/apt-path", valid + "RUN /usr/bin/apt-get update\n", "b:"},
		{"b/apt", valid + "RUN set -eu; apt update\n", "b:"},
		{"c/extra", change(copyLine, "COPY tools/go.mod tools/go.sum extra /build/\n"), "c:"},
		{"c/dot", change(copyLine, "COPY . /build/\n"), "c:"},
		{"c/from-flag", change("COPY ", "COPY --from=builder "), "c:"},
		{"c/one-source", change("tools/go.mod tools/go.sum", "tools/go.mod"), "c:"},
		{"c/backup", change("tools/go.sum", "tools/go.sum.bak"), "c:"},
		{"d/older", change("v1.2.3", "v1.2.2"), "d:"},
		{"d/newer", change("v1.2.3", "v1.2.4"), "d:"},
		{"d/host", change("github.com", "example.com"), "d:"},
		{"d/asset", change("tailwindcss-linux-arm64", "tailwindcss-linux-x64"), "d:"},
		{"d/missing", change(url, ""), "d:"},
		{"e/no-check", change("sha256sum -c", "cat") + "# sha256sum -c -\n", "e:"},
		{"e/no-literal", change(hash, "") + "# " + hash + "\n", "e:"},
		{"e/short", change(hash, "bbbb"), "e:"},
		{"e/uppercase", change(hash, strings.ToUpper(hash)), "e:"},
		{"e/other-run", change(" | sha256sum -c -", "\nRUN sha256sum -c -"), "e:"},
		{"f/syntax", "# syntax=docker/dockerfile:1\n" + valid, "f:"},
		{"f/escape", "# escape=`\n" + valid, "f:"},
		{"f/check", "# ordinary comment\n# check=skip=all\n" + valid, "f:"},
		{"g/no-check", change(arch, "") + "# " + arch + "\n", "g:"},
		{"g/wrong-arch", change(arch, strings.ReplaceAll(arch, "arm64", "amd64")), "g:"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			problems := checkVerifyImageRecipe(tt.recipe, digest, "v1.2.3")
			if (tt.want == "" && len(problems) != 0) || (tt.want != "" && (len(problems) != 1 || !strings.HasPrefix(problems[0], tt.want))) {
				t.Fatalf("want %q, got %v", tt.want, problems)
			}
		})
	}
	t.Run("real-file", func(t *testing.T) {
		_, digest, _ := strings.Cut(containerGoImage, "@")
		if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(digest) {
			t.Fatal("containerGoImage has no full digest")
		}
		download, err := os.ReadFile("../../tools/download/main.go")
		if err != nil {
			t.Fatal(err)
		}
		version := regexp.MustCompile(`(?m)^const tailwindVersion = "(v[0-9]+\.[0-9]+\.[0-9]+)"$`).FindSubmatch(download)
		if len(version) != 2 {
			t.Fatal("tailwindVersion constant not found")
		}
		recipe, err := os.ReadFile("verifyimage/Dockerfile")
		if err != nil {
			t.Fatal(err)
		}
		if problems := checkVerifyImageRecipe(string(recipe), digest, string(version[1])); len(problems) != 0 {
			t.Fatal("real recipe:", problems)
		}
	})
}
