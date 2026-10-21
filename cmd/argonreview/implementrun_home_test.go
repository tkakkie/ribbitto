package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImplementRunHomeUnderAccount(t *testing.T) {
	x := newImplementRun(t)
	home := filepath.Join(x.f.account, "dev/ribbitto/agy-worker-home")
	implementCheckMkdir(t, filepath.Dir(home))
	if err := os.Rename(x.f.home, home); err != nil {
		t.Fatal(err)
	}
	x.f.home = home
	x.set("home", home)
	hooks, err := implementerHooks(false, "/trusted/argonreview", filepath.Join(home, "guard"))
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"implementer/hooks.json", ".gemini/config/hooks.json"} {
		write(t, filepath.Join(home, rel), string(hooks))
	}
	// Replaces the old nested-account refusal: a sibling of shared .gemini
	// under the account home is the production worker layout.
	x.run(t, 0)
	x.cleaned(t)
}

func TestImplementRunHomeSeparation(t *testing.T) {
	const (
		textual     = "must not equal or contain calling account home, or overlap shared .gemini"
		accountHome = "refusing --home (implementer home): it is the account's home or an ancestor of it"
		sharedHome  = "refusing --home (implementer home): it overlaps the account's shared .gemini"
		symlinkHome = "opening --home (implementer home): Alias: not a directory"
	)
	for _, scope := range []struct {
		name string
		want [4]string
	}{
		{"account", [4]string{textual, accountHome, symlinkHome, accountHome}},
		{"ancestor", [4]string{textual, accountHome, symlinkHome, accountHome}},
		{"gemini", [4]string{textual, sharedHome, symlinkHome, sharedHome}},
		// Config's parent still spells .gemini identically, so the textual check wins.
		{"inside-gemini", [4]string{textual, sharedHome, symlinkHome, textual}},
		{"contains-gemini", [4]string{sharedHome, sharedHome, symlinkHome, sharedHome}},
	} {
		for i, alias := range []string{"literal", "account-symlink", "home-symlink", "case"} {
			t.Run(scope.name+"/"+alias, func(t *testing.T) {
				// A system temp-directory symlink must not mask the tested alias.
				base, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				root := filepath.Join(base, "Scope")
				account := filepath.Join(root, "Account")
				gemini := filepath.Join(account, ".gemini")
				implementCheckMkdir(t, filepath.Join(gemini, "Config"))
				home := map[string]string{
					"account": account, "ancestor": root, "gemini": gemini,
					"inside-gemini":   filepath.Join(gemini, "Config"),
					"contains-gemini": filepath.Join(root, "Worker"),
				}[scope.name]
				if scope.name == "contains-gemini" {
					// Isolate shared containment from account containment: shared
					// .gemini lives outside the account via a symlink.
					implementCheckMkdir(t, home)
					if err := os.Rename(gemini, filepath.Join(home, "Shared")); err != nil {
						t.Fatal(err)
					}
					symlink(t, filepath.Join(home, "Shared"), gemini)
				}
				switch alias {
				case "account-symlink":
					link := filepath.Join(filepath.Dir(root), "Alias")
					symlink(t, account, link)
					account = link
				case "home-symlink":
					// walk refuses the alias before directory identities are compared.
					link := filepath.Join(filepath.Dir(root), "Alias")
					symlink(t, home, link)
					home = link
				case "case":
					other := filepath.Join(filepath.Dir(home), strings.ToUpper(filepath.Base(home)))
					original, err := os.Stat(home)
					if err != nil {
						t.Fatal(err)
					}
					info, err := os.Stat(other)
					if os.IsNotExist(err) {
						t.Skip("the file system is case-sensitive")
					}
					if err != nil || !os.SameFile(original, info) {
						t.Fatalf("case alias identity: %v", err)
					}
					home = other
				}
				r := &implementRun{home: home, deps: implementRunDeps{accountHome: account}}
				err = r.separateHome()
				if err == nil || !strings.Contains(err.Error(), scope.want[i]) {
					t.Fatalf("separateHome() = %v, want refusal containing %q", err, scope.want[i])
				}
				if !strings.Contains(err.Error(), "--home") || strings.Contains(err.Error(), "RIBBITTO_ARGON_HOME") {
					t.Fatalf("refusal names the wrong home setting: %v", err)
				}
			})
		}
	}
}

func TestImplementRunReadOnlyAgentLeftover(t *testing.T) {
	x := newImplementRun(t)
	agent := filepath.Join(x.f.home, ".gemini/config/agents/argon-implementer.md")
	write(t, agent, "trusted agent\n")
	if err := os.Chmod(agent, 0o400); err != nil {
		t.Fatal(err)
	}
	leftover, err := os.Open(agent)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = leftover.Close() }()
	before, err := leftover.Stat()
	if err != nil {
		t.Fatal(err)
	}
	check := x.deps.check
	x.deps.check = func(args []string, out, stderr io.Writer) int {
		if strings.Contains(strings.Join(x.order, ","), "place-agent") && !strings.Contains(strings.Join(x.order, ","), "remove-agent") {
			info, err := os.Stat(agent)
			if err != nil || info.Mode().Perm() != 0o400 || os.SameFile(before, info) {
				t.Fatalf("agent not replaced at mode 0400: %v %v", info, err)
			}
		}
		return check(args, out, stderr)
	}
	x.run(t, 0)
	x.cleaned(t)
}
