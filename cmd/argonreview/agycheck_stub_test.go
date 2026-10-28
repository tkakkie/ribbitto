package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"testing"
)

func agyCheckRPC(method, params string) string {
	if params == "" {
		params = "{}"
	}
	return `{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":` + params + "}\n"
}
func agyCheckInitialize() string {
	return agyCheckRPC("initialize", `{"protocolVersion":"2025-06-18"}`)
}

func TestAgyCheckStubProtocol(t *testing.T) {
	for _, tc := range []struct {
		name, input, tag, contains string
		code, records              int
	}{
		{"eof", "", "", "", 0, 1},
		{"initialize", agyCheckInitialize(), "initialize", `"protocolVersion":"2025-06-18"`, 0, 3},
		{"fallback", agyCheckRPC("initialize", `{"protocolVersion":"unknown"}`), "initialize", `"protocolVersion":"2025-06-18"`, 0, 3},
		{"initialized", `{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n", "initialized", "", 0, 2},
		{"ping", agyCheckRPC("ping", ""), "ping", `"result":{}`, 0, 3},
		{"toolslist", agyCheckInitialize() + agyCheckRPC("tools/list", ""), "toolslist", `"name":"danger"`, 0, 5},
		{"verify", agyCheckInitialize() + agyCheckRPC("tools/call", `{"name":"verify","arguments":{}}`), "verify", "agy-check fixture response", 0, 5},
		{"danger", agyCheckInitialize() + agyCheckRPC("tools/call", `{"name":"danger"}`), "danger", "agy-check fixture response", 0, 5},
		{"list", agyCheckInitialize() + agyCheckRPC("resources/list", ""), "list", `"resources":[`, 0, 5},
		{"read", agyCheckInitialize() + agyCheckRPC("resources/read", `{"uri":"ribbitto-check://fixture"}`), "read", "agy-check fixture resource", 0, 5},
		{"unknown", agyCheckRPC(`untrusted-method\u001b`, ""), "", `"code":-32601`, 0, 1},
		{"malformed", "{\n", "", `"code":-32700`, 0, 1},
		{"oversize", strings.Repeat("x", verifyLineLimit+1) + "\n", "", "", 1, 1},
		{"not-initialized", agyCheckRPC("tools/list", ""), "toolslist", "initialize required", 0, 3},
		{"twice", agyCheckInitialize() + agyCheckInitialize(), "initialize", "already initialized", 0, 5},
		{"invalid-init", agyCheckRPC("initialize", ""), "initialize", "requires string", 0, 3},
		{"invalid-list", agyCheckInitialize() + agyCheckRPC("tools/list", `{"cursor":"x"}`), "toolslist", "invalid list", 0, 5},
		{"invalid-call", agyCheckInitialize() + agyCheckRPC("tools/call", `{"name":"arbitrary"}`), "call", "unknown fixture tool", 0, 5},
		{"invalid-arguments", agyCheckInitialize() + agyCheckRPC("tools/call", `{"name":"verify","arguments":null}`), "verify", "invalid arguments", 0, 5},
		{"invalid-read", agyCheckInitialize() + agyCheckRPC("resources/read", `{"uri":"elsewhere"}`), "read", "unknown resource", 0, 5},
		{"notification", `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"verify"}}` + "\n", "", "", 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := agyCheckHookHarnessNew(t)
			h.d.stdin = strings.NewReader(tc.input)
			var out, stderr bytes.Buffer
			code := agyCheckStub([]string{agyCheckHookTestDir}, &out, &stderr, agyCheckStubDeps{h.d})
			if code != tc.code || !strings.Contains(out.String(), tc.contains) || len(h.files) != tc.records {
				t.Fatalf("stub protocol/record mismatch: code=%d records=%d out=%q err=%q", code, len(h.files), out.String(), stderr.String())
			}
			if h.files[agyCheckHookTestDir+"/calls/000001"].String() != "start -\n" {
				t.Fatal("stub start record missing")
			}
			if tc.tag != "" {
				last := h.files[agyCheckHookTestDir+"/calls/"+formatAgyCheckNumber(tc.records)]
				kind := "response"
				if tc.name == "initialized" {
					kind = "request"
				}
				if last == nil || last.String() != kind+" "+tc.tag+"\n" {
					t.Fatal("stub used raw method or lost response record")
				}
			}
			for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
				if line != "" && !json.Valid([]byte(line)) {
					t.Fatal("stub response is not JSONL")
				}
			}
		})
	}
}
func formatAgyCheckNumber(n int) string {
	return fmt.Sprintf("%06d", n)
}

type agyCheckStubReadCheck struct {
	io.Reader
	check func()
}

func (r agyCheckStubReadCheck) Read(p []byte) (int, error) { r.check(); return r.Reader.Read(p) }

type agyCheckStubWriteCheck struct {
	io.Writer
	check func()
}

func (w agyCheckStubWriteCheck) Write(p []byte) (int, error) { w.check(); return w.Writer.Write(p) }

func TestAgyCheckStubEvidenceOrder(t *testing.T) {
	for _, name := range []string{"normal", "output-failure", "start-failure", "request-failure", "response-failure", "input-failure", "cap"} {
		t.Run(name, func(t *testing.T) {
			h := agyCheckHookHarnessNew(t)
			firstRead := true
			h.d.stdin = agyCheckStubReadCheck{strings.NewReader(agyCheckRPC("ping", "")), func() {
				if firstRead && (len(h.files) != 1 || h.files[agyCheckHookTestDir+"/calls/000001"].String() != "start -\n") {
					t.Fatal("start must precede first stdin read")
				}
				firstRead = false
			}}
			create := h.d.create
			if strings.HasSuffix(name, "failure") && name != "output-failure" && name != "input-failure" {
				at := map[string]string{"start-failure": "000001", "request-failure": "000002", "response-failure": "000003"}[name]
				h.d.create = func(p string, flags int, mode os.FileMode) (io.WriteCloser, error) {
					if strings.HasSuffix(p, at) {
						return nil, os.ErrPermission
					}
					return create(p, flags, mode)
				}
			}
			if name == "input-failure" {
				h.d.stdin = &agyCheckHookReader{err: errors.New("read failure")}
			}
			if name == "cap" {
				h.d.create = func(string, int, os.FileMode) (io.WriteCloser, error) { return nil, os.ErrExist }
			}
			var out, stderr bytes.Buffer
			var writer io.Writer = agyCheckStubWriteCheck{&out, func() {
				if h.files[agyCheckHookTestDir+"/calls/000002"] == nil || h.files[agyCheckHookTestDir+"/calls/000002"].String() != "request ping\n" {
					t.Fatal("request must precede response bytes")
				}
				if h.files[agyCheckHookTestDir+"/calls/000003"] != nil {
					t.Fatal("response record must follow successful output")
				}
			}}
			if name == "output-failure" {
				writer = agyCheckFailWriter{}
			}
			code := agyCheckStub([]string{agyCheckHookTestDir}, writer, &stderr, agyCheckStubDeps{h.d})
			if name == "normal" {
				if code != 0 || len(h.files) != 3 {
					t.Fatal("normal stub exchange failed")
				}
				return
			}
			if code != 1 {
				t.Fatal("stub record/stdio failure looked normal")
			}
			if name != "response-failure" && out.Len() != 0 {
				t.Fatal("stub answered despite failed evidence")
			}
			if name == "output-failure" && h.files[agyCheckHookTestDir+"/calls/000003"] != nil {
				t.Fatal("response recorded after failed stdout")
			}
		})
	}
}

func TestAgyCheckStubState(t *testing.T) {
	for _, name := range []string{"root", "real-mismatch", "effective-mismatch", "lookup", "symlink-ancestor", "mode", "owner", "sentinel", "outside", "calls-mode", "sentinel-mode"} {
		t.Run(name, func(t *testing.T) {
			h := agyCheckHookHarnessNew(t)
			dir := agyCheckHookTestDir
			switch name {
			case "root":
				h.d.current = func() (*user.User, error) { return &user.User{Uid: "0"}, nil }
				h.d.uid = func() (int, int) { return 0, 0 }
			case "real-mismatch":
				h.d.uid = func() (int, int) { return 502, 501 }
			case "effective-mismatch":
				h.d.uid = func() (int, int) { return 501, 502 }
			case "lookup":
				h.d.current = func() (*user.User, error) { return nil, os.ErrPermission }
			case "symlink-ancestor":
				h.info["/fictional-check/middle"] = agyCheckInfo{os.ModeSymlink | 0o755, 0}
			case "mode":
				h.info[dir] = agyCheckInfo{os.ModeDir | 0o750, 501}
			case "owner":
				h.info[dir] = agyCheckInfo{os.ModeDir | 0o700, 502}
			case "sentinel":
				delete(h.info, dir+"/"+agyCheckHookSentinel)
			case "outside":
				dir = "/outside"
			case "calls-mode":
				h.info[dir+"/calls"] = agyCheckInfo{os.ModeDir | 0o750, 501}
			case "sentinel-mode":
				h.info[dir+"/"+agyCheckHookSentinel] = agyCheckInfo{0o640, 501}
			}
			// Outside is refused on the first missing component, before any write.
			if name == "outside" {
				h.d.lstat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
			}
			var out, stderr bytes.Buffer
			if code := agyCheckStub([]string{dir}, &out, &stderr, agyCheckStubDeps{h.d}); code != 1 || out.Len() != 0 || len(h.files) != 0 {
				t.Fatal("unsafe stub state accepted")
			}
		})
	}
}

func TestAgyCheckGeneratedProcesses(t *testing.T) {
	binary := agyCheckToolBinary(t)
	m, r, f := agyCheckTestRow(t)
	config, err := agyCheckConfig(true, "alternate", binary, r.State, r.Stub)
	if err != nil {
		t.Fatal(err)
	}
	var hooks map[string]map[string][]struct {
		Hooks []struct {
			Command string `json:"command"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(config.Hooks, &hooks); err != nil {
		t.Fatal(err)
	}
	command := hooks["ribbitto-implementer-guard"]["PreToolUse"][0].Hooks[0].Command
	const processes = 6
	commands := make([]*exec.Cmd, processes)
	for i := range commands {
		commands[i] = exec.Command("/bin/sh", "-c", command)
		commands[i].Stdin = strings.NewReader("{}")
		if err := commands[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	for _, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	claims, err := agyCheckReadClaims(m.Isolation, r.State, f)
	if err != nil {
		t.Fatal(err)
	}
	replies, err := agyCheckClaimReplies(claims, "-")
	if err != nil || len(replies) != processes {
		t.Fatalf("concurrent process claims lost: %v %v", replies, err)
	}
	for i, word := range replies {
		want := "allow"
		if i%2 == 1 {
			want = "deny"
		}
		if word != want || claims[i].N != i+1 {
			t.Fatal("concurrent claims not in number order")
		}
	}
	var mcp struct {
		Servers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(config.MCP, &mcp); err != nil {
		t.Fatal(err)
	}
	server := mcp.Servers[verifyServerName]
	cmd := exec.Command(server.Command, server.Args...)
	cmd.Stdin = strings.NewReader(agyCheckInitialize() + agyCheckRPC("tools/call", `{"name":"verify","arguments":{}}`))
	data, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(data), "agy-check fixture response") {
		t.Fatalf("generated stub command failed: %q %v", data, err)
	}
	records, err := agyCheckReadStubRecords(m.Isolation, r.Stub, f)
	if err != nil || len(records) != 5 || records[4] != (agyCheckStubRecord{5, "response", "verify"}) {
		t.Fatalf("generated stub evidence lost: %+v %v", records, err)
	}
	if err := f.cleanup(m.Isolation, r.Run); err != nil {
		t.Fatal(err)
	}
}

func TestAgyCheckStubBrokenPipe(t *testing.T) {
	binary := agyCheckToolBinary(t)
	m, r, f := agyCheckTestRow(t)
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := read.Close(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = write.Close() }()
	cmd := exec.Command(binary, "agy-check", "stub-mcp", r.Stub)
	cmd.Stdin = strings.NewReader(agyCheckInitialize())
	cmd.Stdout = write
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err = cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("broken pipe bypassed stub exit 1: %v %s", err, stderr.String())
	}
	records, err := agyCheckReadStubRecords(m.Isolation, r.Stub, f)
	if err != nil || len(records) != 2 || records[1].Kind != "request" {
		t.Fatalf("broken pipe recorded a response: %+v %v", records, err)
	}
}

func TestAgyCheckStubPrivatePredicates(t *testing.T) {
	for _, part := range []string{"", "/" + agyCheckHookSentinel, "/calls"} {
		for _, kind := range []string{"missing", "symlink", "owner", "mode", "type", "special-bits"} {
			label := "state"
			if part != "" {
				label = strings.TrimPrefix(part, "/")
			}
			t.Run(label+"/"+kind, func(t *testing.T) {
				h := agyCheckHookHarnessNew(t)
				path := agyCheckHookTestDir + part
				info := h.info[path]
				switch kind {
				case "missing":
					delete(h.info, path)
				case "symlink":
					info.mode = os.ModeSymlink | info.mode.Perm()
					h.info[path] = info
				case "owner":
					info.uid++
					h.info[path] = info
				case "mode":
					info.mode |= 0o040
					h.info[path] = info
				case "type":
					info.mode ^= os.ModeDir
					h.info[path] = info
				case "special-bits":
					info.mode |= os.ModeSetgid
					h.info[path] = info
				}
				var out, stderr bytes.Buffer
				code := agyCheckStub([]string{agyCheckHookTestDir}, &out, &stderr, agyCheckStubDeps{h.d})
				if code != 1 || out.Len() != 0 || len(h.files) != 0 {
					t.Fatal("unsafe stub private predicate accepted")
				}
			})
		}
	}
}
