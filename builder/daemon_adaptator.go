package builder

import (
	"io"
	"time"

	"github.com/docker/docker/api/types/backend"
	"github.com/docker/docker/image"
	"github.com/docker/docker/reference"
	"github.com/docker/engine-api/types"
	"github.com/docker/engine-api/types/container"
	"github.com/docker/engine-api/types/network"
	"golang.org/x/net/context"
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

func (a *DaemonAdaptator) Commit(container string, config *backend.ContainerCommitConfig) (string, error) {
	return a.backend.Commit(container, config)
}

func (a *DaemonAdaptator) ContainerKill(containerID string, sig uint64) error {
	return a.backend.ContainerKill(containerID, sig)
}

func (a *DaemonAdaptator) ContainerStart(containerID string, hostConfig *container.HostConfig) error {
	return a.backend.ContainerStart(containerID, hostConfig)
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
