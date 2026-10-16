// Command svelte builds the composer island of #1152's prototype: it bundles
// web/islands/src/composer-v1.js with esbuild's Go API into one minified
// IIFE in web/static/islands, compiling each .svelte file through the
// embedded compile shim with the pinned Node. It uses the packages the locked
// npm install placed in web/islands/node_modules and never installs them.
// Run it with the repository root as its argument. Prototype only: #1187
// builds the production version.
package main

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

const node = "bin/node-v24.21.0/bin/node"

//go:embed compile.mjs
var shim string

var entries = []string{"composer-v1.js"}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: svelte <repository root>")
		os.Exit(2)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "svelte:", err)
		os.Exit(1)
	}
}

func run(root string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolving the root: %w", err)
	}
	islands := filepath.Join(root, "web", "islands")
	out := filepath.Join(root, "web", "static", "islands")
	if err := os.MkdirAll(out, 0o755); err != nil {
		return fmt.Errorf("creating the output directory: %w", err)
	}
	for _, entry := range entries {
		code, err := build(filepath.Join(root, node), islands, "src/"+entry)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(out, entry), code, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", entry, err)
		}
	}
	licence, err := os.ReadFile(filepath.Join(islands, "node_modules", "svelte", "LICENSE.md"))
	if err != nil {
		return fmt.Errorf("reading Svelte's licence: %w", err)
	}
	if err := os.WriteFile(filepath.Join(out, "svelte-LICENSE"), licence, 0o644); err != nil {
		return fmt.Errorf("writing Svelte's licence: %w", err)
	}
	return nil
}

// build bundles one entry. Relative paths and no timestamps keep the output
// byte-identical across machines.
func build(node, islands, entry string) ([]byte, error) {
	result := api.Build(api.BuildOptions{
		AbsWorkingDir:     islands,
		EntryPoints:       []string{entry},
		Bundle:            true,
		Format:            api.FormatIIFE,
		Platform:          api.PlatformBrowser,
		Target:            api.ES2022,
		Conditions:        []string{"browser", "production"},
		MinifyWhitespace:  true,
		MinifyIdentifiers: true,
		MinifySyntax:      true,
		// The leak probe finds the component's marker class by name.
		KeepNames:     true,
		LegalComments: api.LegalCommentsNone,
		Charset:       api.CharsetUTF8,
		Write:         false,
		Outfile:       "out.js",
		Plugins:       []api.Plugin{compiler(node, islands)},
	})
	if len(result.Errors) > 0 {
		messages := api.FormatMessages(result.Errors, api.FormatMessagesOptions{Kind: api.ErrorMessage})
		return nil, fmt.Errorf("building %s:\n%s", entry, strings.Join(messages, ""))
	}
	if len(result.Warnings) > 0 {
		messages := api.FormatMessages(result.Warnings, api.FormatMessagesOptions{Kind: api.WarningMessage})
		return nil, fmt.Errorf("building %s:\n%s", entry, strings.Join(messages, ""))
	}
	if len(result.OutputFiles) != 1 {
		return nil, fmt.Errorf("building %s: %d output files, want one", entry, len(result.OutputFiles))
	}
	return result.OutputFiles[0].Contents, nil
}

// compiler compiles .svelte files through the shim, run in web/islands so
// that it resolves the locked compiler and svelte.config.js.
func compiler(node, islands string) api.Plugin {
	return api.Plugin{
		Name: "svelte",
		Setup: func(b api.PluginBuild) {
			b.OnLoad(api.OnLoadOptions{Filter: `\.svelte$`}, func(args api.OnLoadArgs) (api.OnLoadResult, error) {
				name, err := filepath.Rel(islands, args.Path)
				if err != nil || strings.HasPrefix(name, "..") {
					return api.OnLoadResult{}, fmt.Errorf("%s is outside web/islands", args.Path)
				}
				cmd := exec.Command(node, "--input-type=module", "--eval", shim, filepath.ToSlash(name))
				cmd.Dir = islands
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				if err := cmd.Run(); err != nil {
					var exit *exec.ExitError
					if errors.As(err, &exit) {
						return api.OnLoadResult{}, fmt.Errorf("compiling %s:\n%s", name, stderr.String())
					}
					return api.OnLoadResult{}, fmt.Errorf("compiling %s: %w", name, err)
				}
				contents := stdout.String()
				return api.OnLoadResult{Contents: &contents, Loader: api.LoaderJS, ResolveDir: filepath.Dir(args.Path)}, nil
			})
		},
	}
}
