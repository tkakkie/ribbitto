package web

import (
	"crypto/sha256"

	"github.com/tkakkie/ribbitto/internal/kernel"
	"github.com/tkakkie/ribbitto/internal/web/view"
)

func channelImageURL(id kernel.ID, key string) string {
	keys := [...]string{"lake", "hills", "leaf", "cup", "flowers", "sunrise", "moon", "sail", "cactus", "mountain", "shell", "kite"}
	for _, known := range keys {
		if key == known {
			return view.ChannelImageURL(key)
		}
	}
	// Hash all UUID bytes, including the random suffix, rather than grouping
	// channels created close together by their UUIDv7 timestamp.
	digest := sha256.Sum256(id[:])
	return view.ChannelImageURL(keys[int(digest[0])%len(keys)])
}
