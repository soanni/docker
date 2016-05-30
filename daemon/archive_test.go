package daemon

import (
	"fmt"
	"io"
	"runtime"
	"testing"

	"github.com/docker/docker/container"
	"github.com/docker/docker/layer"
	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/pkg/registrar"
	"github.com/docker/docker/pkg/truncindex"
	"github.com/docker/docker/volume"
	volumestore "github.com/docker/docker/volume/store"
)

func TestContainerStatPathUnknownContainer(t *testing.T) {
	daemon := &Daemon{
		containers: container.NewMemoryStore(),
		idIndex:    truncindex.NewTruncIndex([]string{}),
		nameIndex:  registrar.NewRegistrar(),
	}
	stat, err := daemon.ContainerStatPath("any", "any/path")
	if err == nil || stat != nil {
		t.Fatalf("Expected an error and no stat, got %v, %v", err, stat)
	}
}

type testRWLayer struct {
	dir string
}

func (l *testRWLayer) TarStream() (io.ReadCloser, error) {
	return nil, nil
}

func (l *testRWLayer) Name() string {
	return ""
}

func (l *testRWLayer) Parent() layer.Layer {
	return nil
}

func (l *testRWLayer) Mount(mountLabel string) (string, error) {
	if l.dir == "" {
		return "", fmt.Errorf("RWLayer mount error")
	}
	return l.dir, nil
}

func (l *testRWLayer) Unmount() error {
	return nil
}

func (l *testRWLayer) Size() (int64, error) {
	return 0, nil
}

func (l *testRWLayer) Changes() ([]archive.Change, error) {
	return []archive.Change{}, nil
}

func (l *testRWLayer) Metadata() (map[string]string, error) {
	return map[string]string{}, nil
}

func TestContainerStatPathErrorsStat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("TODO: make this test windows compatible")
	}
	errorCases := []struct {
		baseFS      string
		rwLayerDir  string
		mountPoints map[string]*volume.MountPoint
		path        string
	}{
		{},
		{
			baseFS:     "/base/fs",
			rwLayerDir: "/dir",
		},
		{
			baseFS:     "/base/fs",
			rwLayerDir: "/base/fs",
			mountPoints: map[string]*volume.MountPoint{
				"myvolume": {
					ID: "volume_id",
				},
			},
		},
	}
	for i, e := range errorCases {
		c := &container.Container{
			CommonContainer: container.CommonContainer{
				ID:          "5a4ff6a163ad4533d22d69a2b8960bf7fafdcba06e72d2febdba229008b0bf57",
				Name:        "tender_bardeen",
				BaseFS:      e.baseFS,
				MountPoints: e.mountPoints,
				RWLayer: &testRWLayer{
					dir: e.rwLayerDir,
				},
			},
		}

		store := container.NewMemoryStore()
		store.Add(c.ID, c)

		index := truncindex.NewTruncIndex([]string{})
		index.Add(c.ID)

		vstore, err := volumestore.New("")
		if err != nil {
			t.Fatal(err)
		}

		daemon := &Daemon{
			containers: store,
			idIndex:    index,
			volumes:    vstore,
			nameIndex:  registrar.NewRegistrar(),
		}
		stat, err := daemon.ContainerStatPath("container_id", e.path)
		if err == nil || stat != nil {
			t.Fatalf("%d: Expected an error and no stat, got %v, %v", i, err, stat)
		}
	}
}
