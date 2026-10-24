package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// Peers are host-wrapper policy; the launcher only enforces their separation.
type implementPeerHomes []string

func (p *implementPeerHomes) String() string { return fmt.Sprint([]string(*p)) }
func (p *implementPeerHomes) Set(value string) error {
	if len(*p) == 3 {
		return errors.New("at most three --peer-home values are allowed")
	}
	*p = append(*p, value)
	return nil
}

func (p implementPeerHomes) validate(home string, lstat func(string) (fs.FileInfo, error)) error {
	for _, peer := range p {
		if peer == "/" {
			return errors.New("--peer-home cannot be root")
		}
		if err := implementRealDirectory(peer, lstat); err != nil {
			return fmt.Errorf("checking --peer-home %q: %w", peer, err)
		}
		info, err := lstat(peer)
		if err != nil {
			return fmt.Errorf("checking --peer-home identity: %w", err)
		}
		other, err := lstat(home)
		if peer == home || err == nil && os.SameFile(info, other) {
			return errors.New("--peer-home must differ from --home")
		}
	}
	return nil
}

func (p implementPeerHomes) separate(home, tree string, paths []string, lstat func(string) (fs.FileInfo, error)) error {
	for i, first := range p {
		for _, second := range p[i+1:] {
			inside, err := implementContains(first, second, lstat)
			if err != nil {
				return fmt.Errorf("checking peer containment %q / %q: %w", first, second, err)
			}
			if inside {
				return fmt.Errorf("peer home %q overlaps peer home %q", first, second)
			}
			inside, err = implementContains(second, first, lstat)
			if err != nil {
				return fmt.Errorf("checking peer containment %q / %q: %w", second, first, err)
			}
			if inside {
				return fmt.Errorf("peer home %q overlaps peer home %q", first, second)
			}
		}
	}
	for _, peer := range p {
		for _, path := range paths {
			inside, err := implementContains(peer, path, lstat)
			if err != nil {
				return fmt.Errorf("checking peer containment %q / %q: %w", peer, path, err)
			}
			if inside {
				return fmt.Errorf("path %q is inside peer home %q", path, peer)
			}
		}
		for _, root := range []string{tree, home} {
			inside, err := implementContains(root, peer, lstat)
			if err != nil {
				return fmt.Errorf("checking peer containment %q / %q: %w", root, peer, err)
			}
			if inside {
				return fmt.Errorf("peer home %q is inside %q", peer, root)
			}
		}
	}
	return nil
}
