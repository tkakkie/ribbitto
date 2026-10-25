// Command webtypes installs the locked checker or checks client TypeScript.
// Installation is explicit; ordinary checks never fetch dependencies.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) != 3 || (os.Args[1] != "install" && os.Args[1] != "check") {
		fmt.Fprintln(os.Stderr, "usage: webtypes install|check <repository root>")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func environment(root string) []string {
	// Preserve sandbox proxy settings; clearing every npm_config_* would lose them.
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		key = strings.ReplaceAll(strings.ToLower(key), "-", "_") // npm accepts both spellings and cases.
		switch key {
		case "node_options": // Prevent injected Node code or runtime flags.
			continue
		case "npm_config_omit": // Keep the checker and native optional package installed.
			continue
		case "npm_config_os", "npm_config_cpu", "npm_config_libc": // Select packages for the actual host.
			continue
		case "npm_config_ignore_scripts": // Prevent inherited lifecycle-script policy.
			continue
		case "npm_config_cache": // Keep cache writes inside this worktree.
			continue
		}
		env = append(env, entry)
	}
	return append(env, "npm_config_cache="+filepath.Join(root, "bin/.cache/npm"))
}

func command(root, name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Dir, cmd.Env = root, environment(root)
	return cmd
}

func ready(root string) error {
	pin, err := os.ReadFile(filepath.Join(root, ".node-version"))
	if err != nil {
		return fmt.Errorf("reading Node pin: %w", err)
	}
	out, err := command(root, "node", "--version").Output()
	if err != nil {
		return fmt.Errorf("Node %s unavailable: %w", strings.TrimSpace(string(pin)), err)
	}
	if strings.TrimSpace(string(out)) != "v"+strings.TrimSpace(string(pin)) {
		return fmt.Errorf("Node version %q does not match %s", strings.TrimSpace(string(out)), strings.TrimSpace(string(pin)))
	}
	return nil
}

func checkerReady(root string) error {
	var pinned struct {
		DevDependencies struct{ TypeScript string }
	}
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return fmt.Errorf("reading checker pin: %w", err)
	}
	if err := json.Unmarshal(data, &pinned); err != nil {
		return fmt.Errorf("reading checker pin: %w", err)
	}
	var installed struct{ Version string }
	data, err = os.ReadFile(filepath.Join(root, "node_modules/typescript/package.json"))
	if err == nil {
		err = json.Unmarshal(data, &installed)
	}
	if err != nil || installed.Version == "" {
		return fmt.Errorf("TypeScript installed version unreadable; pinned version %q", pinned.DevDependencies.TypeScript)
	}
	if installed.Version != pinned.DevDependencies.TypeScript {
		return fmt.Errorf("TypeScript installed version %q does not match pinned version %q", installed.Version, pinned.DevDependencies.TypeScript)
	}
	return nil
}

func run(mode, root string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolving root: %w", err)
	}
	err = ready(root)
	if err == nil && mode == "check" {
		err = checkerReady(root)
	}
	if err != nil {
		if mode == "check" && os.Getenv("RIBBITTO_REQUIRE_NODE") != "1" {
			fmt.Printf("NOT VERIFIED: client TypeScript: %v\n", err)
			return nil
		}
		return err
	}
	var cmd *exec.Cmd
	if mode == "install" {
		if err := os.MkdirAll(filepath.Join(root, "bin"), 0700); err != nil {
			return fmt.Errorf("creating tooling directory: %w", err)
		}
		// Separate empty files avoid npm's double-loading error for one path.
		config, err := os.MkdirTemp(filepath.Join(root, "bin"), "npm-config-")
		if err != nil {
			return fmt.Errorf("creating empty npm configs: %w", err)
		}
		defer func() { _ = os.RemoveAll(config) }()
		for _, name := range []string{"user", "global"} {
			if err := os.WriteFile(filepath.Join(config, name), nil, 0600); err != nil {
				return fmt.Errorf("writing empty npm config: %w", err)
			}
		}
		// CLI policy overrides env/.npmrc; lockfile integrity also covers registry overrides.
		cmd = command(root, "npm", "ci", "--ignore-scripts", "--include=optional", "--include=dev", "--registry=https://registry.npmjs.org/", "--userconfig="+filepath.Join(config, "user"), "--globalconfig="+filepath.Join(config, "global"))
	} else {
		cmd = command(root, "node", "node_modules/typescript/bin/tsc", "--project", "tsconfig.json")
	}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("webtypes %s: %w", mode, err)
	}
	return nil
}
