package middleware

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestLogPanic(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
	}{{"uncommitted", 0}, {"committed", http.StatusCreated}} {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			h := RequestLog(slog.New(slog.NewJSONHandler(&logs, nil)), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tt.status != 0 {
					w.WriteHeader(tt.status)
				}
				panic("handler panic")
			}))
			func() {
				defer func() {
					if value := recover(); value != "handler panic" {
						t.Errorf("panic = %v, want handler panic", value)
					}
				}()
				h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
			}()
			if strings.Count(logs.String(), "\n") != 1 {
				t.Fatalf("want one log line, got %s", logs.String())
			}
			var record map[string]any
			if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
				t.Fatal(err)
			}
			want := tt.status
			if want == 0 {
				want = http.StatusInternalServerError
			}
			if record["status"] != float64(want) {
				t.Fatalf("status = %v, want %d", record["status"], want)
			}
		})
	}
}

type headerRecorder struct {
	*httptest.ResponseRecorder
	statuses []int
}

func (w *headerRecorder) WriteHeader(status int) {
	w.statuses = append(w.statuses, status)
	w.ResponseRecorder.WriteHeader(status)
}

func TestLogResponseStatus(t *testing.T) {
	for _, kind := range []string{"implicit", "explicit", "informational", "flush"} {
		t.Run(kind, func(t *testing.T) {
			underlying := &headerRecorder{ResponseRecorder: httptest.NewRecorder()}
			w := &logResponse{ResponseWriter: underlying}
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
			calls := len(underlying.statuses)
			w.WriteHeader(http.StatusInternalServerError)
			if len(underlying.statuses) != calls+1 || underlying.statuses[calls] != http.StatusInternalServerError {
				t.Fatalf("later WriteHeader did not reach underlying writer: %v", underlying.statuses)
			}
			if w.status != want {
				t.Fatalf("status = %d, want %d", w.status, want)
			}
		})
	}
}
