package daemon

import (
	"fmt"

	"github.com/Sirupsen/logrus"
	"github.com/docker/docker/pkg/namesgenerator"
	"github.com/docker/docker/pkg/registrar"
	"github.com/docker/docker/pkg/stringid"
	"github.com/docker/docker/utils"
	containertypes "github.com/docker/engine-api/types/container"
)

var (
	validContainerNameChars   = utils.RestrictedNameChars
	validContainerNamePattern = utils.RestrictedNamePattern
)

func (daemon *Daemon) generateIDAndName(name string) (string, string, error) {
	var (
		err error
		id  = stringid.GenerateNonCryptoID()
	)

	if name == "" {
		if name, err = daemon.generateNewName(id); err != nil {
			return "", "", err
		}
		return id, name, nil
	}

	if name, err = daemon.reserveName(id, name); err != nil {
		return "", "", err
	}

	return id, name, nil
}

func (daemon *Daemon) reserveName(id, name string) (string, error) {
	if !validContainerNamePattern.MatchString(name) {
		return "", fmt.Errorf("Invalid container name (%s), only %s are allowed", name, validContainerNameChars)
	}
	if name[0] != '/' {
		name = "/" + name
	}

	if err := daemon.nameIndex.Reserve(name, id); err != nil {
		if err == registrar.ErrNameReserved {
			id, err := daemon.nameIndex.Get(name)
			if err != nil {
				logrus.Errorf("got unexpected error while looking up reserved name: %v", err)
				return "", err
			}
			return "", fmt.Errorf("Conflict. The name %q is already in use by container %s. You have to remove (or rename) that container to be able to reuse that name.", name, id)
		}
		return "", fmt.Errorf("error reserving name: %s, error: %v", name, err)
	}
	return name, nil
}

func (daemon *Daemon) releaseName(name string) {
	daemon.nameIndex.Release(name)
}

func (daemon *Daemon) generateNewName(id string) (string, error) {
	var name string
	for i := 0; i < 6; i++ {
		name = namesgenerator.GetRandomName(i)
		if name[0] != '/' {
			name = "/" + name
		}

		if err := daemon.nameIndex.Reserve(name, id); err != nil {
			if err == registrar.ErrNameReserved {
				continue
			}
			return "", err
		}
		return name, nil
	}

	name = "/" + stringid.TruncateID(id)
	if err := daemon.nameIndex.Reserve(name, id); err != nil {
		return "", err
	}
	return name, nil
}

func (daemon *Daemon) generateHostname(id string, config *containertypes.Config) {
	// Generate default hostname
	if config.Hostname == "" {
		config.Hostname = id[:12]
	}
}
