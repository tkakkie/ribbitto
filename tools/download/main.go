// Command download installs pinned Tailwind and golangci-lint releases after
// checking their SHA-256 hashes. Make uses -paths to share these version pins.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const tailwindVersion = "v4.3.3"
const lintVersion = "2.14.0"

type asset struct {
	url, hash, path, member string
}

func paths() (string, string) {
	return "./bin/tailwindcss-" + tailwindVersion, "./bin/golangci-lint-v" + lintVersion + "/golangci-lint"
}

func main() {
	tool, root, showPaths, err := parse(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "usage: download <tailwind|golangci-lint> <repository root> | download -paths")
		os.Exit(2)
	}
	if showPaths {
		tailwind, lint := paths()
		fmt.Println(tailwind, lint)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := run(ctx, tool, root); err != nil {
		fmt.Fprintln(os.Stderr, "download:", err)
		os.Exit(1)
	}
}

func parse(args []string) (string, string, bool, error) {
	flags := flag.NewFlagSet("download", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	showPaths := flags.Bool("paths", false, "print versioned binary paths for Make")
	if err := flags.Parse(args); err != nil {
		return "", "", false, err
	}
	if *showPaths && flags.NArg() == 0 {
		return "", "", true, nil
	}
	if *showPaths || flags.NArg() != 2 || flags.Arg(1) == "" || (flags.Arg(0) != "tailwind" && flags.Arg(0) != "golangci-lint") {
		return "", "", false, fmt.Errorf("invalid arguments")
	}
	return flags.Arg(0), flags.Arg(1), false, nil
}

func selectAsset(tool, goos, goarch string) (asset, error) {
	platform := goos + "-" + goarch
	if platform != "darwin-arm64" && platform != "linux-amd64" {
		return asset{}, fmt.Errorf("unsupported platform: %s", platform)
	}
	tailwind, lint := paths()
	switch tool {
	case "tailwind":
		name, hash := "macos-arm64", "cdf646702987a743464dff4d9c60fd4480d1c1e73dd819a9a67f1078815dce9d"
		if goos == "linux" {
			name, hash = "linux-x64", "dc61b3ac6b8c9ca874c0cc4c57b2409791a64c5540404ca5f5367360babc313a"
		}
		return asset{"https://github.com/tailwindlabs/tailwindcss/releases/download/" + tailwindVersion + "/tailwindcss-" + name, hash, tailwind, ""}, nil
	case "golangci-lint":
		// From the official golangci-lint-2.14.0-checksums.txt release asset,
		// not a hash derived only from the archive we downloaded.
		hash := "5ef5f36a7147e91dc58ef9ef4d11bb7bad5ead0c76eb6c01327a73c641d1dcc3"
		if goos == "linux" {
			hash = "ab90aeb7b066f92a33415b638a50fe5344bbb75a0d32ad30cc248d88f81032ab"
		}
		name := "golangci-lint-" + lintVersion + "-" + platform
		return asset{"https://github.com/golangci/golangci-lint/releases/download/v" + lintVersion + "/" + name + ".tar.gz", hash, lint, name + "/golangci-lint"}, nil
	default:
		return asset{}, fmt.Errorf("unknown tool: %s", tool)
	}
}

func run(ctx context.Context, tool, root string) error {
	a, err := selectAsset(tool, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, a.url, nil)
	if err != nil {
		return fmt.Errorf("creating download request: %w", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", tool, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: HTTP %s", tool, response.Status)
	}
	// Bound memory even if a release endpoint sends an unexpectedly large body.
	data, err := io.ReadAll(io.LimitReader(response.Body, 256<<20))
	if err != nil {
		return fmt.Errorf("reading %s download: %w", tool, err)
	}
	return install(root, a, data)
}

func install(root string, a asset, data []byte) error {
	if fmt.Sprintf("%x", sha256.Sum256(data)) != a.hash {
		return fmt.Errorf("SHA-256 mismatch for %s", a.path)
	}
	path := filepath.Join(root, a.path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating binary directory: %w", err)
	}
	// A sibling temporary file keeps the final rename on the same filesystem.
	file, err := os.CreateTemp(filepath.Dir(path), ".download-*")
	if err != nil {
		return fmt.Errorf("creating temporary binary: %w", err)
	}
	defer func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}()
	if a.member == "" {
		_, err = file.Write(data)
	} else {
		err = extract(data, a.member, file)
	}
	if err != nil {
		return fmt.Errorf("writing binary: %w", err)
	}
	if err := file.Chmod(0o755); err != nil {
		return fmt.Errorf("making binary executable: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("closing binary: %w", err)
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return fmt.Errorf("installing binary: %w", err)
	}
	return nil
}

func extract(data []byte, member string, out io.Writer) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("opening gzip archive: %w", err)
	}
	defer func() { _ = gz.Close() }()
	archive := tar.NewReader(gz)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			return fmt.Errorf("archive missing %s", member)
		}
		if err != nil {
			return fmt.Errorf("reading tar archive: %w", err)
		}
		// Never unpack arbitrary paths or links supplied by an archive.
		if header.Name == member {
			if header.Typeflag != tar.TypeReg {
				return fmt.Errorf("archive member %s is not a regular file", member)
			}
			_, err := io.Copy(out, archive)
			return err
		}
	}
}
