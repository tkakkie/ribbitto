package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tkakkie/ribbitto/internal/conversation"
	"github.com/tkakkie/ribbitto/internal/conversation/conversationpg"
	"github.com/tkakkie/ribbitto/internal/identity/identitypg"
	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/org"
	"github.com/tkakkie/ribbitto/internal/org/orgpg"
	platform "github.com/tkakkie/ribbitto/internal/platform/postgres"
)

// Mirror load-client.md's v1 expected contract; command packages cannot import each other.
type expectedFile struct {
	Header   expectedHeader    `json:"header"`
	Messages []expectedMessage `json:"messages"`
}
type expectedHeader struct {
	Version          int    `json:"version"`
	Kind             string `json:"kind"`
	OrganizationSlug string `json:"organization_slug"`
	ChannelID        string `json:"channel_id"`
	InitialCursor    uint64 `json:"initial_cursor"`
	FinalWatermark   uint64 `json:"final_watermark"`
}
type expectedMessage struct {
	Sequence uint64 `json:"sequence"`
	Marker   string `json:"marker"`
}

func readExpectedCredentials(path string) (credentialFile, kernel.ID, error) {
	f, err := os.Open(path)
	if err != nil {
		return credentialFile{}, kernel.ID{}, errors.New("cannot open credential file")
	}
	defer func() { _ = f.Close() }()
	d := json.NewDecoder(io.LimitReader(f, 16<<20))
	d.DisallowUnknownFields()
	var data credentialFile
	var channel kernel.ID
	// Decoder errors can contain tokens; keep all input failures content-free.
	info, statErr := f.Stat()
	if statErr != nil || info.Size() > 16<<20 || d.Decode(&data) != nil || d.Decode(new(any)) != io.EOF || len(data.ChannelIDs) == 0 || len(data.Accounts) == 0 {
		return data, channel, errors.New("invalid credential file")
	}
	if _, err := org.ValidateSlug(data.OrganizationSlug); err != nil {
		return data, channel, errors.New("invalid credential organization slug")
	}
	for i, id := range data.ChannelIDs {
		if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
			return data, channel, errors.New("invalid credential channel ID")
		}
		raw, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
		if err != nil || len(raw) != len(channel) {
			return data, channel, errors.New("invalid credential channel ID")
		}
		if i == 0 {
			copy(channel[:], raw)
		}
	}
	for _, a := range data.Accounts {
		if len(a.Tokens) == 0 {
			return data, channel, errors.New("missing credential tokens")
		}
		for _, token := range a.Tokens {
			if token == "" {
				return data, channel, errors.New("empty credential token")
			}
		}
	}
	return data, channel, nil
}

func writeExpected(ctx context.Context, databaseURL, tokens, output string, after uint64) (err error) {
	data, channel, err := readExpectedCredentials(tokens)
	if err != nil {
		return err
	}
	f, err := createCredentials(output)
	if err != nil {
		return fmt.Errorf("creating expected file: %w", err)
	}
	defer func() {
		_ = f.Close()
		if err != nil {
			_ = os.Remove(f.Name())
		}
	}()
	pool, err := platform.OpenPool(ctx, databaseURL, nil)
	if err != nil {
		return fmt.Errorf("opening RIBBITTO_DATABASE_URL: %w", err)
	}
	defer pool.Close()
	account, _, err := identitypg.NewSessions(pool, time.Now, nil).Resolve(ctx, data.Accounts[0].Tokens[0])
	if err != nil {
		return fmt.Errorf("resolving expected session: %w", err)
	}
	m, err := orgpg.NewAuthorizer(pool).Member(ctx, &account, data.OrganizationSlug)
	if err != nil {
		return fmt.Errorf("resolving expected membership: %w", err)
	}
	reader := conversationpg.NewReader(pool,
		func(s platform.Snapshot) conversation.MemberDirectory { return orgpg.MembersIn(s) },
		func(s platform.Snapshot) conversation.AccountDirectory { return identitypg.AccountsIn(s) },
		func(s platform.Snapshot) conversation.EventCursor { return orgpg.EventCursorIn(s) })
	page, err := reader.Page(ctx, m, channel, nil, nil)
	if err != nil {
		return err
	}
	result := expectedFile{Header: expectedHeader{1, "expected", data.OrganizationSlug, data.ChannelIDs[0], after, uint64(*page.EventCursor)}, Messages: []expectedMessage{}}
	marker := regexp.MustCompile(`^loadgen[A-Z2-7]{26}(0|[1-9][0-9]*)Z`)
	for {
		for _, entry := range page.Entries {
			seq := uint64(entry.EventSeq)
			if seq > after && seq <= result.Header.FinalWatermark {
				result.Messages = append(result.Messages, expectedMessage{seq, marker.FindString(entry.Body)})
			}
		}
		if len(page.Entries) == 0 || !page.Older || uint64(page.Entries[0].EventSeq) <= after {
			break
		}
		before := page.Entries[0].EventSeq
		page, err = reader.Page(ctx, m, channel, nil, &before)
		if err != nil {
			return err
		}
	}
	sort.Slice(result.Messages, func(i, j int) bool { return result.Messages[i].Sequence < result.Messages[j].Sequence })
	if err := json.NewEncoder(f).Encode(result); err != nil {
		return fmt.Errorf("writing expected file: %w", err)
	}
	return f.Close()
}
