package daemon

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/container"
	"github.com/docker/docker/daemon/network"
	derr "github.com/docker/docker/errors"
	"github.com/docker/docker/image"
	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/pkg/signal"
	"github.com/docker/docker/pkg/truncindex"
	containertypes "github.com/docker/engine-api/types/container"
	"github.com/docker/engine-api/types/strslice"
	"github.com/docker/go-connections/nat"
)

var (
	// ErrRootFSReadOnly is returned when a container
	// rootfs is marked readonly.
	ErrRootFSReadOnly = errors.New("container rootfs is marked read-only")
)

// GetContainer looks for a container using the provided information, which could be
// one of the following inputs from the caller:
//  - A full container ID, which will exact match a container in daemon's list
//  - A container name, which will only exact match via the GetByName() function
//  - A partial container ID prefix (e.g. short ID) of any length that is
//    unique enough to only return a single container object
//  If none of these searches succeed, an error is returned
func (daemon *Daemon) GetContainer(prefixOrName string) (*container.Container, error) {
	if len(prefixOrName) == 0 {
		return nil, derr.NewBadRequestError(fmt.Errorf("No container name or ID supplied"))
	}

	if containerByID := daemon.containers.Get(prefixOrName); containerByID != nil {
		// prefix is an exact match to a full container ID
		return containerByID, nil
	}

	// GetByName will match only an exact name provided; we ignore errors
	if containerByName, _ := daemon.GetByName(prefixOrName); containerByName != nil {
		// prefix is an exact match to a full container Name
		return containerByName, nil
	}

	containerID, indexError := daemon.idIndex.Get(prefixOrName)
	if indexError != nil {
		// When truncindex defines an error type, use that instead
		if indexError == truncindex.ErrNotExist {
			err := fmt.Errorf("No such container: %s", prefixOrName)
			return nil, derr.NewRequestNotFoundError(err)
		}
		return nil, indexError
	}
	return daemon.containers.Get(containerID), nil
}

// Exists returns a true if a container of the specified ID or name exists,
// false otherwise.
func (daemon *Daemon) Exists(id string) bool {
	c, _ := daemon.GetContainer(id)
	return c != nil
}

// IsPaused returns a bool indicating if the specified container is paused.
func (daemon *Daemon) IsPaused(id string) bool {
	c, _ := daemon.GetContainer(id)
	return c.State.IsPaused()
}

func (daemon *Daemon) containerRoot(id string) string {
	return filepath.Join(daemon.repository, id)
}

// Load reads the contents of a container from disk
// This is typically done at startup.
func (daemon *Daemon) load(id string) (*container.Container, error) {
	container := daemon.newBaseContainer(id)

	if err := container.FromDisk(); err != nil {
		return nil, err
	}

	if container.ID != id {
		return container, fmt.Errorf("Container %s is stored at %s", container.ID, id)
	}

	return container, nil
}

func (daemon *Daemon) registerName(container *container.Container) error {
	if daemon.Exists(container.ID) {
		return fmt.Errorf("Container is already loaded")
	}
	if err := validateID(container.ID); err != nil {
		return err
	}
	if container.Name == "" {
		name, err := daemon.generateNewName(container.ID)
		if err != nil {
			return err
		}
		container.Name = name

		if err := container.ToDiskLocking(); err != nil {
			logrus.Errorf("Error saving container name to disk: %v", err)
		}
	}
	return daemon.nameIndex.Reserve(container.Name, container.ID)
}

// Register makes a container object usable by the daemon as <container.ID>
func (daemon *Daemon) Register(c *container.Container) error {
	// Attach to stdout and stderr
	if c.Config.OpenStdin {
		c.NewInputPipes()
	} else {
		c.NewNopInputPipe()
	}

	daemon.containers.Add(c.ID, c)
	daemon.idIndex.Add(c.ID)

	return nil
}

func (daemon *Daemon) getEntrypointAndArgs(configEntrypoint strslice.StrSlice, configCmd strslice.StrSlice) (string, []string) {
	if len(configEntrypoint) != 0 {
		return configEntrypoint[0], append(configEntrypoint[1:], configCmd...)
	}
	return configCmd[0], configCmd[1:]
}

func (daemon *Daemon) newContainer(name string, config *containertypes.Config, imgID image.ID) (*container.Container, error) {
	var (
		id             string
		err            error
		noExplicitName = name == ""
	)
	id, name, err = daemon.generateIDAndName(name)
	if err != nil {
		return nil, err
	}

	daemon.generateHostname(id, config)
	entrypoint, args := daemon.getEntrypointAndArgs(config.Entrypoint, config.Cmd)

	base := daemon.newBaseContainer(id)
	base.Created = time.Now().UTC()
	base.Path = entrypoint
	base.Args = args //FIXME: de-duplicate from config
	base.Config = config
	base.HostConfig = &containertypes.HostConfig{}
	base.ImageID = imgID
	base.NetworkSettings = &network.Settings{IsAnonymousEndpoint: noExplicitName}
	base.Name = name
	base.Driver = daemon.GraphDriverName()

	return base, err
}

// GetByName returns a container given a name.
func (daemon *Daemon) GetByName(name string) (*container.Container, error) {
	if len(name) == 0 {
		return nil, fmt.Errorf("No container name supplied")
	}
	fullName := name
	if name[0] != '/' {
		fullName = "/" + name
	}
	id, err := daemon.nameIndex.Get(fullName)
	if err != nil {
		return nil, fmt.Errorf("Could not find entity for %s", name)
	}
	e := daemon.containers.Get(id)
	if e == nil {
		return nil, fmt.Errorf("Could not find container for entity id %s", id)
	}
	return e, nil
}

func (daemon *Daemon) shutdownContainer(c *container.Container) error {
	// TODO(windows): Handle docker restart with paused containers
	if c.IsPaused() {
		// To terminate a process in freezer cgroup, we should send
		// SIGTERM to this process then unfreeze it, and the process will
		// force to terminate immediately.
		logrus.Debugf("Found container %s is paused, sending SIGTERM before unpause it", c.ID)
		sig, ok := signal.SignalMap["TERM"]
		if !ok {
			return fmt.Errorf("System doesn not support SIGTERM")
		}
		if err := daemon.kill(c, int(sig)); err != nil {
			return fmt.Errorf("sending SIGTERM to container %s with error: %v", c.ID, err)
		}
		if err := daemon.containerUnpause(c); err != nil {
			return fmt.Errorf("Failed to unpause container %s with error: %v", c.ID, err)
		}
		if _, err := c.WaitStop(10 * time.Second); err != nil {
			logrus.Debugf("container %s failed to exit in 10 second of SIGTERM, sending SIGKILL to force", c.ID)
			sig, ok := signal.SignalMap["KILL"]
			if !ok {
				return fmt.Errorf("System does not support SIGKILL")
			}
			if err := daemon.kill(c, int(sig)); err != nil {
				logrus.Errorf("Failed to SIGKILL container %s", c.ID)
			}
			c.WaitStop(-1 * time.Second)
			return err
		}
	}
	// If container failed to exit in 10 seconds of SIGTERM, then using the force
	if err := daemon.containerStop(c, 10); err != nil {
		return fmt.Errorf("Stop container %s with error: %v", c.ID, err)
	}

	c.WaitStop(-1 * time.Second)
	return nil
}

// Mount sets container.BaseFS
// (is it not set coming in? why is it unset?)
func (daemon *Daemon) Mount(container *container.Container) error {
	dir, err := container.RWLayer.Mount(container.GetMountLabel())
	if err != nil {
		return err
	}
	logrus.Debugf("container mounted via layerStore: %v", dir)

	if container.BaseFS != dir {
		// The mount path reported by the graph driver should always be trusted on Windows, since the
		// volume path for a given mounted layer may change over time.  This should only be an error
		// on non-Windows operating systems.
		if container.BaseFS != "" && runtime.GOOS != "windows" {
			daemon.Unmount(container)
			return fmt.Errorf("Error: driver %s is returning inconsistent paths for container %s ('%s' then '%s')",
				daemon.GraphDriverName(), container.ID, container.BaseFS, dir)
		}
	}
	container.BaseFS = dir // TODO: combine these fields
	return nil
}

// Unmount unsets the container base filesystem
func (daemon *Daemon) Unmount(container *container.Container) error {
	if err := container.RWLayer.Unmount(); err != nil {
		logrus.Errorf("Error unmounting container %s: %s", container.ID, err)
		return err
	}
	return nil
}

func (daemon *Daemon) kill(c *container.Container, sig int) error {
	return daemon.containerd.Signal(c.ID, sig)
}

func (daemon *Daemon) subscribeToContainerStats(c *container.Container) chan interface{} {
	return daemon.statsCollector.collect(c)
}

func (daemon *Daemon) unsubscribeToContainerStats(c *container.Container, ch chan interface{}) {
	daemon.statsCollector.unsubscribe(c, ch)
}

func (daemon *Daemon) changes(container *container.Container) ([]archive.Change, error) {
	return container.RWLayer.Changes()
}

// verifyContainerSettings performs validation of the hostconfig and config
// structures.
func (daemon *Daemon) verifyContainerSettings(hostConfig *containertypes.HostConfig, config *containertypes.Config, update bool) ([]string, error) {

	// First perform verification of settings common across all platforms.
	if config != nil {
		if config.WorkingDir != "" {
			config.WorkingDir = filepath.FromSlash(config.WorkingDir) // Ensure in platform semantics
			if !system.IsAbs(config.WorkingDir) {
				return nil, fmt.Errorf("The working directory '%s' is invalid. It needs to be an absolute path", config.WorkingDir)
			}
		}

		if len(config.StopSignal) > 0 {
			_, err := signal.ParseSignal(config.StopSignal)
			if err != nil {
				return nil, err
			}
		}

		// Validate if the given hostname is RFC 1123 (https://tools.ietf.org/html/rfc1123) compliant.
		if len(config.Hostname) > 0 {
			// RFC1123 specifies that 63 bytes is the maximium length
			// Windows has the limitation of 63 bytes in length
			// Linux hostname is limited to HOST_NAME_MAX=64, not not including the terminating null byte.
			// We limit the length to 63 bytes here to match RFC1035 and RFC1123.
			matched, _ := regexp.MatchString("^(([[:alnum:]]|[[:alnum:]][[:alnum:]\\-]*[[:alnum:]])\\.)*([[:alnum:]]|[[:alnum:]][[:alnum:]\\-]*[[:alnum:]])$", config.Hostname)
			if len(config.Hostname) > 63 || !matched {
				return nil, fmt.Errorf("invalid hostname format: %s", config.Hostname)
			}
		}
	}

	if hostConfig == nil {
		return nil, nil
	}

	for port := range hostConfig.PortBindings {
		_, portStr := nat.SplitProtoPort(string(port))
		if _, err := nat.ParsePort(portStr); err != nil {
			return nil, fmt.Errorf("Invalid port specification: %q", portStr)
		}
		for _, pb := range hostConfig.PortBindings[port] {
			_, err := nat.NewPort(nat.SplitProtoPort(pb.HostPort))
			if err != nil {
				return nil, fmt.Errorf("Invalid port specification: %q", pb.HostPort)
			}
		}
	}

	// Now do platform-specific verification
	return verifyPlatformContainerSettings(daemon, hostConfig, config, update)
}

func (daemon *Daemon) setHostConfig(container *container.Container, hostConfig *containertypes.HostConfig) error {
	// Do not lock while creating volumes since this could be calling out to external plugins
	// Don't want to block other actions, like `docker ps` because we're waiting on an external plugin
	if err := daemon.registerMountPoints(container, hostConfig); err != nil {
		return err
	}

	container.Lock()
	defer container.Unlock()

	// Register any links from the host config before starting the container
	if err := daemon.registerLinks(container, hostConfig); err != nil {
		return err
	}

	// make sure links is not nil
	// this ensures that on the next daemon restart we don't try to migrate from legacy sqlite links
	if hostConfig.Links == nil {
		hostConfig.Links = []string{}
	}

	container.HostConfig = hostConfig
	return container.ToDisk()
}

func (daemon *Daemon) setSecurityOptions(container *container.Container, hostConfig *containertypes.HostConfig) error {
	container.Lock()
	defer container.Unlock()
	return parseSecurityOpt(container, hostConfig)
}
