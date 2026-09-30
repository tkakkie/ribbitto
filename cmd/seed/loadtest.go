package main

import (
	"fmt"
	"os"
	"path/filepath"
)

type credentialFile struct {
	OrganizationSlug string          `json:"organization_slug"`
	ChannelIDs       []string        `json:"channel_ids"`
	Accounts         []accountTokens `json:"accounts"`
}

type accountTokens struct {
	Handle string   `json:"handle"`
	Tokens []string `json:"tokens"`
}

func validateRun(messages, channels, members, streams, perAccount, sessions int, loadTest bool, output string) (int, error) {
	const maxWork = 100000 // One budget for all sessions and messages, including fixture accounts.
	accounts := members
	if messages < 1 {
		return 0, fmt.Errorf("messages N must be positive")
	}
	if loadTest {
		if streams < 1 || perAccount < 1 || sessions < 1 || output == "" {
			return 0, fmt.Errorf("load tests require positive streams, streams-per-account, sessions-per-account and an output path")
		}
		accounts = max(members, 1+(streams-1)/perAccount)
	} else {
		sessions = 0
	}
	// Divide before multiplying so even maximum-int flags cannot overflow.
	if messages > maxWork/channels || (sessions > 0 && accounts > (maxWork-messages*channels)/sessions) {
		return 0, fmt.Errorf("seed exceeds the %d total sessions and messages limit", maxWork)
	}
	return accounts, nil
}

func createCredentials(path string) (*os.File, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return nil, err
	}
	// Check resolved ancestors so relative paths and symlinked directories
	// cannot put credentials into a checkout (including a Git worktree).
	for dir := parent; ; dir = filepath.Dir(dir) {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
			return nil, fmt.Errorf("credential output must be outside repositories")
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	return os.OpenFile(filepath.Join(parent, filepath.Base(absolute)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
}
