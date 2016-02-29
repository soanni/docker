// Package xdg implements convenience functions for reading configuration and
// data file according to the XDG Base Directory Specification.
// See http://standards.freedesktop.org/basedir-spec/basedir-spec-latest.html
package xdg

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

var (
	Home = os.Getenv("HOME")

	DefaultConfigHome  string
	DefaultConfigsHome []string
	DefaultDataHome    string
	DefaultDatasHome   []string
	DefaultCacheHome   string
)

func ConfigPath(name string) string {
	xdgConfigDir := xdgPath("XDG_CONFIG_HOME", DefaultConfigHome)
	return filepath.Join(xdgConfigDir, name)
}

func SearchConfigPath(name string) (string, error) {
	xdgConfigDir := xdgPath("XDG_CONFIG_HOME", DefaultConfigHome)
	xdgConfigDirs := xdgPaths("XDG_CONFIG_DIRS", xdgConfigDir, DefaultConfigsHome)

	// Look at xdgConfigDirs in order and get the first one accessible
	for _, folder := range xdgConfigDirs {
		fpath := filepath.Join(folder, name)
		if exists(fpath) {
			return fpath, nil
		}
	}
	return "", fmt.Errorf("did not found configuration file %s in %v", name, xdgConfigDirs)
}

func DataPath(name string) string {
	xdgDataDir := xdgPath("XDG_DATA_HOME", DefaultDataHome)
	return filepath.Join(xdgDataDir, name)
}

func SearchDataPath(name string) (string, error) {
	xdgDataDir := xdgPath("XDG_DATA_HOME", DefaultConfigHome)
	xdgDataDirs := xdgPaths("XDG_DATA_DIRS", xdgDataDir, DefaultConfigsHome)

	// Look at xdgDataDirs in order and get the first one accessible
	for _, folder := range xdgDataDirs {
		fpath := filepath.Join(folder, name)
		if exists(fpath) {
			return fpath, nil
		}
	}
	return "", fmt.Errorf("did not found data file %s in %v", name, xdgDataDirs)
}

func CachePath(name string) string {
	xdgCacheDir := xdgPath("XDG_CACHE_DIR", DefaultCacheHome)
	return filepath.Join(xdgCacheDir, name)
}

func SearchCachePath(name string) (string, error) {
	xdgCacheDir := xdgPath("XDG_CACHE_DIR", DefaultCacheHome)
	fpath := filepath.Join(xdgCacheDir, name)
	if exists(fpath) {
		return fpath, nil
	}
	return "", fmt.Errorf("did not found data file %s in %s", name, xdgCacheDir)
}

func xdgPath(name, defaultPath string) string {
	dir := os.Getenv(name)
	if dir != "" && path.IsAbs(dir) {
		return dir
	}

	return defaultPath
}

func xdgPaths(name, homePath string, defaultPaths []string) []string {
	dirs := []string{}

	paths := strings.Split(os.Getenv(name), ":")
	for _, p := range paths {
		if p != "" && path.IsAbs(p) {
			dirs = append(dirs, p)
		}
	}

	if len(dirs) == 0 {
		dirs = append(dirs, defaultPaths...)
	}

	if homePath != "" {
		dirs = append([]string{homePath}, dirs...)
	}

	return dirs
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil || os.IsExist(err)
}
