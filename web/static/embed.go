// Package static provides embedded assets and the manifest-backed page script registry.
package static

import (
	"embed"
	"io/fs"
)

//go:embed css vendor brand *.js channel-images-v1 dist/*.js dist/manifest.json
var files embed.FS

// FS returns the embedded assets including CSS, vendored libraries, brand
// images, channel illustrations and application scripts.
func FS() fs.FS { return files }
