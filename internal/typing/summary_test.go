package typing

import (
	"slices"
	"testing"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

func TestOthersTyping(t *testing.T) {
	a, b, c, d := Typist{Member: kernel.ID{1}}, Typist{Member: kernel.ID{2}}, Typist{Member: kernel.ID{3}}, Typist{Member: kernel.ID{4}}
	viewer := Typist{Member: kernel.ID{9}}
	for _, tt := range []struct {
		name   string
		latest []Typist
		want   []Typist
	}{
		{"nobody", nil, nil},
		{"only the viewer", []Typist{viewer}, nil},
		{"one other", []Typist{a}, []Typist{a}},
		{"exactly the maximum", []Typist{a, b, c}, []Typist{a, b, c}},
		{"one over the maximum", []Typist{a, b, c, d}, []Typist{a, b, c}},
		{"viewer first", []Typist{viewer, a, b, c}, []Typist{a, b, c}},
		{"viewer between others", []Typist{a, viewer, b, c}, []Typist{a, b, c}},
		{"viewer last of four", []Typist{a, b, c, viewer}, []Typist{a, b, c}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := othersTyping(tt.latest, viewer.Member); !slices.Equal(got, tt.want) {
				t.Fatalf("othersTyping = %v, want %v", got, tt.want)
			}
		})
	}
}
