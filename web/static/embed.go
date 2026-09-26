// Package static provides the assets shipped in the server binary.
package static

import (
	"embed"
	"io/fs"
)

//go:embed css vendor
var files embed.FS

// FS returns the embedded assets rooted at css and vendor.
func FS() fs.FS { return files }
