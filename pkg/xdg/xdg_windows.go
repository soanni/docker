package xdg

import (
	"os"
)

func init() {
	DefaultConfigHome = os.Getenv("APPDATA")
	DefaultConfigsHome = []string{os.Getenv("APPDATA"), os.Getenv("LOCALAPPDATA")}
	DefaultDataHome = os.Getenv("APPDATE")
	DefaultDatasHome = []string{os.Getenv("APPDATA"), os.Getenv("LOCALAPPDATA")}
	DefaultCacheHome = os.Getenv("TEMP")
}
