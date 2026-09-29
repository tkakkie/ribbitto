// Package static provides the assets shipped in the server binary.
package static

import (
	"embed"
	"io/fs"
)

//go:embed css vendor *.js
var files embed.FS

// FS returns the embedded assets including CSS, vendored libraries and application scripts.
func FS() fs.FS { return files }
