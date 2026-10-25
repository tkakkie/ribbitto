package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// A key change needs this constant, docs/workflow/argon-verify-image.md and a review.
const approvedPGDGFingerprint = "B97B0AFCAA1A47F044F244A07FCC7D46ACCC4CF8"

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
	keyChecked, uncheckedPGDG := false, false
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
			if regexp.MustCompile(`\bapt\s`).MatchString(body) {
				problems = append(problems, "b: plain apt is forbidden")
			}
			pgdgAction := strings.Contains(body, "apt.postgresql.org.sh")
			installsPostgresqlCommon := false
			for _, at := range regexp.MustCompile(`[^\s;|&]*\bapt-get\b[^\s;|&]*`).FindAllStringIndex(body, -1) {
				args := strings.Fields(regexp.MustCompile(`[;|&]`).Split(body[at[0]:], 2)[0])
				bare := regexp.MustCompile(`(?:^|;|&&)\s*$`).MatchString(body[:at[0]])
				if !bare || args[0] != "apt-get" || len(args) < 2 ||
					(args[1] != "install" && (args[1] != "update" || len(args) != 2)) {
					problems = append(problems, "b: only bare apt-get update or install is allowed")
					continue
				}
				if args[1] == "install" {
					if !strings.Contains(" "+strings.Join(args[2:], " ")+" ", " -y ") {
						problems = append(problems, "h: install needs -y")
					}
					if !strings.Contains(" "+strings.Join(args[2:], " ")+" ", " --no-install-recommends ") {
						problems = append(problems, "h: install needs --no-install-recommends")
					}
					for _, arg := range args[2:] {
						if arg == "-y" || arg == "--no-install-recommends" {
							continue
						}
						name, version, pinned := strings.Cut(arg, "=")
						installsPostgresqlCommon = installsPostgresqlCommon || name == "postgresql-common"
						pgdgAction = pgdgAction || name == "postgresql-18"
						allowed := name == "postgresql-common" || name == "ca-certificates" || name == "postgresql-18"
						if !allowed || !pinned || !regexp.MustCompile(`^[A-Za-z0-9.+~:-]+$`).MatchString(version) {
							problems = append(problems, "h: forbidden install flag or unapproved/unpinned package: "+arg)
						}
					}
				}
			}
			for _, at := range regexp.MustCompile(`[^\s;|&]*apt\.postgresql\.org\.sh[^\s;|&]*`).FindAllStringIndex(body, -1) {
				if body[at[0]:at[1]] != "/usr/share/postgresql-common/pgdg/apt.postgresql.org.sh" ||
					!regexp.MustCompile(`(?:^|;|&&)\s*$`).MatchString(body[:at[0]]) ||
					!regexp.MustCompile(`^\s+-y\s*(?:;|&&|$)`).MatchString(body[at[1]:]) {
					problems = append(problems, "j: PGDG setup must be the bare full-path command with only -y")
				}
			}
			// A check in this RUN cannot authorize this RUN's PGDG actions.
			uncheckedPGDG = uncheckedPGDG || pgdgAction && !keyChecked
			fingerprint := regexp.MustCompile(`(?:^|;|&&)\s*fingerprint="\$\(gpg --show-keys --with-colons /usr/share/postgresql-common/pgdg/apt\.postgresql\.org\.gpg \| grep '\^fpr:' \| cut -d: -f10\)";\s*test "\$fingerprint" = "` + approvedPGDGFingerprint + `"\s*;?\s*$`).MatchString(body)
			errexit := regexp.MustCompile(`^set\s+(?:-[a-z]+\s+)*-[a-z]*e[a-z]*(?:\s+-[a-z]+)*\s*(;|&&)`).MatchString(body)
			disabledErrexit := regexp.MustCompile(`\bset\s+[^;|&]*(?:\+[a-z]*e[a-z]*\b|\+o\s+errexit\b)`).MatchString(body)
			if fingerprint && errexit && !disabledErrexit && !strings.Contains(body, "||") {
				keyChecked = true
			}
			clusterEdit := `sed -i 's/^#create_main_cluster.*/create_main_cluster = false/' /etc/postgresql-common/createcluster.conf;`
			clusterCheck := `grep -qx 'create_main_cluster = false' /etc/postgresql-common/createcluster.conf;`
			if installsPostgresqlCommon && (!errexit || disabledErrexit ||
				!regexp.MustCompile(regexp.QuoteMeta(clusterEdit)+`\s+`+regexp.QuoteMeta(clusterCheck)).MatchString(body)) {
				problems = append(problems, "k: postgresql-common RUN needs errexit and the cluster edit immediately followed by its exact-line check")
			}
			urls := regexp.MustCompile(`https?://[^\s"';]+`).FindAllString(body, -1)
			for _, url := range urls {
				downloadCount++
				if url != "https://github.com/tailwindlabs/tailwindcss/releases/download/"+tailwindVersion+"/tailwindcss-linux-arm64" {
					problems = append(problems, "d: Tailwind URL must use the pinned version, host and arm64 asset")
				}
			}
			if len(urls) > 0 {
				checksum := regexp.MustCompile(`\becho\s+(?:"[0-9a-f]{64}\s+/usr/local/bin/tailwindcss"|'[0-9a-f]{64}\s+/usr/local/bin/tailwindcss')\s*\|\s*sha256sum\s+-c(?:\s|$)`).MatchString(body)
				output := regexp.MustCompile(`\bcurl\b[^;|&]*\s-o\s+/usr/local/bin/tailwindcss(?:\s|$)`).MatchString(body)
				if !checksum || !output {
					problems = append(problems, "e: Tailwind RUN needs a literal SHA-256 pipeline checking the download's exact output path")
				}
				arch := regexp.MustCompile(`(?:\btest\s+|\[\s+)"\$\((dpkg --print-architecture|uname -m)\)"\s+=\s+(?:"([a-z0-9]+)"|([a-z0-9]+))`).FindStringSubmatch(body)
				fatal := false
				if len(arch) > 0 {
					value := arch[2] + arch[3]
					accepted := arch[1] == "dpkg --print-architecture" && value == "arm64" || arch[1] == "uname -m" && value == "aarch64"
					if !accepted {
						problems = append(problems, "g: architecture must pair dpkg with arm64 or uname with aarch64")
					}
					start := strings.Index(body, arch[0])
					// Keep the guard directly after set: shell control flow can swallow errexit.
					prefix := regexp.MustCompile(`^(?:--\S+\s+)*set\s+(?:-[a-z]+\s+)*-[a-z]*e[a-z]*(?:\s+-[a-z]+)*\s*(;|&&)\s*$`).MatchString(body[:start])
					ending := `^\s*(;|&&\s*curl\b)`
					if strings.HasPrefix(arch[0], "[") {
						ending = `^\s+\]\s*(;|&&\s*curl\b)`
					}
					terminated := regexp.MustCompile(ending).MatchString(body[start+len(arch[0]):])
					download := regexp.MustCompile(`\bcurl\b|https?://`).FindStringIndex(body)
					noFallback := !strings.Contains(body, "||")
					fatal = prefix && terminated && noFallback && start < download[0]
				}
				if !fatal {
					problems = append(problems, "g: Tailwind RUN needs set -e and a standalone architecture guard before the download")
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
	if uncheckedPGDG {
		problems = append(problems, "i: PGDG actions need a valid fingerprint check in an earlier RUN")
	}
	return problems
}

func TestVerifyImageRecipe(t *testing.T) {
	digest, hash := "sha256:"+strings.Repeat("a", 64), strings.Repeat("b", 64)
	base := "FROM golang@" + digest + "\n"
	copyLine := "COPY tools/go.mod tools/go.sum /build/tools/\n"
	arch := `test "$(dpkg --print-architecture)" = arm64; `
	url := "https://github.com/tailwindlabs/tailwindcss/releases/download/v1.2.3/tailwindcss-linux-arm64"
	tools := base + copyLine + "RUN set -eu; " + arch + "curl -o /usr/local/bin/tailwindcss " + url + `; echo "` + hash + `  /usr/local/bin/tailwindcss" | sha256sum -c -` + "\n"
	fingerprint := approvedPGDGFingerprint
	keyCheck := `fingerprint="$(gpg --show-keys --with-colons /usr/share/postgresql-common/pgdg/apt.postgresql.org.gpg | grep '^fpr:' | cut -d: -f10)"; test "$fingerprint" = "` + fingerprint + `";`
	clusterEdit := `sed -i 's/^#create_main_cluster.*/create_main_cluster = false/' /etc/postgresql-common/createcluster.conf; `
	clusterCheck := `grep -qx 'create_main_cluster = false' /etc/postgresql-common/createcluster.conf; `
	debian := "RUN set -eu; export DEBIAN_FRONTEND=noninteractive; apt-get update; " +
		"apt-get install -y --no-install-recommends postgresql-common=278 ca-certificates=20250419+deb13u1; " +
		clusterEdit + clusterCheck +
		"rm -rf /var/lib/apt/lists/*; " + keyCheck + "\n"
	// Separate RUNs let errexit fixtures exercise i and k independently.
	clusterRun := strings.Replace(debian, keyCheck, "", 1)
	keyRun := "RUN set -eu; " + keyCheck + "\n"
	pgdgScript := "/usr/share/postgresql-common/pgdg/apt.postgresql.org.sh"
	pgdg := "RUN set -eu; export DEBIAN_FRONTEND=noninteractive; " + pgdgScript + " -y; apt-get update; " +
		"apt-get install -y --no-install-recommends postgresql-18=18.6-1.pgdg13+2; " +
		"rm -rf /var/lib/apt/lists/*; useradd -u 1000 -m -d /home/check -s /bin/bash check\n"
	valid := tools + debian + pgdg
	change := func(old, replacement string) string { return strings.Replace(valid, old, replacement, 1) }
	for _, tt := range []struct{ name, recipe, want string }{
		{"valid", valid, ""},
		{"generators-only", tools, ""},
		{"apt-get-update", valid + "RUN apt-get update\n", ""},
		{"second-install", valid + "RUN apt-get install -y --no-install-recommends ca-certificates=20250419+deb13u1\n", ""},
		{"apt-get-and", change("; apt-get update;", "&& apt-get update &&"), ""},
		{"pgdg-bare", tools + debian + "RUN " + pgdgScript + " -y\n", ""},
		{"pgdg-and", change(pgdgScript+" -y;", pgdgScript+" -y &&"), ""},
		{"fingerprint-set-e", change(debian, strings.Replace(debian, "set -eu;", "set -e;", 1)), ""},
		{"case-continuation-comments", "# apt-get VOLUME\n" + change("RUN ", "run \\\n# ignored inside continuation\n"), ""},
		{"reverse-copy", change("tools/go.mod tools/go.sum", "tools/go.sum tools/go.mod"), ""},
		{"uname", change(arch, `test "$(uname -m)" = aarch64; `), ""},
		{"bracket-guard", change(arch, `[ "$(dpkg --print-architecture)" = "arm64" ]; `), ""},
		{"guard-and", change(arch, strings.ReplaceAll(arch, ";", "&&")), ""},
		{"set-e", change("set -eu", "set -e"), ""},
		{"set-ue", change("set -eu", "set -ue"), ""},
		{"set-and", change("set -eu;", "set -eu &&"), ""},
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
		{"b/apt-get-upgrade", valid + "RUN apt-get upgrade\n", "b:"},
		{"b/apt-get-update-args", valid + "RUN apt-get update -y\n", "b:"},
		{"b/apt-get-no-command", valid + "RUN apt-get\n", "b:"},
		{"b/apt-get-wrapped", valid + "RUN command apt-get update\n", "b:"},
		{"b/apt-get-piped", valid + "RUN true | apt-get update\n", "b:"},
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
		{"e/other-path", change(hash+"  /usr/local/bin/tailwindcss", hash+"  /usr/local/bin/other"), "e:"},
		{"e/path-suffix", change(hash+"  /usr/local/bin/tailwindcss", hash+"  /usr/local/bin/tailwindcss.bak"), "e:"},
		{"e/output-path", change("-o /usr/local/bin/tailwindcss", "-o /usr/local/bin/other"), "e:"},
		{"e/separate-pipeline", change(" | sha256sum -c -", "; sha256sum -c -"), "e:"},
		{"f/syntax", "# syntax=docker/dockerfile:1\n" + valid, "f:"},
		{"f/escape", "# escape=`\n" + valid, "f:"},
		{"f/check", "# ordinary comment\n# check=skip=all\n" + valid, "f:"},
		{"g/no-check", change(arch, "") + "# " + arch + "\n", "g:"},
		{"g/wrong-arch", change(arch, strings.ReplaceAll(arch, "arm64", "amd64")), "g:"},
		{"g/uname-arm64", change("dpkg --print-architecture", "uname -m"), "g:"},
		{"g/dpkg-aarch64", change(arch, strings.ReplaceAll(arch, "arm64", "aarch64")), "g:"},
		{"g/other-run", change(arch, "") + "RUN set -eu; " + arch + "\n", "g:"},
		{"g/after-download", strings.TrimSuffix(change(arch, ""), "\n") + "; " + arch + "\n", "g:"},
		{"g/fallback", change(arch, strings.ReplaceAll(arch, ";", " || true;")), "g:"},
		{"g/fallback-colon", change(arch, strings.ReplaceAll(arch, ";", " || :;")), "g:"},
		{"g/and-fallback", change(arch, strings.ReplaceAll(arch, ";", " && true || true;")), "g:"},
		{"g/and-swallow", change(arch, strings.ReplaceAll(arch, ";", " && true;")), "g:"},
		{"g/no-errexit", change("set -eu; ", ""), "g:"},
		{"g/pipeline", change(arch, strings.ReplaceAll(arch, ";", " | cat;")), "g:"},
		{"g/condition", change(arch, "if "+arch+"then :; fi; "), "g:"},
		{"h/no-y", change("install -y", "install"), "h:"},
		{"h/no-recommends-flag", change("--no-install-recommends ", ""), "h:"},
		{"h/extra-flag", change("install -y", "install -y -o"), "h:"},
		{"h/unauthenticated", change("install -y", "install -y --allow-unauthenticated"), "h:"},
		{"h/downgrades", change("install -y", "install -y --allow-downgrades"), "h:"},
		{"h/target-release", change("install -y", "install -y -t"), "h:"},
		{"h/unpinned", change("postgresql-common=278", "postgresql-common"), "h:"},
		{"h/unknown-pinned", change("postgresql-common=278", "procps=278"), "h:"},
		{"h/unknown-unpinned", change("postgresql-common=278", "procps"), "h:"},
		{"h/empty-version", change("postgresql-common=278", "postgresql-common="), "h:"},
		{"h/invalid-version", change("postgresql-common=278", "postgresql-common=278*"), "h:"},
		{"i/no-check-run", change(debian, ""), "i:"},
		{"i/check-removed", change(keyCheck, ""), "i:"},
		{"i/same-run", change(debian+pgdg, strings.TrimSuffix(debian, "\n")+" "+strings.TrimPrefix(pgdg, "RUN set -eu; ")), "i:"},
		{"i/after-pgdg", change(debian+pgdg, pgdg+debian), "i:"},
		{"i/other-key", change(fingerprint, strings.Repeat("AB", 20)), "i:"},
		{"i/lowercase", change(fingerprint, strings.ToLower(fingerprint)), "i:"},
		{"i/short", change(fingerprint, fingerprint[:39]), "i:"},
		{"i/asc", change("apt.postgresql.org.gpg", "apt.postgresql.org.asc"), "i:"},
		{"i/fallback", change(keyCheck, strings.TrimSuffix(keyCheck, ";")+" || true;"), "i:"},
		{"i/other-fallback", change(keyCheck, keyCheck+" true || true;"), "i:"},
		{"i/no-errexit", change(debian, clusterRun+strings.Replace(keyRun, "set -eu; ", "", 1)), "i:"},
		{"i/and-continue", change(keyCheck, strings.TrimSuffix(keyCheck, ";")+" && :; true"), "i:"},
		{"i/last-not-check", change(keyCheck, keyCheck+" true"), "i:"},
		{"i/set-plus-e", change(debian, clusterRun+strings.Replace(keyRun, "set -eu;", "set -eu; set +e;", 1)), "i:"},
		{"i/set-plus-o-errexit", change(debian, clusterRun+strings.Replace(keyRun, "set -eu;", "set -eu; set +o errexit;", 1)), "i:"},
		{"i/install-only", tools + "RUN apt-get install -y --no-install-recommends postgresql-18=18.6-1.pgdg13+2\n", "i:"},
		{"i/script-only", tools + "RUN " + pgdgScript + " -y\n", "i:"},
		{"j/no-y", change(pgdgScript+" -y", pgdgScript), "j:"},
		{"j/extra-arg", change(pgdgScript+" -y", pgdgScript+" -y -i"), "j:"},
		{"j/other-path", change(pgdgScript, "/tmp/apt.postgresql.org.sh"), "j:"},
		{"j/piped", change(pgdgScript+" -y;", pgdgScript+" -y | cat;"), "j:"},
		{"j/piped-input", change(pgdgScript, "true | "+pgdgScript), "j:"},
		{"j/bash", change(pgdgScript, "bash "+pgdgScript), "j:"},
		{"k/sed-missing", change(clusterEdit, ""), "k:"},
		{"k/sed-reworded", change(clusterEdit, strings.Replace(clusterEdit, "sed -i", "sed --in-place", 1)), "k:"},
		{"k/grep-missing", change(clusterCheck, ""), "k:"},
		{"k/grep-before-sed", change(clusterEdit+clusterCheck, clusterCheck+clusterEdit), "k:"},
		{"k/no-errexit-run", change(debian, strings.Replace(clusterRun, "set -eu; ", "", 1)+keyRun), "k:"},
		{"k/set-plus-e", change(debian, strings.Replace(clusterRun, "set -eu;", "set -eu; set +e;", 1)+keyRun), "k:"},
		{"k/no-cluster-check", change(clusterCheck, ""), "k:"},
		{"k/cluster-check-other-run", change(clusterCheck, "") + "RUN " + clusterCheck + "\n", "k:"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			problems := checkVerifyImageRecipe(tt.recipe, digest, "v1.2.3")
			if (tt.want == "" && len(problems) != 0) || (tt.want != "" && (len(problems) != 1 || !strings.HasPrefix(problems[0], tt.want))) {
				t.Fatalf("want %q, got %v", tt.want, problems)
			}
		})
	}
	t.Run("real-file", func(t *testing.T) {
		_, digest, _ := strings.Cut(containerBaseImage, "@")
		if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(digest) {
			t.Fatal("containerBaseImage has no full digest")
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
		if strings.Count(string(recipe), approvedPGDGFingerprint) != 1 {
			t.Fatal("real recipe must contain the approved PGDG fingerprint exactly once")
		}
		docs, err := os.ReadFile("../../docs/workflow/argon-verify-image.md")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(docs), approvedPGDGFingerprint) {
			t.Fatal("workflow docs must contain the approved PGDG fingerprint")
		}
		if problems := checkVerifyImageRecipe(string(recipe), digest, string(version[1])); len(problems) != 0 {
			t.Fatal("real recipe:", problems)
		}
	})
}
