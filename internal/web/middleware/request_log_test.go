package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLogResponseStatus(t *testing.T) {
	for _, kind := range []string{"implicit", "explicit", "informational", "flush"} {
		t.Run(kind, func(t *testing.T) {
			w := &logResponse{ResponseWriter: httptest.NewRecorder()}
			want := http.StatusCreated
			switch kind {
			case "implicit":
				want = http.StatusOK
				_, _ = w.Write([]byte("ok"))
			case "explicit":
				w.WriteHeader(want)
			case "informational":
				w.WriteHeader(http.StatusEarlyHints)
				if w.status != 0 {
					t.Fatal("informational response committed final status")
				}
				w.WriteHeader(want)
			case "flush":
				want = http.StatusOK
				if err := http.NewResponseController(w).Flush(); err != nil {
					t.Fatal(err)
				}
			}
			w.WriteHeader(http.StatusInternalServerError)
			if w.status != want {
				t.Fatalf("status = %d, want %d", w.status, want)
			}
		})
	}
}
