package xdg

import (
	"path/filepath"
)

func init() {
	DefaultConfigHome = filepath.Join(Home, "Library/Preferences")
	DefaultConfigsHome = []string{"/Library/Preferences", "/Library/Application Support"}
	DefaultDataHome = filepath.Join(Home, "Library")
	DefaultDatasHome = []string{"/Library"}
	DefaultCacheHome = filepath.Join(Home, "Library/Caches")
}
