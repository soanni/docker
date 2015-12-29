package main

import (
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/docker/docker/pkg/reexec"
	"github.com/go-check/check"
)

func Test(t *testing.T) {
	reexec.Init() // This is required for external graphdriver tests

	if !isLocalDaemon {
		fmt.Println("INFO: Testing against a remote daemon")
	} else {
		fmt.Println("INFO: Testing against a local daemon")
	}

	check.TestingT(t)
}

func init() {
	// TODO Start daemon
	// TODO Setup daemon (make sure stuff)
	setupDaemon()

	check.Suite(&DockerSuite{})
	// TODO Stop daemon
}

func setupDaemon() {
	cmd := exec.Command("bash", "../hack/make/.ensure-emptyfs")
	cmd.Env = append(cmd.Env, "DEST=../bundles")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_, err := runCommand(cmd)
	if err != nil {
		fmt.Printf("Error while setuping the default deamon", err)
		os.Exit(1)
	}
	// TODO ensureEmptyFs()
	// TODO ensureFrozenImages()
	// TODO ensureHttpServer()
}

type DockerSuite struct {
}

func (s *DockerSuite) TearDownTest(c *check.C) {
	deleteAllContainers()
	deleteAllImages()
	deleteAllVolumes()
	deleteAllNetworks()
}

func init() {
	check.Suite(&DockerRegistrySuite{
		ds: &DockerSuite{},
	})
}

type DockerRegistrySuite struct {
	ds  *DockerSuite
	reg *testRegistryV2
	d   *Daemon
}

func (s *DockerRegistrySuite) SetUpTest(c *check.C) {
	testRequires(c, DaemonIsLinux)
	s.reg = setupRegistry(c)
	s.d = NewDaemon(c)
}

func (s *DockerRegistrySuite) TearDownTest(c *check.C) {
	if s.reg != nil {
		s.reg.Close()
	}
	if s.ds != nil {
		s.ds.TearDownTest(c)
	}
	s.d.Stop()
}

func init() {
	check.Suite(&DockerDaemonSuite{
		ds: &DockerSuite{},
	})
}

type DockerDaemonSuite struct {
	ds *DockerSuite
	d  *Daemon
}

func (s *DockerDaemonSuite) SetUpTest(c *check.C) {
	testRequires(c, DaemonIsLinux)
	s.d = NewDaemon(c)
}

func (s *DockerDaemonSuite) TearDownTest(c *check.C) {
	testRequires(c, DaemonIsLinux)
	s.d.Stop()
	s.ds.TearDownTest(c)
}

func init() {
	check.Suite(&DockerTrustSuite{
		ds: &DockerSuite{},
	})
}

type DockerTrustSuite struct {
	ds  *DockerSuite
	reg *testRegistryV2
	not *testNotary
}

func (s *DockerTrustSuite) SetUpTest(c *check.C) {
	s.reg = setupRegistry(c)
	s.not = setupNotary(c)
}

func (s *DockerTrustSuite) TearDownTest(c *check.C) {
	s.reg.Close()
	s.not.Close()
	s.ds.TearDownTest(c)
}
