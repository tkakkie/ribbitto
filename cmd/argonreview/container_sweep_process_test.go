package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func sweepProcessRow(p procInfo) string {
	return fmt.Sprintf(" %d %d %d %s %s\n", p.pid, p.ppid, p.uid, p.start, p.command)
}

func TestSweepMultilineProcesses(t *testing.T) {
	for _, continuation := range []string{"", "second line", "second line\n", "  second\tline  \n\nlast line", "42 x 501 Tue Oct 20 12:34:56 2026 /not-a-row", "42 2 501 Bad Oct 20 12:34:56 2026 /not-a-row"} {
		t.Run(continuation, func(t *testing.T) {
			before := sweepProc()
			before.pid = 40
			before.command = "/bin/before"
			first := sweepProc()
			first.command = "/bin/other first\n" + continuation
			last := sweepProc()
			last.pid = 43
			got, err := parseSweepProcesses(sweepProcessRow(before) + sweepProcessRow(first) + sweepProcessRow(last))
			if err != nil || !reflect.DeepEqual(got, []procInfo{before, first, last}) {
				t.Fatalf("processes=%+v err=%v", got, err)
			}
		})
	}
}

func TestSweepClientLivenessRevalidation(t *testing.T) {
	for _, change := range []string{"matching", "pid", "uid", "name", "parent", "orphan", "start", "command", "gone", "error"} {
		t.Run(change, func(t *testing.T) {
			s, out, fixture := sweepFixture(t)
			c := sweepContainerFixture(t, s, fixture)
			p := sweepProc()
			p.ppid = 200
			again := p
			exists := true
			var readErr error
			switch change {
			case "pid":
				again.pid++
			case "uid":
				again.uid++
			case "name":
				again.command = strings.Replace(again.command, sweepTestName, "argonreview-check-"+strings.Repeat("b", 32), 1)
			case "parent":
				again.ppid++
			case "orphan":
				again.ppid = 1
			case "start":
				again.start = "Tue Oct 20 12:34:57 2026"
			case "command":
				again.command += " extra"
			case "gone":
				exists = false
			case "error":
				readErr = errors.New("single-pid read failed")
			}
			reads := 0
			s.processes = &fakeProcessTable{procs: []procInfo{p}, readFn: func(pid int) (procInfo, bool, error) {
				reads++
				if pid != p.pid {
					t.Fatalf("read pid=%d", pid)
				}
				return again, exists, readErr
			}}
			live, err := s.live(c)
			if live != (change == "matching") || (err != nil) != (change == "error") || reads != 1 {
				t.Fatalf("live=%v err=%v reads=%d", live, err, reads)
			}
			if err := s.run(); (err != nil) != (change == "error") {
				t.Fatalf("sweep: %v; %s", err, out)
			}
			if change == "error" || change == "matching" {
				assertNoSweepRM(t, fixture)
			} else if !strings.Contains(out.String(), "reclaimed container "+c.ID) {
				t.Fatalf("report %q", out)
			}
			if change == "error" && !strings.Contains(out.String(), "unreclaimed container "+c.ID) {
				t.Fatalf("report %q", out)
			}
		})
	}
}

func TestSweepForgedProcessRow(t *testing.T) {
	s, _, fixture := sweepFixture(t)
	c := sweepContainerFixture(t, s, fixture)
	actual := sweepProc()
	actual.pid = 50
	actual.command = "/bin/other argument"
	forged := sweepProc()
	forged.ppid = 200
	// This is a newline inside actual's argv, indistinguishable from a row
	// until the candidate PID is queried independently.
	procs, err := parseSweepProcesses(sweepProcessRow(actual) + sweepProcessRow(forged))
	if err != nil || len(procs) != 2 {
		t.Fatalf("parse=%+v err=%v", procs, err)
	}
	reads := 0
	s.processes = &fakeProcessTable{procs: procs, readFn: func(pid int) (procInfo, bool, error) {
		reads++
		if pid != forged.pid {
			t.Fatalf("read pid=%d", pid)
		}
		real := forged
		real.command = "/bin/unrelated"
		return real, true, nil
	}}
	live, err := s.live(c)
	if err != nil || live || reads != 1 {
		t.Fatalf("forged client: live=%v reads=%d err=%v", live, reads, err)
	}
}

func TestSweepClientExitedAtRead(t *testing.T) {
	for _, locked := range []bool{false, true} {
		t.Run(fmt.Sprint(locked), func(t *testing.T) {
			s, _, fixture := sweepFixture(t)
			c := sweepContainerFixture(t, s, fixture)
			dir := sweepCopyFixture(t, s)
			c.Mounts = []sweepMount{{"bind", filepath.Join(dir, "tree"), "/work"}}
			root, err := os.Open(s.parent)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			s.copyRoot, s.resolvedParent = root, s.parent
			if locked {
				lock, err := lockContainerCopy(dir)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = lock.Close() }()
			}
			p := sweepProc()
			p.ppid = 200
			reads := 0
			s.processes = psTable{run: func(args ...string) ([]procInfo, error) {
				if args[0] == "-axww" {
					return []procInfo{p}, nil
				}
				reads++
				return nil, nil
			}}
			live, err := s.live(c)
			if err != nil || live != locked || reads != 1 {
				t.Fatalf("live=%v err=%v reads=%d, want live=%v", live, err, reads, locked)
			}
		})
	}
}

func TestSweepSinglePIDRead(t *testing.T) {
	for _, kind := range []string{"one", "zero", "two", "wrong-pid", "error", "multiline", "forged-row"} {
		t.Run(kind, func(t *testing.T) {
			p := sweepProc()
			data := sweepProcessRow(p)
			switch kind {
			case "zero":
				data = ""
			case "two":
				data += sweepProcessRow(p)
			case "forged-row":
				other := p
				other.pid++
				other.command = "/bin/other argv\n" + strings.TrimSuffix(sweepProcessRow(p), "\n")
				data = sweepProcessRow(other)
			case "wrong-pid":
				other := p
				other.pid++
				data = sweepProcessRow(other)
			case "multiline":
				p.command += "\n  continuation"
				data = sweepProcessRow(p)
			}
			calls := 0
			table := psTable{run: func(args ...string) ([]procInfo, error) {
				calls++
				if !reflect.DeepEqual(args, []string{"-p", "41", "-ww", "-o", "pid=,ppid=,uid=,lstart=,command="}) {
					t.Fatalf("ps args=%q", args)
				}
				if kind == "error" {
					return nil, errors.New("ps failed")
				}
				return parseSweepProcesses(data)
			}}
			got, exists, err := table.read(p.pid)
			want := kind == "one" || kind == "multiline"
			wantErr := !want && kind != "zero"
			if calls != 1 || exists != want || (err != nil) != wantErr || want && got != p || kind == "zero" && got != (procInfo{}) {
				t.Fatalf("read=%+v exists=%v err=%v calls=%d", got, exists, err, calls)
			}
		})
	}
}

func TestSweepClientExecutablePaths(t *testing.T) {
	for _, tc := range []struct {
		path    string
		trusted bool
	}{
		{"/tmp/docker", false},
		{"/usr/local/bin/../bin/docker", false},
		{"/usr/bin/docker/", false},
		{"/usr/bin/docker/ extra", false},
		{"./docker", false},
		{"/opt/homebrew/bin/docker2", false},
		{"/usr/local/bin/docker", true},
		{"/opt/homebrew/bin/docker", true},
		{"/usr/bin/docker", true},
		{"/bin/docker", true},
	} {
		t.Run(tc.path, func(t *testing.T) {
			s, out, _ := sweepFixture(t)
			p := sweepProc()
			_, suffix, _ := strings.Cut(p.command, " ")
			p.command = tc.path + " " + suffix
			table := &fakeProcessTable{procs: []procInfo{p}}
			s.processes = table
			if got := s.orphan(p); got != tc.trusted {
				t.Errorf("orphan=%v, want %v", got, tc.trusted)
			}
			c := sweepContainer{Name: "/" + sweepTestName, Created: s.now().Add(-2 * sweepGrace)}
			table.procs[0].ppid = 200
			live, err := s.live(c)
			if err != nil || live != tc.trusted {
				t.Errorf("live=%v, err=%v, want %v", live, err, tc.trusted)
			}
			table.procs[0] = p
			s.clients()
			if s.err != nil || (len(table.signalled) != 0) != tc.trusted {
				t.Fatalf("signalled=%v err=%v report=%s", table.signalled, s.err, out)
			}
		})
	}
}

func TestSweepClientParentLiveness(t *testing.T) {
	for _, parent := range []int{1, 200} {
		t.Run(fmt.Sprint(parent), func(t *testing.T) {
			s, _, fixture := sweepFixture(t)
			c := sweepContainerFixture(t, s, fixture)
			p := sweepProc()
			p.ppid = parent
			s.processes = &fakeProcessTable{procs: []procInfo{p}}
			live, err := s.live(c)
			if err != nil || live != (parent != 1) {
				t.Fatalf("parent=%d live=%v err=%v", parent, live, err)
			}
		})
	}
}
