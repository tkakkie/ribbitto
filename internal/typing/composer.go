package typing

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"time"

	"github.com/tkakkie/ribbitto/internal/kernel"
)

// ClosedRetention bounds refusal of signals from a successfully posted composer.
const ClosedRetention = time.Minute

// SignalDeadline bounds time from the caller's handling start to locked acceptance.
const SignalDeadline = 10 * time.Second

// IssueComposer returns a unique signed generation for server-resolved scope.
// It stores nothing. Failed-submission re-renders must reuse the submitted value.
func (s *State) IssueComposer(organizationID kernel.ID, place Place, memberID kernel.ID) string {
	nonce := rand.Text()
	return nonce + "." + base64.RawURLEncoding.EncodeToString(s.signature(organizationID, place, memberID, nonce))
}

func (s *State) signature(organizationID kernel.ID, place Place, memberID kernel.ID, nonce string) []byte {
	mac := hmac.New(sha256.New, s.signingKey)
	// Fixed-size IDs avoid ambiguous concatenation; the random nonce separates tabs.
	mac.Write(organizationID[:])
	mac.Write(memberID[:])
	mac.Write(place.Channel[:])
	mac.Write(place.Topic[:])
	mac.Write([]byte(nonce))
	return mac.Sum(nil)
}

func (s *State) issued(organizationID kernel.ID, place Place, memberID kernel.ID, generation string) bool {
	nonce, signature, ok := strings.Cut(generation, ".")
	// Compare the canonical encoding too: aliases must not bypass closed's key.
	want := base64.RawURLEncoding.EncodeToString(s.signature(organizationID, place, memberID, nonce))
	return ok && hmac.Equal([]byte(signature), []byte(want))
}

// Signal accepts only an issued, open composer within SignalDeadline of handledAt.
// The caller supplies its handling start and trusted scope/identity after authorization.
// Refusal changes no activity, expiry deadline or visible generation.
func (s *State) Signal(organizationID kernel.ID, place Place, typist Typist, generation string, handledAt time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if now.Before(handledAt) || now.Sub(handledAt) > SignalDeadline || s.closed[generation] != nil || !s.issued(organizationID, place, typist.Member, generation) {
		return false
	}
	if place.Topic == (kernel.ID{}) || s.streams[streamKey{organizationID, typist.Member, place.Channel}] == nil {
		return false
	}
	s.start(organizationID, place, typist)
	return true
}

// Posted stops the member in the actual successfully posted scope and closes its
// issued composer under the signal lock. Missing/invalid generations still stop
// activity, allowing posting before composers carry generations.
func (s *State) Posted(organizationID kernel.ID, place Place, memberID kernel.ID, generation string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed[generation] == nil && s.issued(organizationID, place, memberID, generation) {
		s.closed[generation] = time.AfterFunc(ClosedRetention, func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			delete(s.closed, generation)
		})
	}
	s.stop(organizationID, place, memberID)
}
