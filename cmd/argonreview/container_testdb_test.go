package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestContainerTestDBEndToEnd(t *testing.T) {
	pkg := "github.com/tkakkie/ribbitto/cmd/ribbitto"
	pass := containerEvents(pkg, "TestOne", "pass") + containerEvents("q", "TestTwo", "pass")
	for _, tc := range []struct {
		name, output, want string
		status             int
	}{
		{"pass", pass, "verified\n2 passing leaf tests", 0},
		{"skip", containerEvents(pkg, "TestDB", "skip"), "NOT VERIFIED: 1 skipped\n" + pkg + "/TestDB", 0},
		{"measurement skip", containerJSON(pkg, "start", "run/TestFirstUnreadBench") + `{"Action":"output","Package":"` + pkg + `","Test":"TestFirstUnreadBench","Output":"set RIBBITTO_FIRST_UNREAD_BENCH=1 outside CI\n"}` + "\n" + containerJSON(pkg, "skip/TestFirstUnreadBench", "run/TestSibling", "pass/TestSibling", "pass"), "NOT VERIFIED: 1 skipped\n" + pkg + "/TestFirstUnreadBench", 0},
		{"failure", containerJSON(pkg, "start", "run/TestBad", "fail/TestBad", "fail"), "FAILED", 1},
		{"server did not start", "", "FAILED", containerTestDBStartFailed},
		{"empty stream", "", "FAILED: empty or truncated test stream", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var previous string
			for _, check := range []string{"test", "test-db"} {
				runner := fakeContainerRunner{func(_ context.Context, args, _ []string, stdout, _ io.Writer) (int, error) {
					if args[0] == "run" {
						_, _ = io.WriteString(stdout, tc.output)
						return tc.status, nil
					}
					return 0, nil
				}}
				report, err := runContainerCheck(context.Background(), containerFixture(t), check, "./...", runner, t.TempDir())
				verdict, _, _ := strings.Cut(report, "\n"+containerCheckScope(check))
				if err != nil || verdict != tc.want || previous != "" && verdict != previous {
					t.Fatalf("%s: verdict %q, want %q (previous %q), error %v", check, verdict, tc.want, previous, err)
				}
				previous = verdict
			}
		})
	}
}

const testDBScriptGolden = `set -eu
mkdir /tmp/work
cp -R /work/. /tmp/work
cd /tmp/work
/usr/local/bin/templ generate >&2
/usr/local/bin/sqlc generate >&2
/usr/local/bin/tailwindcss -i web/styles/app.css -o web/static/css/app.css --minify >&2
/usr/lib/postgresql/18/bin/initdb -D /home/check/pgdata -U postgres --auth=trust --no-sync >&2
/usr/lib/postgresql/18/bin/postgres -D /home/check/pgdata -c listen_addresses=127.0.0.1 -c unix_socket_directories=/tmp -c fsync=off -c synchronous_commit=off -c full_page_writes=off -c max_connections=100 >&2 &
pgpid=$!
stop_postgres() {
 kill -TERM "$pgpid" 2>/dev/null || :
 wait "$pgpid" || :
}
trap stop_postgres 0
ready=0
tries=0
while [ "$tries" -lt 100 ]; do
 if ! kill -0 "$pgpid" 2>/dev/null; then break; fi
 if /usr/lib/postgresql/18/bin/pg_isready -h 127.0.0.1 -p 5432 -U postgres >&2; then
  ready=1
  break
 fi
 tries=$((tries + 1))
 sleep 0.1
done
if [ "$ready" != 1 ]; then
 stop_postgres
 trap - 0
 exit 27
fi
status=0
go test -count=1 -json -p 2 "$1" || status=$?
stop_postgres
trap - 0
exit "$status"
`

func TestContainerTestDBScript(t *testing.T) {
	if containerTestDBScript != testDBScriptGolden {
		t.Fatal("fixed database script changed")
	}
	goTest := `go test -count=1 -json -p 2 "$1"`
	exit := "exit " + strconv.Itoa(containerTestDBStartFailed)
	if strings.Count(containerTestDBScript, goTest) != 1 || strings.Contains(containerTestDBScript, "-race") || !strings.Contains(containerTestDBScript, exit) || strings.Index(containerTestDBScript, exit) > strings.Index(containerTestDBScript, goTest) {
		t.Fatal("startup failure must exit before the single fixed go test command")
	}
	makefile, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(makefile), "\nGO_TEST_FLAGS ?= "+containerTestDBParallel+"\n") {
		t.Fatal("database concurrency drifted from Makefile")
	}
	for _, check := range verifyCheckNames() {
		args := containerArguments("copy", "name", check, "./internal/org/orgpg")
		if check == "test-db" {
			want := []string{"sh", "-c", testDBScriptGolden, "sh", "./internal/org/orgpg"}
			if !reflect.DeepEqual(args[len(args)-5:], want) {
				t.Fatalf("positional command: %q", args)
			}
		} else if strings.Contains(strings.Join(args, "\n"), "/usr/lib/postgresql/") {
			t.Fatalf("database script leaked into %s", check)
		}
	}
}

func TestContainerTestDBScriptShell(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh unavailable for fake database script execution")
	}
	for _, tool := range []string{"cp", "mkdir", "sleep"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s unavailable for fake database script execution", tool)
		}
	}
	for _, mode := range []string{"start failure", "success", "test failure"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			write(t, filepath.Join(dir, "source/input"), "source")
			fake := func(name, body string) string {
				path := filepath.Join(dir, name)
				write(t, path, "#!/bin/sh\nset -eu\n"+body)
				if err := os.Chmod(path, 0o700); err != nil {
					t.Fatal(err)
				}
				return "'" + strings.ReplaceAll(path, "'", "'\"'\"'") + "'"
			}
			tool := fake("tool", "echo generator\n")
			initdb := fake("initdb", "echo initdb\n")
			postgres := fake("postgres", `echo server
if [ "$FAKE_MODE" = 'start failure' ]; then exit 1; fi
trap 'echo stopped >> "$FAKE_DIR/trace"; exit 0' TERM
echo ready > "$FAKE_DIR/ready"
while :; do sleep 0.01; done
`)
			ready := fake("pg_isready", "echo readiness\n[ -f \"$FAKE_DIR/ready\" ]\n")
			goTool := fake("go", "echo go >> \"$FAKE_DIR/trace\"\necho 'go output'\nexit \"$FAKE_STATUS\"\n")
			script := strings.NewReplacer(
				containerTemplCommand, tool, containerSQLCCommand, tool, containerCSSCommand, tool,
				"/usr/lib/postgresql/18/bin/initdb", initdb, "/usr/lib/postgresql/18/bin/postgres", postgres,
				"/usr/lib/postgresql/18/bin/pg_isready", ready, "go test", goTool+" test",
				"/tmp/work", "\"$FAKE_DIR/work\"", "/work/.", "\"$FAKE_DIR/source/.\"",
			).Replace(containerTestDBScript)
			status := "0"
			if mode == "test failure" {
				status = "9"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script, "sh", "./pattern")
			cmd.Env = append(os.Environ(), "FAKE_DIR="+dir, "FAKE_MODE="+mode, "FAKE_STATUS="+status)
			cmd.WaitDelay = time.Second
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			code := 0
			var failure *exec.ExitError
			if errors.As(err, &failure) {
				code = failure.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			trace, _ := os.ReadFile(filepath.Join(dir, "trace"))
			wantCode, wantOut, wantTrace := 0, "go output\n", "go\nstopped\n"
			switch mode {
			case "start failure":
				wantCode, wantOut, wantTrace = containerTestDBStartFailed, "", ""
			case "test failure":
				wantCode = 9
			}
			if code != wantCode || stdout.String() != wantOut || string(trace) != wantTrace || !strings.Contains(stderr.String(), "generator\n") || !strings.Contains(stderr.String(), "initdb\n") || !strings.Contains(stderr.String(), "server\n") {
				t.Fatalf("code %d stdout %q stderr %q trace %q", code, &stdout, &stderr, trace)
			}
		})
	}
}
