package main

import (
	"strings"
	"testing"
)

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
			live, err := s.live(c)
			if err != nil || live != tc.trusted {
				t.Errorf("live=%v, err=%v, want %v", live, err, tc.trusted)
			}
			s.clients()
			if s.err != nil || (len(table.signalled) != 0) != tc.trusted {
				t.Fatalf("signalled=%v err=%v report=%s", table.signalled, s.err, out)
			}
		})
	}
}
