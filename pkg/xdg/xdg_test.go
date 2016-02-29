package xdg

import (
	"os"
	"testing"
)

func testSearchXdgPath(t *testing.T, fun func(string) (string, error), path string, envs map[string]string, expectedConfigPath string, expectedErr bool) {
	backupenvs := setEnvs(envs)
	defer setEnvs(backupenvs)

	configPath, err := fun(path)
	if expectedErr && err == nil {
		t.Fatalf("expected an error but got %v and %s", err, configPath)
	}
	if configPath != expectedConfigPath {
		t.Fatalf("expected %s, got %s", expectedConfigPath, configPath)
	}
}

func setEnvs(envs map[string]string) map[string]string {
	backup := make(map[string]string, len(envs))
	for key, value := range envs {
		backup[key] = os.Getenv(key)
		os.Setenv(key, value)
	}
	return backup
}
