// Package web carries the portal's templates and static assets.
//
// They are embedded in the binary so the runtime container is one executable
// plus CA certificates, with no filesystem layout to get wrong at deploy time.
package web

import (
	"embed"
	"io/fs"
	"os"
)

//go:embed templates static
var embedded embed.FS

// Assets returns the compiled-in asset tree.
func Assets() fs.FS { return embedded }

// DirAssets returns an asset tree read from disk.
//
// This backs the development reload mode, where templates and CSS are re-read
// per request so the browser can be refreshed without rebuilding.
func DirAssets(dir string) (fs.FS, error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, err
	}
	return os.DirFS(dir), nil
}
