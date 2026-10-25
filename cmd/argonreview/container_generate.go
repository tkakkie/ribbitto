package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
)

// The pinned Debian Go base supplies GNU find/cmp/tar. find -exec passes names
// as arguments; the list stays NUL-separated through tar, with no word splitting.
// GNU ustar refuses unrepresentable names instead of silently omitting them.
// Keep all logs off stdout, including failures after a partial archive.
// Handoff and write rules: docs/workflow/argon-verify-writeback.md.
const containerGenerateLocations = "old=/work new=/tmp/work names=/tmp/writeback-names\n"

const containerGenerateSelect = containerGenerateLocations + `cd "$new"
find . -type f -exec sh -c '
 old=$1
 shift
 for file do
  name=${file#./}
  if [ ! -e "$old/$name" ]; then
   printf "%s\0" "$name" || exit 34
  else
   status=0
   cmp -s -- "$old/$name" "$file" || status=$?
   case "$status" in
    0) ;;
    1) printf "%s\0" "$name" || exit 34 ;;
    *) exit 34 ;;
   esac
  fi
 done
' sh "$old" {} + > "$names" || exit 34
`

// Empty output needs only tar's two end blocks, independent of tar's policy
// for an empty file list. set -e also makes a failed zero-block write refuse.
const containerGenerateArchive = `if [ ! -s "$names" ]; then
 head -c 1024 /dev/zero
 exit 0
fi
exec tar --format=ustar --null --verbatim-files-from --no-recursion -cf - -T "$names"
`

const containerGenerateScript = containerCopyScript + containerTemplCommand + " >&2 || exit 31\n" + containerSQLCCommand + " >&2 || exit 32\n" + containerCSSCommand + " >&2 || exit 33\n" + containerGenerateSelect + containerGenerateArchive

func containerGenerateCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		_, _ = fmt.Fprintln(stderr, "usage: argonreview container-generate <worktree>")
		return 2
	}
	info, ok := debug.ReadBuildInfo()
	_, err := trusted(info, ok, func() (string, error) { return sourceRevision(os.Getenv("RIBBITTO_ARGON_SOURCE"), git) })
	return startContainerGenerate(args[0], stdout, stderr, err, sweepContainers, dockerRunner{}, containerCopyParent, nil)
}

func startContainerGenerate(tree string, stdout, stderr io.Writer, trustErr error, sweep func(io.Writer) error, runner containerRunner, copyParent string, hook func(string, string)) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var report string
	err := trustErr
	if err == nil {
		report, err = runContainerGenerate(ctx, tree, sweep, runner, copyParent, hook)
	}
	if report == "" {
		report = writebackReport(nil, nil, err)
	}
	// Container and source cleanup, then descriptor/temp cleanup, precede output.
	if _, writeErr := io.WriteString(stdout, report); writeErr != nil {
		err = errors.Join(err, fmt.Errorf("writing write-back report: %w", writeErr))
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "argonreview: container-generate failed; see refusal report")
		return 1
	}
	return 0
}

func runContainerGenerate(ctx context.Context, tree string, sweep func(io.Writer) error, runner containerRunner, copyParent string, hook func(string, string)) (report string, err error) {
	defer func() {
		if report == "" {
			report = writebackReport(nil, nil, err)
		}
	}()
	repo, err := prepareWritebackRepo(ctx, tree)
	if err != nil {
		return "", err
	}
	defer closeFD(repo.root)
	// Sweep diagnostics are trusted status, but not part of the bounded report.
	if err := sweep(io.Discard); err != nil {
		return "", fmt.Errorf("sweeping before generation: %w", err)
	}
	var result containerExecution
	_, err = runContainerExecution(ctx, tree, "writeback", "", runner, copyParent, &result)
	if result.output.over {
		return "", errors.Join(err, errors.New("container output limit exceeded (stdout and stderr share 4 MiB)"))
	}
	if err != nil {
		return "", err
	}
	if result.code != 0 {
		return "", fmt.Errorf("container generation exited with status %d", result.code)
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("generation interrupted: %w", err)
	}
	entries, err := parseWritebackArchive(bytes.NewReader(result.output.stdout.Bytes()))
	if err != nil {
		return "", err
	}
	// No generator or cleanup can now fail after a destination is replaced.
	return writebackFiles(tree, repo.root, entries, repo, hook)
}
