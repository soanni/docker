// +build windows

package daemon

import (
	"fmt"

	"github.com/docker/docker/container"
	networktypes "github.com/docker/engine-api/types/network"
	"github.com/docker/libnetwork"
)

func (daemon *Daemon) setupLinkedContainers(container *container.Container) ([]string, error) {
	return nil, nil
}

// ConnectToNetwork connects a container to a network
func (daemon *Daemon) ConnectToNetwork(container *container.Container, idOrName string, endpointConfig *networktypes.EndpointSettings) error {
	return fmt.Errorf("Windows does not support connecting a running container to a network")
}

// DisconnectFromNetwork disconnects container from a network.
func (daemon *Daemon) DisconnectFromNetwork(container *container.Container, n libnetwork.Network, force bool) error {
	return fmt.Errorf("Windows does not support disconnecting a running container from a network")
}

func isLinkable(child *container.Container) bool {
	return false
}
