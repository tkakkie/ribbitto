package main

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
)

// verifyLogStore belongs to one server process. PR 1 has no callers outside
// tests; logs-off callers retain nothing by simply not calling capture.
type verifyLogStore struct {
	mu                   sync.Mutex
	captures             []*verifyLogCapture
	bytes, reads, served int
}

func (s *verifyLogStore) capture(first string, r *containerRecord) *verifyLogCapture {
	c := newVerifyLogCapture(first, r)
	if c == nil || c.size > verifyLogRecordMax {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		var id [verifyLogIDBytes]byte
		if _, err := rand.Read(id[:]); err != nil {
			return nil
		}
		c.id = hex.EncodeToString(id[:])
		if s.find(c.id) == nil {
			break
		}
	}
	for len(s.captures) >= verifyLogCaptures || s.bytes+c.size > verifyLogStoreBytes {
		s.bytes -= s.captures[0].size
		s.captures[0] = nil
		s.captures = s.captures[1:]
	}
	s.captures = append(s.captures, c)
	s.bytes += c.size
	return c
}

// find requires mu; captures are immutable except their read counters.
func (s *verifyLogStore) find(id string) *verifyLogCapture {
	for _, c := range s.captures {
		if c.id == id {
			return c
		}
	}
	return nil
}
