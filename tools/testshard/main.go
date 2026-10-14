// Command testshard splits the application module's Go test packages
// between CI's parallel jobs. It prints one shard's package patterns for
// make check-test; -check fails unless the shards together list every
// package of go list ./... exactly once. The named shards hold the slowest
// packages; "rest" is everything else, so a package added later runs in CI
// without editing this list or the workflow. make check runs -check.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
)

// The named shards hold the packages that take longest on CI (#992), so
// each job finishes at about the same time; "rest" runs in the checks job.
var shards = []struct {
	name     string
	packages []string
}{
	{"loadgen", []string{"./cmd/loadgen", "./cmd/seed"}},
	{"web", []string{"./cmd/ribbitto", "./internal/web"}},
}

const rest = "rest"

func main() {
	check := flag.Bool("check", false, "fail unless the shards list every package exactly once")
	flag.Parse()
	if *check && flag.NArg() == 1 || !*check && flag.NArg() == 2 {
		if err := run(flag.Arg(0), flag.Arg(1), *check); err != nil {
			fmt.Fprintln(os.Stderr, "testshard:", err)
			os.Exit(1)
		}
		return
	}
	fmt.Fprintln(os.Stderr, "usage: testshard <repository root> <shard> | testshard -check <repository root>")
	os.Exit(2)
}

func run(root, shard string, check bool) error {
	all, err := list(root)
	if err != nil {
		return err
	}
	parts, err := partition(all)
	if err != nil {
		return err
	}
	if check {
		return covers(parts, all)
	}
	packages, ok := parts[shard]
	if !ok {
		return fmt.Errorf("unknown shard %q", shard)
	}
	fmt.Println(strings.Join(packages, "\n"))
	return nil
}

// list returns the module's packages as ./-relative patterns, the form the
// named shards use.
func list(root string) ([]string, error) {
	out, err := goList(root, "-m")
	if err != nil {
		return nil, err
	}
	module := strings.TrimSpace(out)
	paths, err := goList(root, "./...")
	if err != nil {
		return nil, err
	}
	var all []string
	for _, path := range strings.Fields(paths) {
		switch {
		case path == module:
			all = append(all, ".")
		case strings.HasPrefix(path, module+"/"):
			all = append(all, "./"+strings.TrimPrefix(path, module+"/"))
		default:
			return nil, fmt.Errorf("package %s is outside module %s", path, module)
		}
	}
	return all, nil
}

func goList(root string, args ...string) (string, error) {
	cmd := exec.Command("go", append([]string{"list"}, args...)...)
	cmd.Dir = root
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go list %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// partition assigns every package to one shard. A named package that no
// longer exists is an error, so a rename cannot leave a shard quietly empty.
func partition(all []string) (map[string][]string, error) {
	parts := map[string][]string{}
	named := map[string]string{}
	for _, s := range shards {
		for _, p := range s.packages {
			if other, ok := named[p]; ok {
				return nil, fmt.Errorf("%s is in shards %s and %s", p, other, s.name)
			}
			if !slices.Contains(all, p) {
				return nil, fmt.Errorf("shard %s names %s, which go list ./... does not list", s.name, p)
			}
			named[p] = s.name
			parts[s.name] = append(parts[s.name], p)
		}
	}
	parts[rest] = []string{}
	for _, p := range all {
		if _, ok := named[p]; !ok {
			parts[rest] = append(parts[rest], p)
		}
	}
	return parts, nil
}

// covers checks the partition itself, so a later change to partition cannot
// drop or repeat a package unnoticed.
func covers(parts map[string][]string, all []string) error {
	seen := map[string]string{}
	for name, packages := range parts {
		for _, p := range packages {
			if other, ok := seen[p]; ok {
				return fmt.Errorf("%s is in shards %s and %s", p, other, name)
			}
			seen[p] = name
		}
	}
	for _, p := range all {
		if _, ok := seen[p]; !ok {
			return fmt.Errorf("%s is in no shard", p)
		}
	}
	if len(seen) != len(all) {
		return fmt.Errorf("shards list %d packages, go list ./... lists %d", len(seen), len(all))
	}
	return nil
}
