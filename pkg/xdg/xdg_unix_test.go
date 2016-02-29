// +build !windows !darwin

package xdg

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigPathDefaultValue(t *testing.T) {
	backupenv := setEnvs(map[string]string{
		"XDG_CONFIG_HOME": "",
	})
	defer setEnvs(backupenv)

	expectedConfigPath := fmt.Sprintf("%s/%s", os.Getenv("HOME"), ".config/something")
	configPath := ConfigPath("something")
	if configPath != expectedConfigPath {
		t.Fatalf("expected %s, got %s", expectedConfigPath, configPath)
	}
}

func TestConfigPathWithEnvironmentVariableSet(t *testing.T) {
	backupenv := setEnvs(map[string]string{
		"XDG_CONFIG_HOME": "/tmp/",
	})
	defer setEnvs(backupenv)

	expectedConfigPath := fmt.Sprintf("/tmp/%s", "something")
	configPath := ConfigPath("something")
	if configPath != expectedConfigPath {
		t.Fatalf("expected %s, got %s", expectedConfigPath, configPath)
	}
}

func TestSearchConfigPath(t *testing.T) {
	// setup some files
	tmpXdgSearchConfigPath, err := ioutil.TempDir("", "xdg-search-config-path")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpXdgSearchConfigPath)
	tmpXdgSearchConfigPathFolder1 := filepath.Join(tmpXdgSearchConfigPath, "folder1")
	if err := os.MkdirAll(tmpXdgSearchConfigPathFolder1, 0700); err != nil {
		t.Fatal(err)
	}
	tmpXdgSearchConfigPathFolder2 := filepath.Join(tmpXdgSearchConfigPath, "folder2")
	if err := os.MkdirAll(tmpXdgSearchConfigPathFolder2, 0700); err != nil {
		t.Fatal(err)
	}

	tmpXdgFileFolder1 := filepath.Join(tmpXdgSearchConfigPathFolder1, "xdgfile")
	if err := ioutil.WriteFile(tmpXdgFileFolder1, []byte("hello"), 0700); err != nil {
		t.Fatal(err)
	}
	tmpXdgFileFolder2 := filepath.Join(tmpXdgSearchConfigPathFolder2, "xdgfile")
	if err := ioutil.WriteFile(tmpXdgFileFolder2, []byte("hello"), 0700); err != nil {
		t.Fatal(err)
	}
	tmpXdgFileFolder22 := filepath.Join(tmpXdgSearchConfigPathFolder2, "xdgfile2")
	if err := ioutil.WriteFile(tmpXdgFileFolder22, []byte("hello"), 0700); err != nil {
		t.Fatal(err)
	}

	configCases := []struct {
		path               string
		envs               map[string]string
		expectedConfigPath string
		expectedErr        bool
	}{
		{
			envs: map[string]string{
				"XDG_CONFIG_HOME": "",
				"XDG_CONFIG_DIRS": "",
			},
			path:        "something",
			expectedErr: true,
		},
		{
			envs: map[string]string{
				"XDG_CONFIG_HOME": "",
				"XDG_CONFIG_DIRS": fmt.Sprintf("%s", tmpXdgSearchConfigPath),
			},
			path:        "something",
			expectedErr: true,
		},
		{
			envs: map[string]string{
				"XDG_CONFIG_HOME": tmpXdgSearchConfigPathFolder1,
				"XDG_CONFIG_DIRS": "",
			},
			path:               "xdgfile",
			expectedConfigPath: fmt.Sprintf(tmpXdgFileFolder1),
			expectedErr:        false,
		},
		{
			envs: map[string]string{
				"XDG_CONFIG_HOME": tmpXdgSearchConfigPathFolder2,
				"XDG_CONFIG_DIRS": "",
			},
			path:               "xdgfile",
			expectedConfigPath: fmt.Sprintf(tmpXdgFileFolder2),
			expectedErr:        false,
		},
		{
			envs: map[string]string{
				"XDG_CONFIG_HOME": "",
				"XDG_CONFIG_DIRS": fmt.Sprintf("%s:%s", tmpXdgSearchConfigPathFolder1, tmpXdgSearchConfigPathFolder2),
			},
			path:               "xdgfile",
			expectedConfigPath: fmt.Sprintf(tmpXdgFileFolder1),
			expectedErr:        false,
		},
		{
			envs: map[string]string{
				"XDG_CONFIG_HOME": "",
				"XDG_CONFIG_DIRS": fmt.Sprintf("%s:%s", tmpXdgSearchConfigPathFolder2, tmpXdgSearchConfigPathFolder1),
			},
			path:               "xdgfile",
			expectedConfigPath: fmt.Sprintf(tmpXdgFileFolder2),
			expectedErr:        false,
		},
		{
			envs: map[string]string{
				"XDG_CONFIG_HOME": "",
				"XDG_CONFIG_DIRS": fmt.Sprintf("%s:%s", tmpXdgSearchConfigPathFolder1, tmpXdgSearchConfigPathFolder2),
			},
			path:               "xdgfile2",
			expectedConfigPath: fmt.Sprintf(tmpXdgFileFolder22),
			expectedErr:        false,
		},
	}

	for _, c := range configCases {
		testSearchXdgPath(t, SearchConfigPath, c.path, c.envs, c.expectedConfigPath, c.expectedErr)
	}
}

func TestDataPathDefaultValue(t *testing.T) {
	backupenv := setEnvs(map[string]string{
		"XDG_DATA_HOME": "",
	})
	defer setEnvs(backupenv)

	expectedDataPath := fmt.Sprintf("%s/%s", os.Getenv("HOME"), ".local/share/something")
	dataPath := DataPath("something")
	if dataPath != expectedDataPath {
		t.Fatalf("expected %s, got %s", expectedDataPath, dataPath)
	}
}

func TestDataPathWithEnvironmentVariableSet(t *testing.T) {
	backupenv := setEnvs(map[string]string{
		"XDG_DATA_HOME": "/tmp/",
	})
	defer setEnvs(backupenv)

	expectedDataPath := fmt.Sprintf("/tmp/%s", "something")
	dataPath := DataPath("something")
	if dataPath != expectedDataPath {
		t.Fatalf("expected %s, got %s", expectedDataPath, dataPath)
	}
}

func TestSearchDataPath(t *testing.T) {
	// setup some files
	tmpXdgSearchDataPath, err := ioutil.TempDir("", "xdg-search-config-path")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpXdgSearchDataPath)
	tmpXdgSearchDataPathFolder1 := filepath.Join(tmpXdgSearchDataPath, "folder1")
	if err := os.MkdirAll(tmpXdgSearchDataPathFolder1, 0700); err != nil {
		t.Fatal(err)
	}
	tmpXdgSearchDataPathFolder2 := filepath.Join(tmpXdgSearchDataPath, "folder2")
	if err := os.MkdirAll(tmpXdgSearchDataPathFolder2, 0700); err != nil {
		t.Fatal(err)
	}

	tmpXdgFileFolder1 := filepath.Join(tmpXdgSearchDataPathFolder1, "xdgfile")
	if err := ioutil.WriteFile(tmpXdgFileFolder1, []byte("hello"), 0700); err != nil {
		t.Fatal(err)
	}
	tmpXdgFileFolder2 := filepath.Join(tmpXdgSearchDataPathFolder2, "xdgfile")
	if err := ioutil.WriteFile(tmpXdgFileFolder2, []byte("hello"), 0700); err != nil {
		t.Fatal(err)
	}
	tmpXdgFileFolder22 := filepath.Join(tmpXdgSearchDataPathFolder2, "xdgfile2")
	if err := ioutil.WriteFile(tmpXdgFileFolder22, []byte("hello"), 0700); err != nil {
		t.Fatal(err)
	}

	configCases := []struct {
		path             string
		envs             map[string]string
		expectedDataPath string
		expectedErr      bool
	}{
		{
			envs: map[string]string{
				"XDG_DATA_HOME": "",
				"XDG_DATA_DIRS": "",
			},
			path:        "something",
			expectedErr: true,
		},
		{
			envs: map[string]string{
				"XDG_DATA_HOME": "",
				"XDG_DATA_DIRS": fmt.Sprintf("%s", tmpXdgSearchDataPath),
			},
			path:        "something",
			expectedErr: true,
		},
		{
			envs: map[string]string{
				"XDG_DATA_HOME": tmpXdgSearchDataPathFolder1,
				"XDG_DATA_DIRS": "",
			},
			path:             "xdgfile",
			expectedDataPath: fmt.Sprintf(tmpXdgFileFolder1),
			expectedErr:      false,
		},
		{
			envs: map[string]string{
				"XDG_DATA_HOME": tmpXdgSearchDataPathFolder2,
				"XDG_DATA_DIRS": "",
			},
			path:             "xdgfile",
			expectedDataPath: fmt.Sprintf(tmpXdgFileFolder2),
			expectedErr:      false,
		},
		{
			envs: map[string]string{
				"XDG_DATA_HOME": "",
				"XDG_DATA_DIRS": fmt.Sprintf("%s:%s", tmpXdgSearchDataPathFolder1, tmpXdgSearchDataPathFolder2),
			},
			path:             "xdgfile",
			expectedDataPath: fmt.Sprintf(tmpXdgFileFolder1),
			expectedErr:      false,
		},
		{
			envs: map[string]string{
				"XDG_DATA_HOME": "",
				"XDG_DATA_DIRS": fmt.Sprintf("%s:%s", tmpXdgSearchDataPathFolder2, tmpXdgSearchDataPathFolder1),
			},
			path:             "xdgfile",
			expectedDataPath: fmt.Sprintf(tmpXdgFileFolder2),
			expectedErr:      false,
		},
		{
			envs: map[string]string{
				"XDG_DATA_HOME": "",
				"XDG_DATA_DIRS": fmt.Sprintf("%s:%s", tmpXdgSearchDataPathFolder1, tmpXdgSearchDataPathFolder2),
			},
			path:             "xdgfile2",
			expectedDataPath: fmt.Sprintf(tmpXdgFileFolder22),
			expectedErr:      false,
		},
	}

	for _, c := range configCases {
		testSearchXdgPath(t, SearchDataPath, c.path, c.envs, c.expectedDataPath, c.expectedErr)
	}
}

func TestCachePathDefaultValue(t *testing.T) {
	backupenv := setEnvs(map[string]string{
		"XDG_CACHE_DIR": "",
	})
	defer setEnvs(backupenv)

	expectedConfigPath := fmt.Sprintf("%s/%s", os.Getenv("HOME"), ".cache/something")
	configPath := CachePath("something")
	if configPath != expectedConfigPath {
		t.Fatalf("expected %s, got %s", expectedConfigPath, configPath)
	}
}

func TestCachePathWithEnvironmentVariableSet(t *testing.T) {
	backupenv := setEnvs(map[string]string{
		"XDG_CACHE_DIR": "/tmp/",
	})
	defer setEnvs(backupenv)

	expectedConfigPath := fmt.Sprintf("/tmp/%s", "something")
	configPath := CachePath("something")
	if configPath != expectedConfigPath {
		t.Fatalf("expected %s, got %s", expectedConfigPath, configPath)
	}
}

func TestSearchCachePath(t *testing.T) {
	// setup some files
	tmpXdgSearchConfigPath, err := ioutil.TempDir("", "xdg-search-config-path")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpXdgSearchConfigPath)
	tmpXdgSearchConfigPathFolder1 := filepath.Join(tmpXdgSearchConfigPath, "folder1")
	if err := os.MkdirAll(tmpXdgSearchConfigPathFolder1, 0700); err != nil {
		t.Fatal(err)
	}
	tmpXdgSearchConfigPathFolder2 := filepath.Join(tmpXdgSearchConfigPath, "folder2")
	if err := os.MkdirAll(tmpXdgSearchConfigPathFolder2, 0700); err != nil {
		t.Fatal(err)
	}

	tmpXdgFileFolder1 := filepath.Join(tmpXdgSearchConfigPathFolder1, "xdgfile")
	if err := ioutil.WriteFile(tmpXdgFileFolder1, []byte("hello"), 0700); err != nil {
		t.Fatal(err)
	}

	configCases := []struct {
		path               string
		envs               map[string]string
		expectedConfigPath string
		expectedErr        bool
	}{
		{
			envs: map[string]string{
				"XDG_CACHE_DIR": "",
			},
			path:        "something",
			expectedErr: true,
		},
		{
			envs: map[string]string{
				"XDG_CACHE_DIR": "",
			},
			path:        "something",
			expectedErr: true,
		},
		{
			envs: map[string]string{
				"XDG_CACHE_DIR": tmpXdgSearchConfigPathFolder1,
			},
			path:               "xdgfile",
			expectedConfigPath: fmt.Sprintf(tmpXdgFileFolder1),
			expectedErr:        false,
		},
	}

	for _, c := range configCases {
		testSearchXdgPath(t, SearchCachePath, c.path, c.envs, c.expectedConfigPath, c.expectedErr)
	}
}
