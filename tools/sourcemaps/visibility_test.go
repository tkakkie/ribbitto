package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func runMockStore(t *testing.T, s *mockStore) (string, error) {
	t.Helper()
	srv := s.server(t)
	defer srv.Close()
	dir, _, _ := writeMapDir(t, shaOf(1), "10")
	var out bytes.Buffer
	err := runStore(t.Context(), testCfg(srv.URL, s.token, time.Now(), &out), dir, shaOf(1), "10", "1")
	return out.String() + fmt.Sprint(err), err
}

func TestUploadResponses(t *testing.T) {
	for _, status := range []int{200, 201, 422} {
		for _, bad := range []string{"empty", "null", "malformed", "zero ID", "wrong name", "wrong size", "bad state", "already exists"} {
			t.Run(fmt.Sprintf("%d/%s", status, bad), func(t *testing.T) {
				s := &mockStore{token: "mock-credential"}
				s.before = func(w http.ResponseWriter, r *http.Request) bool {
					if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/assets") {
						return false
					}
					a := ghAsset{ID: 12, Name: r.URL.Query().Get("name"), Size: r.ContentLength, State: "uploaded"}
					w.WriteHeader(status)
					if body, ok := map[string]string{"empty": "", "null": "null", "malformed": `{"id":"PRIVATE-RESPONSE"}`, "already exists": `{"errors":[{"code":"already_exists"}]}`}[bad]; ok {
						_, _ = w.Write([]byte(body))
						return true
					}
					switch bad {
					case "zero ID":
						a.ID = 0
					case "wrong name":
						a.Name = "PRIVATE-RESPONSE"
					case "wrong size":
						a.Size++
					case "bad state":
						a.State = "PRIVATE-RESPONSE"
					}
					_ = json.NewEncoder(w).Encode(a)
					return true
				}
				logs, err := runMockStore(t, s)
				if err == nil || !strings.Contains(err.Error(), `uploading asset "index.json"`) || len(s.events) != 1 {
					t.Fatalf("invalid upload accepted or continued: %v events=%v", err, s.events)
				}
				for _, private := range []string{s.token, "PRIVATE-RESPONSE", "sourcesContent"} {
					if strings.Contains(err.Error()+logs, private) {
						t.Fatal("private response or credential reached diagnostics")
					}
				}
			})
		}
	}
}

func TestDraftAssetVisibility(t *testing.T) {
	for _, source := range []string{"release", "assets list"} {
		for _, mode := range []string{"transient", "persistent 200", "persistent 201", "duplicate", "extra", "wrong size", "bad state", "wrong ID"} {
			t.Run(source+"/"+mode, func(t *testing.T) {
				t.Parallel()
				s := &mockStore{token: "mock-credential", uploadStatus: map[bool]int{true: 200, false: 201}[mode == "persistent 200"], uploadState: "uploaded"}
				reads := 0
				s.before = func(w http.ResponseWriter, r *http.Request) bool {
					path := "/repos/" + prodStoreRepo + "/releases/101"
					if source == "assets list" {
						path += "/assets"
					}
					if r.Method != "GET" || r.URL.Path != path {
						return false
					}
					reads++
					rel := s.releases[0]
					rel.Assets = slices.Clone(rel.Assets)
					switch mode {
					case "transient", "persistent 200", "persistent 201":
						if mode != "transient" || reads < 3 {
							rel.Assets = rel.Assets[1:]
						}
					case "duplicate":
						rel.Assets = append(rel.Assets, rel.Assets[0])
					case "extra":
						rel.Assets = append(rel.Assets, mockAsset{ID: 999, Name: "extra\n::error::##[forged]"})
					case "wrong size":
						rel.Assets[0].Size++
					case "bad state":
						rel.Assets[0].State = "starter"
					case "wrong ID":
						rel.Assets[0].ID++
					}
					if source == "release" {
						_ = json.NewEncoder(w).Encode(rel)
					} else {
						_ = json.NewEncoder(w).Encode(rel.Assets)
					}
					return true
				}
				logs, err := runMockStore(t, s)
				success := mode == "transient"
				published := slices.ContainsFunc(s.events, func(e string) bool { return strings.HasPrefix(e, "publish:") })
				limit := map[bool]int{true: 1, false: 2*assetReadAttempts + 1}[!success && !strings.HasPrefix(mode, "persistent")]
				if success != (err == nil) || success != published || reads > limit || (!success && reads != limit) {
					t.Fatalf("unexpected verification: %v reads=%d events=%v", err, reads, s.events)
				}
				if strings.Count(strings.Join(s.events, "\n"), "upload:") != 2 {
					t.Fatal("an acknowledged upload was repeated")
				}
				for _, want := range []string{"id=", "size=", "state=", "expected=2 actual=", "missing=", "extra=", "duplicate=", "invalid="} {
					if !strings.Contains(logs, want) {
						t.Errorf("missing diagnostic %s", want)
					}
				}
				if strings.HasPrefix(mode, "persistent") && !strings.Contains(logs, `missing=["index.json"]`) {
					t.Fatal("missing filename absent from diagnostics")
				}
				for _, forbidden := range []string{s.token, "sourcesContent", "::", "##[", "\r"} {
					if strings.Contains(logs, forbidden) {
						t.Fatal("unsafe diagnostic")
					}
				}
			})
		}
	}
}

func TestMissingAssetReupload(t *testing.T) {
	for _, mode := range []string{"200", "201", "500", "422 already_exists", "invalid response", "still absent", "duplicate after", "missing extra", "missing duplicate", "missing invalid", "fresh invalid", "multiple"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			s := &mockStore{token: "mock-credential", uploadStatus: map[bool]int{true: 200, false: 201}[mode == "200"]}
			posts, reads := map[string]int{}, 0
			s.before = func(w http.ResponseWriter, r *http.Request) bool {
				if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/assets") {
					name := r.URL.Query().Get("name")
					posts[name]++
					if posts[name] == 2 && reads != assetReadAttempts+1 {
						t.Error("re-upload without a fresh pair of listings")
					}
					if (posts[name] == 1 || mode == "still absent") && (name == "index.json" || mode == "multiple") {
						w.WriteHeader(s.uploadStatus)
						_ = json.NewEncoder(w).Encode(ghAsset{ID: 999, Name: name, Size: r.ContentLength})
						return true
					}
					if posts[name] == 2 && (mode == "500" || mode == "422 already_exists" || mode == "invalid response") {
						w.WriteHeader(map[string]int{"500": 500, "422 already_exists": 422, "invalid response": 200}[mode])
						_, _ = w.Write([]byte(`{"id":"PRIVATE-RESPONSE","code":"already_exists"}`))
						return true
					}
					return false
				}
				if r.Method != "GET" || !strings.HasPrefix(r.URL.Path, "/repos/"+prodStoreRepo+"/releases/101") {
					return false
				}
				if !strings.HasSuffix(r.URL.Path, "/assets") {
					reads++
				}
				rel := s.releases[0]
				rel.Assets = slices.Clone(rel.Assets)
				switch mode {
				case "missing extra":
					rel.Assets = append(rel.Assets, mockAsset{ID: 999, Name: "extra"})
				case "missing duplicate", "duplicate after":
					if mode == "missing duplicate" || posts["index.json"] == 2 {
						rel.Assets = append(rel.Assets, rel.Assets[0])
					}
				case "missing invalid":
					rel.Assets[0].State = "starter"
				case "fresh invalid":
					if reads == assetReadAttempts+1 {
						rel.Assets = append(rel.Assets, mockAsset{ID: 999, Name: "index.json", State: "starter"})
					}
				}
				if strings.HasSuffix(r.URL.Path, "/assets") {
					_ = json.NewEncoder(w).Encode(rel.Assets)
				} else {
					_ = json.NewEncoder(w).Encode(rel)
				}
				return true
			}
			logs, err := runMockStore(t, s)
			success := mode == "200" || mode == "201" || mode == "multiple"
			wantPosts := map[bool]int{true: 1, false: 2}[strings.HasPrefix(mode, "missing ") || mode == "fresh invalid"]
			if (err == nil) != success || slices.ContainsFunc(s.events, func(e string) bool { return strings.HasPrefix(e, "publish:") }) != success || posts["index.json"] != wantPosts || posts["message-time-ABCDEF12.js.map"] != map[bool]int{true: 2, false: 1}[mode == "multiple"] || reads > 2*assetReadAttempts+1 || (mode == "still absent" && reads != 2*assetReadAttempts+1) {
				t.Fatalf("err=%v posts=%v reads=%d events=%v", err, posts, reads, s.events)
			}
			if strings.Contains(logs, s.token) || strings.Contains(logs, "PRIVATE-RESPONSE") || (!success && !strings.Contains(logs, `"index.json"`)) || (success && wantPosts == 2 && !strings.Contains(logs, "re-upload=true")) {
				t.Fatalf("unsafe or missing diagnostics: %s", logs)
			}
		})
	}
}

func TestDraftAssetsPagination(t *testing.T) {
	now := time.Now()
	name := relNameAt(now, shaOf(1), 10, 1)
	s := &mockStore{token: "mock", releases: []mockRelease{{ID: 1, Name: name, TagName: name, Draft: true}}}
	want, uploaded := map[string][]byte{}, map[string]int64{}
	for i := range 101 {
		a := mockAsset{ID: int64(i + 1), Name: fmt.Sprintf("%d.js.map", i), Size: 1}
		s.releases[0].Assets = append(s.releases[0].Assets, a)
		want[a.Name], uploaded[a.Name] = []byte("x"), a.ID
	}
	srv := s.server(t)
	defer srv.Close()
	c := &apiClient{cfg: testCfg(srv.URL, s.token, now, nil)}
	if got, err := c.readDraftAssets(t.Context(), ghRelease{ID: 1}, name, want, uploaded); err != nil || len(got.Assets) != 101 {
		t.Fatalf("paginated verification failed: %v", err)
	}
	pages := 0
	s.before = func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, "/assets") {
			return false
		}
		pages++
		_ = json.NewEncoder(w).Encode(s.releases[0].Assets[:100])
		return true
	}
	if _, err := c.readDraftAssets(t.Context(), ghRelease{ID: 1}, name, want, uploaded); err == nil || pages != maxReleasePages {
		t.Fatalf("verification did not respect page cap: %v pages=%d", err, pages)
	}
}

type deadlineTransport struct{}

func (deadlineTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	<-r.Context().Done()
	return nil, r.Context().Err()
}

func TestDraftAssetsDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := testCfg("http://mock.invalid", "mock", time.Now(), nil)
		cfg.transport = deadlineTransport{}
		c, start := &apiClient{cfg: cfg}, time.Now()
		if _, err := c.readDraftAssets(t.Context(), ghRelease{ID: 1}, "", nil, nil); err == nil || time.Since(start) != assetReadTimeout {
			t.Fatalf("verification did not respect total timeout: %v elapsed=%s", err, time.Since(start))
		}
	})
}
