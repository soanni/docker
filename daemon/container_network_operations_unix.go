// +build linux freebsd

package daemon

import (
	"fmt"

	"github.com/docker/docker/container"
	"github.com/docker/docker/daemon/links"
	"github.com/docker/docker/runconfig"
	containertypes "github.com/docker/engine-api/types/container"
	networktypes "github.com/docker/engine-api/types/network"
	"github.com/docker/libnetwork"
)

func (daemon *Daemon) setupLinkedContainers(container *container.Container) ([]string, error) {
	var env []string
	children := daemon.children(container)

	bridgeSettings := container.NetworkSettings.Networks[runconfig.DefaultDaemonNetworkMode().NetworkName()]
	if bridgeSettings == nil {
		return nil, nil
	}

	for linkAlias, child := range children {
		if !child.IsRunning() {
			return nil, fmt.Errorf("Cannot link to a non running container: %s AS %s", child.Name, linkAlias)
		}

		childBridgeSettings := child.NetworkSettings.Networks[runconfig.DefaultDaemonNetworkMode().NetworkName()]
		if childBridgeSettings == nil {
			return nil, fmt.Errorf("container %s not attached to default bridge network", child.ID)
		}

		link := links.NewLink(
			bridgeSettings.IPAddress,
			childBridgeSettings.IPAddress,
			linkAlias,
			child.Config.Env,
			child.Config.ExposedPorts,
		)

		for _, envVar := range link.ToEnv() {
			env = append(env, envVar)
		}
	}

	return env, nil
}

// ConnectToNetwork connects a container to a network
func (daemon *Daemon) ConnectToNetwork(container *container.Container, idOrName string, endpointConfig *networktypes.EndpointSettings) error {
	if endpointConfig == nil {
		endpointConfig = &networktypes.EndpointSettings{}
	}
	if !container.Running {
		if container.RemovalInProgress || container.Dead {
			return errRemovalContainer(container.ID)
		}
		if _, err := daemon.updateNetworkConfig(container, idOrName, endpointConfig, true); err != nil {
			return err
		}
		container.NetworkSettings.Networks[idOrName] = endpointConfig
	} else {
		if err := daemon.connectToNetwork(container, idOrName, endpointConfig, true); err != nil {
			return err
		}
	}
	if err := container.ToDiskLocking(); err != nil {
		return fmt.Errorf("Error saving container to disk: %v", err)
	}
	return nil
}

// DisconnectFromNetwork disconnects container from network n.
func (daemon *Daemon) DisconnectFromNetwork(container *container.Container, n libnetwork.Network, force bool) error {
	if container.HostConfig.NetworkMode.IsHost() && containertypes.NetworkMode(n.Type()).IsHost() {
		return runconfig.ErrConflictHostNetwork
	}
	if !container.Running {
		if container.RemovalInProgress || container.Dead {
			return errRemovalContainer(container.ID)
		}
		if _, ok := container.NetworkSettings.Networks[n.Name()]; ok {
			delete(container.NetworkSettings.Networks, n.Name())
		} else {
			return fmt.Errorf("container %s is not connected to the network %s", container.ID, n.Name())
		}
	} else {
		if err := disconnectFromNetwork(container, n, false); err != nil {
			return err
		}
	}

	if err := container.ToDiskLocking(); err != nil {
		return fmt.Errorf("Error saving container to disk: %v", err)
	}

	attributes := map[string]string{
		"container": container.ID,
	}
	daemon.LogNetworkEventWithAttributes(n, "disconnect", attributes)
	return nil
}

func (daemon *Daemon) getIpcContainer(container *container.Container) (*container.Container, error) {
	containerID := container.HostConfig.IpcMode.Container()
	c, err := daemon.GetContainer(containerID)
	if err != nil {
		return nil, err
	}
	if !c.IsRunning() {
		return nil, fmt.Errorf("cannot join IPC of a non running container: %s", containerID)
	}
	if c.IsRestarting() {
		return nil, errContainerIsRestarting(container.ID)
	}
	return c, nil
}

func isLinkable(child *container.Container) bool {
	// A container is linkable only if it belongs to the default network
	_, ok := child.NetworkSettings.Networks[runconfig.DefaultDaemonNetworkMode().NetworkName()]
	return ok
}

func errRemovalContainer(containerID string) error {
	return fmt.Errorf("Container %s is marked for removal and cannot be connected or disconnected to the network", containerID)
}
