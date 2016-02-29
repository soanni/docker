package xdg

import (
	"path/filepath"
)

func init() {
	DefaultConfigHome = filepath.Join(Home, ".config")
	DefaultConfigsHome = []string{"/etc/xdg"}
	DefaultDataHome = filepath.Join(Home, ".local/share")
	DefaultDatasHome = []string{"/usr/local/share", "/usr/share"}
	DefaultCacheHome = filepath.Join(Home, ".cache")
}
