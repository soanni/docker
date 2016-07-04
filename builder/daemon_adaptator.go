package builder

import (
	"io"
	"syscall"
	"time"

	"golang.org/x/net/context"

	"github.com/docker/docker/api/types/backend"
	"github.com/docker/docker/image"
	"github.com/docker/docker/pkg/signal"
	"github.com/docker/docker/reference"
	"github.com/docker/engine-api/types"
	"github.com/docker/engine-api/types/container"
	"github.com/docker/engine-api/types/network"
)

// DaemonAdaptator adapts the builder.Client interface to the builder.Backend
// that represents the daemon.
type DaemonAdaptator struct {
	backend Backend
}

func NewDaemonAdaptator(backend Backend) *DaemonAdaptator {
	return &DaemonAdaptator{backend}
}

func (a *DaemonAdaptator) GetImageOnBuild(name string) (Image, error) {
	return a.backend.GetImageOnBuild(name)
}

func (a *DaemonAdaptator) TagImageWithReference(image image.ID, reference reference.Named) error {
	return a.backend.TagImageWithReference(image, reference)
}

func (a *DaemonAdaptator) PullOnBuild(ctx context.Context, name string, authConfigs map[string]types.AuthConfig, output io.Writer) (Image, error) {
	return a.backend.PullOnBuild(ctx, name, authConfigs, output)
}

func (a *DaemonAdaptator) ContainerAttachRaw(cID string, stdin io.ReadCloser, stdout, stderr io.Writer, stream bool) error {
	return a.backend.ContainerAttachRaw(cID, stdin, stdout, stderr, stream)
}

func (a *DaemonAdaptator) ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, containerName string) (types.ContainerCreateResponse, error) {
	return a.backend.ContainerCreate(types.ContainerCreateConfig{Config: config})
}

func (a *DaemonAdaptator) ContainerRemove(ctx context.Context, container string, options types.ContainerRemoveOptions) error {
	return a.backend.ContainerRm(container, &types.ContainerRmConfig{
		ForceRemove:  options.Force,
		RemoveVolume: options.RemoveVolumes,
		RemoveLink:   options.RemoveLinks,
	})
}

func (a *DaemonAdaptator) ContainerCommit(ctx context.Context, container string, options types.ContainerCommitOptions) (types.ContainerCommitResponse, error) {
	imageID, err := a.backend.Commit(container, &backend.ContainerCommitConfig{
		ContainerCommitConfig: types.ContainerCommitConfig{
			Author:  options.Author,
			Comment: options.Comment,
			Pause:   options.Pause,
			Config:  options.Config,
		},
		Changes: options.Changes,
	})
	return types.ContainerCommitResponse{
		ID: imageID,
	}, err
}

func (a *DaemonAdaptator) ContainerKill(ctx context.Context, container, sigStr string) error {
	var sig syscall.Signal
	// If we have a signal, look at it. Otherwise, do nothing
	if sigStr != "" {
		var err error
		if sig, err = signal.ParseSignal(sigStr); err != nil {
			return err
		}
	}
	return a.backend.ContainerKill(container, uint64(sig))
}

func (a *DaemonAdaptator) ContainerStart(ctx context.Context, container string, options types.ContainerStartOptions) error {
	return a.backend.ContainerStart(container, nil)
}

func (a *DaemonAdaptator) ContainerWait(containerID string, timeout time.Duration) (int, error) {
	return a.backend.ContainerWait(containerID, timeout)
}

func (a *DaemonAdaptator) ContainerUpdateCmdOnBuild(containerID string, cmd []string) error {
	return a.backend.ContainerUpdateCmdOnBuild(containerID, cmd)
}

func (a *DaemonAdaptator) CopyOnBuild(containerID string, destPath string, src FileInfo, decompress bool) error {
	return a.backend.CopyOnBuild(containerID, destPath, src, decompress)
}
