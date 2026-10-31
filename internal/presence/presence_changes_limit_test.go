package presence

import (
	"testing"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/realtime"
)

func TestChangesLimit(t *testing.T) {
	org, unknownOrg := kernel.ID{1}, kernel.ID{9}
	for _, tt := range []struct {
		name            string
		members         int
		afterOrg        kernel.ID
		process         string
		startGeneration int64
		wantReset       bool
		wantFirst       int
		wantCount       int
		wantGeneration  int64
		wantBoundary    int64
	}{
		{name: "100 members from generation 0", members: 100, afterOrg: org, wantCount: 100, wantGeneration: 100},
		{name: "101 members from generation 0 overflows", members: 101, afterOrg: org, wantReset: true, wantGeneration: 101},
		{name: "101 members from generation 1", members: 101, afterOrg: org, startGeneration: 1, wantFirst: 1, wantCount: 100, wantGeneration: 101},
		{name: "101 members from generation 101", members: 101, afterOrg: org, startGeneration: 101, wantGeneration: 101},
		{name: "foreign process", members: 1, afterOrg: org, process: "other", wantReset: true, wantGeneration: 1},
		{name: "unknown organisation with foreign process", afterOrg: unknownOrg, process: "other", wantReset: true},
		{name: "other organisation does not overflow", members: 101, afterOrg: unknownOrg},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := New(realtime.NewHub())
			var start Token
			if tt.process != "" {
				start = Token{Process: tt.process, Generation: tt.startGeneration}
			} else if tt.startGeneration == 0 {
				page, err := s.Read(org, nil)
				if err != nil {
					t.Fatalf("%s: Read = %v; want nil", tt.name, err)
				}
				start = page.Token
			} else {
				start = Token{Process: s.process, Generation: tt.startGeneration}
			}
			for i := range tt.members {
				s.Open(org, kernel.ID{2, byte(i)})
			}
			got := s.After(tt.afterOrg, start)
			if got.Reset != tt.wantReset {
				t.Errorf("%s: Reset = %t; want %t", tt.name, got.Reset, tt.wantReset)
			}
			if tt.wantCount == 0 {
				if got.Entries != nil {
					t.Errorf("%s: Entries = non-nil (len %d); want nil", tt.name, len(got.Entries))
				}
			} else if len(got.Entries) != tt.wantCount {
				t.Errorf("%s: len(Entries) = %d; want %d", tt.name, len(got.Entries), tt.wantCount)
			} else {
				for i := range tt.wantCount {
					want := Entry{Member: kernel.ID{2, byte(tt.wantFirst + i)}, Online: true}
					if got.Entries[i] != want {
						t.Errorf("%s: Entries[%d] = %+v; want %+v", tt.name, i, got.Entries[i], want)
						break
					}
				}
			}
			if got.Token.Generation != tt.wantGeneration {
				t.Errorf("%s: Token.Generation = %d; want %d", tt.name, got.Token.Generation, tt.wantGeneration)
			}
			if got.Boundary != tt.wantBoundary {
				t.Errorf("%s: Boundary = %d; want %d", tt.name, got.Boundary, tt.wantBoundary)
			}
			if got.Token.Process != s.process {
				t.Errorf("%s: Token.Process = %q; want %q", tt.name, got.Token.Process, s.process)
			}
		})
	}
}
