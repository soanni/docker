package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/pkg/integration/checker"
	"github.com/docker/docker/pkg/nat"
	"github.com/docker/docker/runconfig"
	"github.com/docker/libnetwork/resolvconf"
	"github.com/go-check/check"
)

// "test123" should be printed by docker run
func (s *DockerSuite) TestRunEchoStdout(c *check.C) {
	out, _ := dockerCmd(c, "run", "busybox", "echo", "test123")
	c.Assert(out, checker.Equals, "test123\n", check.Commentf("container should've printed 'test123', got '%s'", out))
}

// "test" should be printed
func (s *DockerSuite) TestRunEchoNamedContainer(c *check.C) {
	out, _ := dockerCmd(c, "run", "--name", "testfoonamedcontainer", "busybox", "echo", "test")
	c.Assert(out, checker.Equals, "test\n")
}

// docker run should not leak file descriptors. This test relies on Unix
// specific functionality and cannot run on Windows.
func (s *DockerSuite) TestRunLeakyFileDescriptors(c *check.C) {
	testRequires(c, DaemonIsLinux)
	out, _ := dockerCmd(c, "run", "busybox", "ls", "-C", "/proc/self/fd")

	// normally, we should only get 0, 1, and 2, but 3 gets created by "ls" when it does "opendir" on the "fd" directory
	c.Assert(out, checker.Equals, "0  1  2  3\n")
}

// it should be possible to lookup Google DNS
// this will fail when Internet access is unavailable
func (s *DockerSuite) TestRunLookupGoogleDns(c *check.C) {
	testRequires(c, Network)
	image := DefaultImage
	if daemonPlatform == "windows" {
		// nslookup isn't present in Windows busybox. Is built-in.
		image = WindowsBaseImage
	}
	dockerCmd(c, "run", image, "nslookup", "google.com")
}

// the exit code should be 0
// some versions of lxc might make this test fail
func (s *DockerSuite) TestRunExitCodeZero(c *check.C) {
	dockerCmd(c, "run", "busybox", "true")
}

// the exit code should be 1
// some versions of lxc might make this test fail
func (s *DockerSuite) TestRunExitCodeOne(c *check.C) {
	_, exitCode, err := dockerCmdWithError("run", "busybox", "false")
	if err != nil && !strings.Contains("exit status 1", fmt.Sprintf("%s", err)) {
		c.Fatal(err)
	}
	c.Assert(exitCode, checker.Equals, 1)
}

// it should be possible to pipe in data via stdin to a process running in a container
// some versions of lxc might make this test fail
func (s *DockerSuite) TestRunStdinPipe(c *check.C) {
	// TODO Windows: This needs some work to make compatible.
	testRequires(c, DaemonIsLinux)
	runCmd := exec.Command(dockerBinary, "run", "-i", "-a", "stdin", "busybox", "cat")
	runCmd.Stdin = strings.NewReader("blahblah")
	out, _, _, err := runCommandWithStdoutStderr(runCmd)
	c.Assert(err, checker.IsNil, check.Commentf("failed to run container: %v, output: %q", err, out))

	out = strings.TrimSpace(out)
	dockerCmd(c, "wait", out)

	logsOut, _ := dockerCmd(c, "logs", out)

	defer dockerCmd(c, "rm", out)

	containerLogs := strings.TrimSpace(logsOut)
	c.Assert(containerLogs, checker.Equals, "blahblah", check.Commentf("logs didn't print the container's logs %s", containerLogs))

}

// the container's ID should be printed when starting a container in detached mode
func (s *DockerSuite) TestRunDetachedContainerIDPrinting(c *check.C) {
	out, _ := dockerCmd(c, "run", "-d", "busybox", "true")

	out = strings.TrimSpace(out)
	dockerCmd(c, "wait", out)

	rmOut, _ := dockerCmd(c, "rm", out)

	rmOut = strings.TrimSpace(rmOut)
	c.Assert(strings.TrimSpace(rmOut), checker.Equals, out, check.Commentf("rm didn't print the container ID %s %s", out, rmOut))
}

// the working directory should be set correctly
func (s *DockerSuite) TestRunWorkingDirectory(c *check.C) {
	// TODO Windows: There's a Windows bug stopping this from working.
	testRequires(c, DaemonIsLinux)
	dir := "/root"
	image := "busybox"
	if daemonPlatform == "windows" {
		dir = `/windows`
		image = WindowsBaseImage
	}

	// First with -w
	out, _ := dockerCmd(c, "run", "-w", dir, image, "pwd")
	c.Assert(strings.TrimSpace(out), checker.Equals, dir, check.Commentf("-w failed to set working directory"))

	// Then with --workdir
	out, _ = dockerCmd(c, "run", "--workdir", dir, image, "pwd")
	c.Assert(strings.TrimSpace(out), checker.Equals, dir, check.Commentf("--workdir failed to set working directory"))
}

// pinging Google's DNS resolver should fail when we disable the networking
func (s *DockerSuite) TestRunWithoutNetworking(c *check.C) {
	count := "-c"
	image := "busybox"
	if daemonPlatform == "windows" {
		count = "-n"
		image = WindowsBaseImage
	}

	// First using the long form --net
	out, exitCode, err := dockerCmdWithError("run", "--net=none", image, "ping", count, "1", "8.8.8.8")
	c.Assert(err, checker.NotNil, check.Commentf(out))
	c.Assert(exitCode, checker.Equals, 1, check.Commentf("--net=none should've disabled the network; the container shouldn't have been able to ping 8.8.8.8"))

	// And then with the short form -n
	out, exitCode, err = dockerCmdWithError("run", "-n=false", image, "ping", count, "1", "8.8.8.8")
	c.Assert(err, checker.NotNil, check.Commentf(out))
	c.Assert(exitCode, checker.Equals, 1, check.Commentf("-n=false should've disabled the network; the container shouldn't have been able to ping 8.8.8.8"))
}

//test --link use container name to link target
func (s *DockerSuite) TestRunLinksContainerWithContainerName(c *check.C) {
	// TODO Windows: This test cannot run on a Windows daemon as the networking
	// settings are not populated back yet on inspect.
	testRequires(c, DaemonIsLinux)
	dockerCmd(c, "run", "-i", "-t", "-d", "--name", "parent", "busybox")

	ip, err := inspectField("parent", "NetworkSettings.IPAddress")
	c.Assert(err, check.IsNil)

	out, _ := dockerCmd(c, "run", "--link", "parent:test", "busybox", "/bin/cat", "/etc/hosts")
	c.Assert(out, checker.Contains, ip+"	test", check.Commentf("use a container name to link target failed"))
}

//test --link use container id to link target
func (s *DockerSuite) TestRunLinksContainerWithContainerId(c *check.C) {
	// TODO Windows: This test cannot run on a Windows daemon as the networking
	// settings are not populated back yet on inspect.
	testRequires(c, DaemonIsLinux)
	cID, _ := dockerCmd(c, "run", "-i", "-t", "-d", "busybox")

	cID = strings.TrimSpace(cID)
	ip, err := inspectField(cID, "NetworkSettings.IPAddress")
	c.Assert(err, check.IsNil)

	out, _ := dockerCmd(c, "run", "--link", cID+":test", "busybox", "/bin/cat", "/etc/hosts")
	c.Assert(out, checker.Contains, ip+"	test", check.Commentf("use a container name to link target failed"))
}

// Issue 9677.
// FIXME Split this one into two with testRequires (client / server)
func (s *DockerSuite) TestRunWithDaemonFlags(c *check.C) {
	out, _, err := dockerCmdWithError("--exec-opt", "foo=bar", "run", "-i", "-t", "busybox", "true")
	// TODO
	if err != nil {
		if !strings.Contains(out, "must follow the 'docker daemon' command") && // daemon
			!strings.Contains(out, "flag provided but not defined: --exec-opt") { // no daemon (client-only)
			c.Fatal(err, out)
		}
	}
}

// Regression test for #4979
func (s *DockerSuite) TestRunWithVolumesFromExited(c *check.C) {
	// TODO Windows: This test cannot run on a Windows daemon as Windows does
	// not support volumes
	testRequires(c, DaemonIsLinux)
	dockerCmd(c, "run", "--name", "test-data", "--volume", "/some/dir", "busybox", "touch", "/some/dir/file")

	dockerCmd(c, "run", "--volumes-from", "test-data", "busybox", "cat", "/some/dir/file")
}

// Volume path is a symlink which also exists on the host, and the host side is a file not a dir
// But the volume call is just a normal volume, not a bind mount
func (s *DockerSuite) TestRunCreateVolumesInSymlinkDir(c *check.C) {
	// TODO Windows: This test cannot run on a Windows daemon as Windows does
	// not support volumes
	testRequires(c, DaemonIsLinux, SameHostDaemon, NativeExecDriver)
	name := "test-volume-symlink"

	dir, err := ioutil.TempDir("", name)
	c.Assert(err, checker.IsNil)
	defer os.RemoveAll(dir)

	f, err := os.OpenFile(filepath.Join(dir, "test"), os.O_CREATE, 0700)
	c.Assert(err, checker.IsNil)
	f.Close()

	dockerFile := fmt.Sprintf("FROM busybox\nRUN mkdir -p %s\nRUN ln -s %s /test", dir, dir)
	_, err = buildImage(name, dockerFile, false)
	c.Assert(err, checker.IsNil)

	dockerCmd(c, "run", "-v", "/test/test", name)
}

func (s *DockerSuite) TestRunVolumesMountedAsReadonly(c *check.C) {
	// TODO Windows: This test cannot run on a Windows daemon as Windows does
	// not support volumes
	testRequires(c, DaemonIsLinux)
	_, code, err := dockerCmdWithError("run", "-v", "/test:/test:ro", "busybox", "touch", "/test/somefile")
	c.Assert(err, checker.NotNil)
	c.Assert(code, checker.Not(checker.Equals), 0, check.Commentf("run should fail because volume is ro"))
}

func (s *DockerSuite) TestRunVolumesFromInReadonlyMode(c *check.C) {
	// TODO Windows: This test cannot run on a Windows daemon as Windows does
	// not support volumes
	testRequires(c, DaemonIsLinux)
	dockerCmd(c, "run", "--name", "parent", "-v", "/test", "busybox", "true")

	_, code, err := dockerCmdWithError("run", "--volumes-from", "parent:ro", "busybox", "touch", "/test/file")
	c.Assert(err, checker.NotNil)
	c.Assert(code, checker.Not(checker.Equals), 0, check.Commentf("run should fail because volume is ro"))
}

// Regression test for #1201
func (s *DockerSuite) TestRunVolumesFromInReadWriteMode(c *check.C) {
	// TODO Windows: This test cannot run on a Windows daemon as Windows does
	// not support volumes
	testRequires(c, DaemonIsLinux)
	dockerCmd(c, "run", "--name", "parent", "-v", "/test", "busybox", "true")
	dockerCmd(c, "run", "--volumes-from", "parent:rw", "busybox", "touch", "/test/file")

	out, _, err := dockerCmdWithError("run", "--volumes-from", "parent:bar", "busybox", "touch", "/test/file")
	c.Assert(err, checker.NotNil, check.Commentf("running --volumes-from foo:bar should have failed with invalid mode: %q", out))
	c.Assert(out, checker.Contains, "invalid mode: bar", check.Commentf("running --volumes-from foo:bar should have failed with invalid mode"))

	dockerCmd(c, "run", "--volumes-from", "parent", "busybox", "touch", "/test/file")
}

func (s *DockerSuite) TestVolumesFromGetsProperMode(c *check.C) {
	// TODO Windows: This test cannot run on a Windows daemon as Windows does
	// not support volumes
	testRequires(c, DaemonIsLinux)
	dockerCmd(c, "run", "--name", "parent", "-v", "/test:/test:ro", "busybox", "true")

	// Expect this "rw" mode to be be ignored since the inherited volume is "ro"
	_, _, err := dockerCmdWithError("run", "--volumes-from", "parent:rw", "busybox", "touch", "/test/file")
	c.Assert(err, checker.NotNil, check.Commentf("Expected volumes-from to inherit read-only volume even when passing in `rw`"))

	dockerCmd(c, "run", "--name", "parent2", "-v", "/test:/test:ro", "busybox", "true")

	// Expect this to be read-only since both are "ro"
	_, _, err = dockerCmdWithError("run", "--volumes-from", "parent2:ro", "busybox", "touch", "/test/file")
	c.Assert(err, checker.NotNil, check.Commentf("Expected volumes-from to inherit read-only volume even when passing in `ro`"))
}

// Test for GH#10618
func (s *DockerSuite) TestRunNoDupVolumes(c *check.C) {
	// TODO Windows: This test cannot run on a Windows daemon as Windows does
	// not support volumes
	testRequires(c, DaemonIsLinux)
	mountstr1 := randomTmpDirPath("test1", daemonPlatform) + ":/someplace"
	mountstr2 := randomTmpDirPath("test2", daemonPlatform) + ":/someplace"

	out, _, err := dockerCmdWithError("run", "-v", mountstr1, "-v", mountstr2, "busybox", "true")
	c.Assert(err, checker.NotNil, check.Commentf("Expected error about duplicate volume definitions"))
	c.Assert(out, checker.Contains, "Duplicate bind mount", check.Commentf("Expected 'duplicate volume' error, got %v", err))
}

// Test for #1351
func (s *DockerSuite) TestRunApplyVolumesFromBeforeVolumes(c *check.C) {
	// TODO Windows: This test cannot run on a Windows daemon as Windows does
	// not support volumes
	testRequires(c, DaemonIsLinux)
	dockerCmd(c, "run", "--name", "parent", "-v", "/test", "busybox", "touch", "/test/foo")
	dockerCmd(c, "run", "--volumes-from", "parent", "-v", "/test", "busybox", "cat", "/test/foo")
}

func (s *DockerSuite) TestRunMultipleVolumesFrom(c *check.C) {
	// TODO Windows: This test cannot run on a Windows daemon as Windows does
	// not support volumes
	testRequires(c, DaemonIsLinux)
	dockerCmd(c, "run", "--name", "parent1", "-v", "/test", "busybox", "touch", "/test/foo")
	dockerCmd(c, "run", "--name", "parent2", "-v", "/other", "busybox", "touch", "/other/bar")
	dockerCmd(c, "run", "--volumes-from", "parent1", "--volumes-from", "parent2", "busybox", "sh", "-c", "cat /test/foo && cat /other/bar")
}

// this tests verifies the ID format for the container
func (s *DockerSuite) TestRunVerifyContainerID(c *check.C) {
	out, _ := dockerCmd(c, "run", "-d", "busybox", "true")

	c.Assert(strings.TrimSuffix(out, "\n"), checker.Matches, "^[0-9a-f]{64}$", check.Commentf("Invalid container ID: %s", out))
}

// Test that creating a container with a volume doesn't crash. Regression test for #995.
func (s *DockerSuite) TestRunCreateVolume(c *check.C) {
	// TODO Windows: This test cannot run on a Windows daemon as Windows does
	// not support volumes
	testRequires(c, DaemonIsLinux)
	dockerCmd(c, "run", "-v", "/var/lib/data", "busybox", "true")
}

// Test that creating a volume with a symlink in its path works correctly. Test for #5152.
// Note that this bug happens only with symlinks with a target that starts with '/'.
func (s *DockerSuite) TestRunCreateVolumeWithSymlink(c *check.C) {
	// TODO Windows: This test cannot run on a Windows daemon as Windows does
	// not support volumes
	testRequires(c, DaemonIsLinux)
	image := "docker-test-createvolumewithsymlink"

	buildCmd := exec.Command(dockerBinary, "build", "-t", image, "-")
	buildCmd.Stdin = strings.NewReader(`FROM busybox
		RUN ln -s home /bar`)
	buildCmd.Dir = workingDirectory
	err := buildCmd.Run()
	c.Assert(err, checker.IsNil, check.Commentf("could not build '%s': %v", image, err))

	dockerCmd(c, "run", "-v", "/bar/foo", "--name", "test-createvolumewithsymlink", image, "sh", "-c", "mount | grep -q /home/foo")

	volPath, err := inspectMountSourceField("test-createvolumewithsymlink", "/bar/foo")
	c.Assert(err, checker.IsNil, check.Commentf("[inspect] err: %v", err))

	dockerCmd(c, "rm", "-v", "test-createvolumewithsymlink")

	_, err = os.Stat(volPath)
	c.Assert(os.IsNotExist(err), checker.True, check.Commentf("[open] (expecting 'file does not exist' error) err: %v, volPath: %s", err, volPath))
}

// Tests that a volume path that has a symlink exists in a container mounting it with `--volumes-from`.
func (s *DockerSuite) TestRunVolumesFromSymlinkPath(c *check.C) {
	// TODO Windows: This test cannot run on a Windows daemon as Windows does
	// not support volumes
	testRequires(c, DaemonIsLinux)
	name := "docker-test-volumesfromsymlinkpath"

	buildCmd := exec.Command(dockerBinary, "build", "-t", name, "-")
	buildCmd.Stdin = strings.NewReader(`FROM busybox
		RUN ln -s home /foo
		VOLUME ["/foo/bar"]`)
	buildCmd.Dir = workingDirectory
	err := buildCmd.Run()
	c.Assert(err, checker.IsNil, check.Commentf("could not build 'docker-test-volumesfromsymlinkpath': %v", err))

	dockerCmd(c, "run", "--name", "test-volumesfromsymlinkpath", name)

	dockerCmd(c, "run", "--volumes-from", "test-volumesfromsymlinkpath", "busybox", "sh", "-c", "ls /foo | grep -q bar")
}

func (s *DockerSuite) TestRunExitCode(c *check.C) {
	_, exitCode, err := dockerCmdWithError("run", "busybox", "/bin/sh", "-c", "exit 72")

	c.Assert(err, checker.NotNil)
	c.Assert(exitCode, checker.Equals, 72)
}

func (s *DockerSuite) TestRunUserDefaults(c *check.C) {
	expected := "uid=0(root) gid=0(root)"
	if daemonPlatform == "windows" {
		expected = "uid=1000(SYSTEM) gid=1000(SYSTEM)"
	}
	out, _ := dockerCmd(c, "run", "busybox", "id")
	c.Assert(out, checker.Contains, expected)
}

func (s *DockerSuite) TestRunUserByName(c *check.C) {
	// TODO Windows: This test cannot run on a Windows daemon as Windows does
	// not support the use of -u
	testRequires(c, DaemonIsLinux)
	out, _ := dockerCmd(c, "run", "-u", "root", "busybox", "id")
	c.Assert(out, checker.Contains, "uid=0(root) gid=0(root)", check.Commentf("expected root user got %s", out))
}

func (s *DockerSuite) TestRunUserByID(c *check.C) {
	// TODO Windows: This test cannot run on a Windows daemon as Windows does
	// not support the use of -u
	testRequires(c, DaemonIsLinux)
	out, _ := dockerCmd(c, "run", "-u", "1", "busybox", "id")
	c.Assert(out, checker.Contains, "uid=1(daemon) gid=1(daemon)", check.Commentf("expected daemon user got %s", out))
}

func (s *DockerSuite) TestRunUserByIDBig(c *check.C) {
	// TODO Windows: This test cannot run on a Windows daemon as Windows does
	// not support the use of -u
	testRequires(c, DaemonIsLinux)
	out, _, err := dockerCmdWithError("run", "-u", "2147483648", "busybox", "id")
	c.Assert(err, checker.NotNil, check.Commentf("No error, but must be.", out))
	c.Assert(out, checker.Contains, "Uids and gids must be in range", check.Commentf("expected error about uids range, got %s", out))
}

func (s *DockerSuite) TestRunUserByIDNegative(c *check.C) {
	// TODO Windows: This test cannot run on a Windows daemon as Windows does
	// not support the use of -u
	testRequires(c, DaemonIsLinux)
	out, _, err := dockerCmdWithError("run", "-u", "-1", "busybox", "id")
	c.Assert(err, checker.NotNil, check.Commentf("No error, but must be.", out))
	c.Assert(out, checker.Contains, "Uids and gids must be in range", check.Commentf("expected error about uids range, got %s", out))
}

func (s *DockerSuite) TestRunUserByIDZero(c *check.C) {
	// TODO Windows: This test cannot run on a Windows daemon as Windows does
	// not support the use of -u
	testRequires(c, DaemonIsLinux)
	out, _ := dockerCmd(c, "run", "-u", "0", "busybox", "id")
	c.Assert(out, checker.Contains, "uid=0(root) gid=0(root) groups=10(wheel)", check.Commentf("expected daemon user got %s", out))
}

func (s *DockerSuite) TestRunUserNotFound(c *check.C) {
	// TODO Windows: This test cannot run on a Windows daemon as Windows does
	// not support the use of -u
	testRequires(c, DaemonIsLinux)
	_, _, err := dockerCmdWithError("run", "-u", "notme", "busybox", "id")
	c.Assert(err, checker.NotNil, check.Commentf("unknown user should cause container to fail"))
}

func (s *DockerSuite) TestRunTwoConcurrentContainers(c *check.C) {
	sleepTime := "2"
	if daemonPlatform == "windows" {
		sleepTime = "5" // Make more reliable on Windows
	}
	group := sync.WaitGroup{}
	group.Add(2)

	errChan := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			defer group.Done()
			_, _, err := dockerCmdWithError("run", "busybox", "sleep", sleepTime)
			errChan <- err
		}()
	}

	group.Wait()
	close(errChan)

	for err := range errChan {
		c.Assert(err, check.IsNil)
	}
}

func (s *DockerSuite) TestRunEnvironment(c *check.C) {
	// TODO Windows: Environment handling is different between Linux and
	// Windows and this test relies currently on lxc and unix functionality.
	testRequires(c, DaemonIsLinux)
	cmd := exec.Command(dockerBinary, "run", "-h", "testing", "-e=FALSE=true", "-e=TRUE", "-e=TRICKY", "-e=HOME=", "busybox", "env")
	cmd.Env = append(os.Environ(),
		"TRUE=false",
		"TRICKY=tri\ncky\n",
	)

	out, _, err := runCommandWithOutput(cmd)
	c.Assert(err, checker.IsNil, check.Commentf(out))

	actualEnvLxc := strings.Split(strings.TrimSpace(out), "\n")
	actualEnv := []string{}
	for i := range actualEnvLxc {
		if actualEnvLxc[i] != "container=lxc" {
			actualEnv = append(actualEnv, actualEnvLxc[i])
		}
	}
	sort.Strings(actualEnv)

	goodEnv := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOSTNAME=testing",
		"FALSE=true",
		"TRUE=false",
		"TRICKY=tri",
		"cky",
		"",
		"HOME=/root",
	}
	sort.Strings(goodEnv)
	c.Assert(len(goodEnv), checker.Equals, len(actualEnv), check.Commentf("Wrong environment: should be %d variables, not: %q\n", len(goodEnv), strings.Join(actualEnv, ", ")))
	// FIXME validate this
	c.Assert(goodEnv, checker.DeepEquals, actualEnv)
	// for i := range goodEnv {
	// 	if actualEnv[i] != goodEnv[i] {
	// 		c.Fatalf("Wrong environment variable: should be %s, not %s", goodEnv[i], actualEnv[i])
	// 	}
	// }
}

func (s *DockerSuite) TestRunEnvironmentErase(c *check.C) {
	// TODO Windows: Environment handling is different between Linux and
	// Windows and this test relies currently on lxc and unix functionality.
	testRequires(c, DaemonIsLinux)

	// Test to make sure that when we use -e on env vars that are
	// not set in our local env that they're removed (if present) in
	// the container

	cmd := exec.Command(dockerBinary, "run", "-e", "FOO", "-e", "HOSTNAME", "busybox", "env")
	cmd.Env = appendBaseEnv([]string{})

	out, _, err := runCommandWithOutput(cmd)
	c.Assert(err, checker.IsNil, check.Commentf(out))

	actualEnvLxc := strings.Split(strings.TrimSpace(out), "\n")
	actualEnv := []string{}
	for i := range actualEnvLxc {
		if actualEnvLxc[i] != "container=lxc" {
			actualEnv = append(actualEnv, actualEnvLxc[i])
		}
	}
	sort.Strings(actualEnv)

	goodEnv := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root",
	}
	sort.Strings(goodEnv)
	c.Assert(len(goodEnv), checker.Equals, len(actualEnv), check.Commentf("Wrong environment: should be %d variables, not: %q\n", len(goodEnv), strings.Join(actualEnv, ", ")))
	// FIXME validate this
	c.Assert(goodEnv, checker.DeepEquals, actualEnv)
	// for i := range goodEnv {
	// 	if actualEnv[i] != goodEnv[i] {
	// 		c.Fatalf("Wrong environment variable: should be %s, not %s", goodEnv[i], actualEnv[i])
	// 	}
	// }
}

func (s *DockerSuite) TestRunEnvironmentOverride(c *check.C) {
	// TODO Windows: Environment handling is different between Linux and
	// Windows and this test relies currently on lxc and unix functionality.
	testRequires(c, DaemonIsLinux)

	// Test to make sure that when we use -e on env vars that are
	// already in the env that we're overriding them

	cmd := exec.Command(dockerBinary, "run", "-e", "HOSTNAME", "-e", "HOME=/root2", "busybox", "env")
	cmd.Env = appendBaseEnv([]string{"HOSTNAME=bar"})

	out, _, err := runCommandWithOutput(cmd)
	c.Assert(err, checker.IsNil, check.Commentf(out))

	actualEnvLxc := strings.Split(strings.TrimSpace(out), "\n")
	actualEnv := []string{}
	for i := range actualEnvLxc {
		if actualEnvLxc[i] != "container=lxc" {
			actualEnv = append(actualEnv, actualEnvLxc[i])
		}
	}
	sort.Strings(actualEnv)

	goodEnv := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root2",
		"HOSTNAME=bar",
	}
	sort.Strings(goodEnv)

	c.Assert(len(goodEnv), checker.Equals, len(actualEnv), check.Commentf("Wrong environment: should be %d variables, not: %q\n", len(goodEnv), strings.Join(actualEnv, ", ")))
	// FIXME validate this
	c.Assert(goodEnv, checker.DeepEquals, actualEnv)
	// for i := range goodEnv {
	// 	if actualEnv[i] != goodEnv[i] {
	// 		c.Fatalf("Wrong environment variable: should be %s, not %s", goodEnv[i], actualEnv[i])
	// 	}
	// }
}

func (s *DockerSuite) TestRunContainerNetwork(c *check.C) {
	if daemonPlatform == "windows" {
		// Windows busybox does not have ping. Use built in ping instead.
		dockerCmd(c, "run", WindowsBaseImage, "ping", "-n", "1", "127.0.0.1")
	} else {
		dockerCmd(c, "run", "busybox", "ping", "-c", "1", "127.0.0.1")
	}
}

func (s *DockerSuite) TestRunNetHostNotAllowedWithLinks(c *check.C) {
	// TODO Windows: This is Linux specific as --link is not supported and
	// this will be deprecated in favour of container networking model.
	testRequires(c, DaemonIsLinux, NotUserNamespace)
	dockerCmd(c, "run", "--name", "linked", "busybox", "true")

	_, _, err := dockerCmdWithError("run", "--net=host", "--link", "linked:linked", "busybox", "true")
	c.Assert(err, checker.NotNil, check.Commentf("Expected error"))
}

// #7851 hostname outside container shows FQDN, inside only shortname
// For testing purposes it is not required to set host's hostname directly
// and use "--net=host" (as the original issue submitter did), as the same
// codepath is executed with "docker run -h <hostname>".  Both were manually
// tested, but this testcase takes the simpler path of using "run -h .."
func (s *DockerSuite) TestRunFullHostnameSet(c *check.C) {
	// TODO Windows: -h is not yet functional.
	testRequires(c, DaemonIsLinux)
	out, _ := dockerCmd(c, "run", "-h", "foo.bar.baz", "busybox", "hostname")
	c.Assert(out, checker.Contains, "foo.bar.baz", check.Commentf("expected hostname 'foo.bar.baz', received %s", out))
}

func (s *DockerSuite) TestRunPrivilegedCanMknod(c *check.C) {
	// Not applicable for Windows as Windows daemon does not support
	// the concept of --privileged, and mknod is a Unix concept.
	testRequires(c, DaemonIsLinux, NotUserNamespace)
	out, _ := dockerCmd(c, "run", "--privileged", "busybox", "sh", "-c", "mknod /tmp/sda b 8 0 && echo ok")
	c.Assert(out, checker.Contains, "ok", check.Commentf("expected output ok received %s", out))
}

func (s *DockerSuite) TestRunUnprivilegedCanMknod(c *check.C) {
	// Not applicable for Windows as Windows daemon does not support
	// the concept of --privileged, and mknod is a Unix concept.
	testRequires(c, DaemonIsLinux, NotUserNamespace)
	out, _ := dockerCmd(c, "run", "busybox", "sh", "-c", "mknod /tmp/sda b 8 0 && echo ok")
	c.Assert(out, checker.Contains, "ok", check.Commentf("expected output ok received %s", out))
}

func (s *DockerSuite) TestRunCapDropInvalid(c *check.C) {
	// Not applicable for Windows as there is no concept of --cap-drop
	testRequires(c, DaemonIsLinux)
	out, _, err := dockerCmdWithError("run", "--cap-drop=CHPASS", "busybox", "ls")
	c.Assert(err, checker.NotNil, check.Commentf(out))
}

func (s *DockerSuite) TestRunCapDropCannotMknod(c *check.C) {
	// Not applicable for Windows as there is no concept of --cap-drop or mknod
	testRequires(c, DaemonIsLinux)
	out, _, err := dockerCmdWithError("run", "--cap-drop=MKNOD", "busybox", "sh", "-c", "mknod /tmp/sda b 8 0 && echo ok")

	c.Assert(err, checker.NotNil, check.Commentf(out))
	c.Assert(out, checker.Not(checker.Contains), "ok", check.Commentf("expected output not ok received %s", out))
}

func (s *DockerSuite) TestRunCapDropCannotMknodLowerCase(c *check.C) {
	// Not applicable for Windows as there is no concept of --cap-drop or mknod
	testRequires(c, DaemonIsLinux)
	out, _, err := dockerCmdWithError("run", "--cap-drop=mknod", "busybox", "sh", "-c", "mknod /tmp/sda b 8 0 && echo ok")

	c.Assert(err, checker.NotNil, check.Commentf(out))
	c.Assert(out, checker.Not(checker.Contains), "ok", check.Commentf("expected output not ok received %s", out))
}

func (s *DockerSuite) TestRunCapDropALLCannotMknod(c *check.C) {
	// Not applicable for Windows as there is no concept of --cap-drop or mknod
	testRequires(c, DaemonIsLinux)
	out, _, err := dockerCmdWithError("run", "--cap-drop=ALL", "--cap-add=SETGID", "busybox", "sh", "-c", "mknod /tmp/sda b 8 0 && echo ok")

	c.Assert(err, checker.NotNil, check.Commentf(out))
	c.Assert(out, checker.Not(checker.Contains), "ok", check.Commentf("expected output not ok received %s", out))
}

func (s *DockerSuite) TestRunCapDropALLAddMknodCanMknod(c *check.C) {
	// Not applicable for Windows as there is no concept of --cap-drop or mknod
	testRequires(c, DaemonIsLinux, NotUserNamespace)
	out, _ := dockerCmd(c, "run", "--cap-drop=ALL", "--cap-add=MKNOD", "--cap-add=SETGID", "busybox", "sh", "-c", "mknod /tmp/sda b 8 0 && echo ok")

	c.Assert(out, checker.Contains, "ok", check.Commentf("expected output ok received %s", out))
}

func (s *DockerSuite) TestRunCapAddInvalid(c *check.C) {
	// Not applicable for Windows as there is no concept of --cap-add
	testRequires(c, DaemonIsLinux)
	out, _, err := dockerCmdWithError("run", "--cap-add=CHPASS", "busybox", "ls")

	c.Assert(err, checker.NotNil, check.Commentf(out))
}

func (s *DockerSuite) TestRunCapAddCanDownInterface(c *check.C) {
	// Not applicable for Windows as there is no concept of --cap-add
	testRequires(c, DaemonIsLinux)
	out, _ := dockerCmd(c, "run", "--cap-add=NET_ADMIN", "busybox", "sh", "-c", "ip link set eth0 down && echo ok")

	c.Assert(out, checker.Contains, "ok", check.Commentf("expected output ok received %s", out))
}

func (s *DockerSuite) TestRunCapAddALLCanDownInterface(c *check.C) {
	// Not applicable for Windows as there is no concept of --cap-add
	testRequires(c, DaemonIsLinux)
	out, _ := dockerCmd(c, "run", "--cap-add=ALL", "busybox", "sh", "-c", "ip link set eth0 down && echo ok")

	c.Assert(out, checker.Contains, "ok", check.Commentf("expected output ok received %s", out))
}

func (s *DockerSuite) TestRunCapAddALLDropNetAdminCanDownInterface(c *check.C) {
	// Not applicable for Windows as there is no concept of --cap-add
	testRequires(c, DaemonIsLinux)
	out, _, err := dockerCmdWithError("run", "--cap-add=ALL", "--cap-drop=NET_ADMIN", "busybox", "sh", "-c", "ip link set eth0 down && echo ok")

	c.Assert(err, checker.NotNil, check.Commentf(out))
	c.Assert(out, checker.Not(checker.Contains), "ok", check.Commentf("expected output not ok received %s", out))
}

func (s *DockerSuite) TestRunGroupAdd(c *check.C) {
	// Not applicable for Windows as there is no concept of --group-add
	testRequires(c, DaemonIsLinux, NativeExecDriver)
	out, _ := dockerCmd(c, "run", "--group-add=audio", "--group-add=staff", "--group-add=777", "busybox", "sh", "-c", "id")

	groupsList := "uid=0(root) gid=0(root) groups=10(wheel),29(audio),50(staff),777"
	c.Assert(out, checker.Contains, groupsList, check.Commentf("expected output %s received %s", groupsList, out))
}

func (s *DockerSuite) TestRunPrivilegedCanMount(c *check.C) {
	// Not applicable for Windows as there is no concept of --privileged
	testRequires(c, DaemonIsLinux, NotUserNamespace)
	out, _ := dockerCmd(c, "run", "--privileged", "busybox", "sh", "-c", "mount -t tmpfs none /tmp && echo ok")

	c.Assert(out, checker.Contains, "ok", check.Commentf("expected output ok received %s", out))
}

func (s *DockerSuite) TestRunUnprivilegedCannotMount(c *check.C) {
	// Not applicable for Windows as there is no concept of unprivileged
	testRequires(c, DaemonIsLinux)
	out, _, err := dockerCmdWithError("run", "busybox", "sh", "-c", "mount -t tmpfs none /tmp && echo ok")

	c.Assert(err, checker.NotNil, check.Commentf(out))
	c.Assert(out, checker.Not(checker.Contains), "ok", check.Commentf("expected output not ok received %s", out))
}

func (s *DockerSuite) TestRunSysNotWritableInNonPrivilegedContainers(c *check.C) {
	// Not applicable for Windows as there is no concept of unprivileged
	testRequires(c, DaemonIsLinux)
	_, _, err := dockerCmdWithError("run", "busybox", "touch", "/sys/kernel/profiling")
	c.Assert(err, checker.NotNil, check.Commentf("sys should not be writable in a non privileged container"))
}

func (s *DockerSuite) TestRunSysWritableInPrivilegedContainers(c *check.C) {
	// Not applicable for Windows as there is no concept of unprivileged
	testRequires(c, DaemonIsLinux, NotUserNamespace)
	dockerCmd(c, "run", "--privileged", "busybox", "touch", "/sys/kernel/profiling")
}

func (s *DockerSuite) TestRunProcNotWritableInNonPrivilegedContainers(c *check.C) {
	// Not applicable for Windows as there is no concept of unprivileged
	testRequires(c, DaemonIsLinux)
	_, _, err := dockerCmdWithError("run", "busybox", "touch", "/proc/sysrq-trigger")
	c.Assert(err, checker.NotNil, check.Commentf("proc should not be writable in a non privileged container"))
}

func (s *DockerSuite) TestRunProcWritableInPrivilegedContainers(c *check.C) {
	// Not applicable for Windows as there is no concept of --privileged
	testRequires(c, DaemonIsLinux, NotUserNamespace)
	dockerCmd(c, "run", "--privileged", "busybox", "touch", "/proc/sysrq-trigger")
}

func (s *DockerSuite) TestRunDeviceNumbers(c *check.C) {
	// Not applicable on Windows as /dev/ is a Unix specific concept
	// TODO: NotUserNamespace could be removed here if "root" "root" is replaced w user
	testRequires(c, DaemonIsLinux, NotUserNamespace)
	out, _ := dockerCmd(c, "run", "busybox", "sh", "-c", "ls -l /dev/null")
	deviceLineFields := strings.Fields(out)
	deviceLineFields[6] = ""
	deviceLineFields[7] = ""
	deviceLineFields[8] = ""
	expected := []string{"crw-rw-rw-", "1", "root", "root", "1,", "3", "", "", "", "/dev/null"}

	c.Assert(deviceLineFields, checker.DeepEquals, expected, check.Commentf("expected output\ncrw-rw-rw- 1 root root 1, 3 May 24 13:29 /dev/null\n received\n %s\n", out))
}

func (s *DockerSuite) TestRunThatCharacterDevicesActLikeCharacterDevices(c *check.C) {
	// Not applicable on Windows as /dev/ is a Unix specific concept
	testRequires(c, DaemonIsLinux)
	out, _ := dockerCmd(c, "run", "busybox", "sh", "-c", "dd if=/dev/zero of=/zero bs=1k count=5 2> /dev/null ; du -h /zero")

	actual := strings.Trim(out, "\r\n")
	c.Assert(actual[0], checker.Equals, '0', check.Commentf("expected a new file called /zero to be create that is greater than 0 bytes long, but du says: %s", actual))
}

func (s *DockerSuite) TestRunUnprivilegedWithChroot(c *check.C) {
	// Not applicable on Windows as it does not support chroot
	testRequires(c, DaemonIsLinux)
	dockerCmd(c, "run", "busybox", "chroot", "/", "true")
}

func (s *DockerSuite) TestRunAddingOptionalDevices(c *check.C) {
	// Not applicable on Windows as Windows does not support --device
	testRequires(c, DaemonIsLinux, NotUserNamespace)
	out, _ := dockerCmd(c, "run", "--device", "/dev/zero:/dev/nulo", "busybox", "sh", "-c", "ls /dev/nulo")
	c.Assert(out, checker.Contains, "/dev/nulo", check.Commentf("expected output /dev/nulo, received %s", out))
}

func (s *DockerSuite) TestRunAddingOptionalDevicesNoSrc(c *check.C) {
	// Not applicable on Windows as Windows does not support --device
	testRequires(c, DaemonIsLinux, NotUserNamespace)
	out, _ := dockerCmd(c, "run", "--device", "/dev/zero:rw", "busybox", "sh", "-c", "ls /dev/zero")
	c.Assert(out, checker.Contains, "/dev/zero", check.Commentf("expected output /dev/zero, received %s", out))
}

func (s *DockerSuite) TestRunAddingOptionalDevicesInvalidMode(c *check.C) {
	// Not applicable on Windows as Windows does not support --device
	testRequires(c, DaemonIsLinux, NotUserNamespace)
	_, _, err := dockerCmdWithError("run", "--device", "/dev/zero:ro", "busybox", "sh", "-c", "ls /dev/zero")
	c.Assert(err, checker.NotNil, check.Commentf("run container with device mode ro should fail"))
}

func (s *DockerSuite) TestRunModeHostname(c *check.C) {
	// Not applicable on Windows as Windows does not support -h
	testRequires(c, SameHostDaemon, DaemonIsLinux, NotUserNamespace)

	out, _ := dockerCmd(c, "run", "-h=testhostname", "busybox", "cat", "/etc/hostname")

	c.Assert(out, checker.Contains, "testhostname", check.Commentf("expected 'testhostname', but says: %q", out))

	out, _ = dockerCmd(c, "run", "--net=host", "busybox", "cat", "/etc/hostname")

	hostname, err := os.Hostname()
	c.Assert(err, checker.IsNil)
	c.Assert(out, checker.Contains, hostname, check.Commentf("expected %q, but says: %q", hostname, out))
}

func (s *DockerSuite) TestRunRootWorkdir(c *check.C) {
	out, _ := dockerCmd(c, "run", "--workdir", "/", "busybox", "pwd")
	expected := "/\n"
	if daemonPlatform == "windows" {
		expected = "C:" + expected
	}
	c.Assert(out, checker.Equals, expected, check.Commentf("pwd returned %q (expected %s)", s, expected))
}

func (s *DockerSuite) TestRunAllowBindMountingRoot(c *check.C) {
	testRequires(c, DaemonIsLinux)
	dockerCmd(c, "run", "-v", "/:/host", "busybox", "ls", "/host")
}

func (s *DockerSuite) TestRunDisallowBindMountingRootToRoot(c *check.C) {
	// Not applicable on Windows as Windows does not support volumes
	testRequires(c, DaemonIsLinux)
	out, _, err := dockerCmdWithError("run", "-v", "/:/", "busybox", "ls", "/host")
	c.Assert(err, checker.NotNil, check.Commentf(out))
}

// Verify that a container gets default DNS when only localhost resolvers exist
func (s *DockerSuite) TestRunDnsDefaultOptions(c *check.C) {
	// Not applicable on Windows as this is testing Unix specific functionality
	testRequires(c, SameHostDaemon, DaemonIsLinux)

	// preserve original resolv.conf for restoring after test
	origResolvConf, err := ioutil.ReadFile("/etc/resolv.conf")
	c.Assert(os.IsNotExist(err), checker.False, check.Commentf("/etc/resolv.conf does not exist"))

	// defer restored original conf
	defer func() {
		if err := ioutil.WriteFile("/etc/resolv.conf", origResolvConf, 0644); err != nil {
			c.Fatal(err)
		}
	}()

	// test 3 cases: standard IPv4 localhost, commented out localhost, and IPv6 localhost
	// 2 are removed from the file at container start, and the 3rd (commented out) one is ignored by
	// GetNameservers(), leading to a replacement of nameservers with the default set
	tmpResolvConf := []byte("nameserver 127.0.0.1\n#nameserver 127.0.2.1\nnameserver ::1")
	err = ioutil.WriteFile("/etc/resolv.conf", tmpResolvConf, 0644)
	c.Assert(err, check.IsNil)

	actual, _ := dockerCmd(c, "run", "busybox", "cat", "/etc/resolv.conf")
	// check that the actual defaults are appended to the commented out
	// localhost resolver (which should be preserved)
	// NOTE: if we ever change the defaults from google dns, this will break
	expected := "#nameserver 127.0.2.1\n\nnameserver 8.8.8.8\nnameserver 8.8.4.4\n"
	c.Assert(actual, checker.Equals, expected, check.Commentf("expected resolv.conf be: %q, but was: %q", expected, actual))
}

func (s *DockerSuite) TestRunDnsOptions(c *check.C) {
	// Not applicable on Windows as Windows does not support --dns*, or
	// the Unix-specific functionality of resolv.conf.
	testRequires(c, DaemonIsLinux)
	out, stderr, _ := dockerCmdWithStdoutStderr(c, "run", "--dns=127.0.0.1", "--dns-search=mydomain", "--dns-opt=ndots:9", "busybox", "cat", "/etc/resolv.conf")

	// The client will get a warning on stderr when setting DNS to a localhost address; verify this:
	c.Assert(stderr, checker.Contains, "Localhost DNS setting", check.Commentf("Expected warning on stderr about localhost resolver, but got %q", stderr))

	actual := strings.Replace(strings.Trim(out, "\r\n"), "\n", " ", -1)

	c.Assert(actual, checker.Equals, "search mydomain nameserver 127.0.0.1 options ndots:9", check.Commentf("expected 'search mydomain nameserver 127.0.0.1 options ndots:9', but says: %q", actual))

	out, stderr, _ = dockerCmdWithStdoutStderr(c, "run", "--dns=127.0.0.1", "--dns-search=.", "--dns-opt=ndots:3", "busybox", "cat", "/etc/resolv.conf")

	actual = strings.Replace(strings.Trim(strings.Trim(out, "\r\n"), " "), "\n", " ", -1)
	c.Assert(actual, checker.Equals, "nameserver 127.0.0.1 options ndots:3", check.Commentf("expected 'nameserver 127.0.0.1 options ndots:3', but says: %q", actual))
}

func (s *DockerSuite) TestRunDnsRepeatOptions(c *check.C) {
	testRequires(c, DaemonIsLinux)
	out, _ := dockerCmd(c, "run", "--dns=1.1.1.1", "--dns=2.2.2.2", "--dns-search=mydomain", "--dns-search=mydomain2", "--dns-opt=ndots:9", "--dns-opt=timeout:3", "busybox", "cat", "/etc/resolv.conf")

	actual := strings.Replace(strings.Trim(out, "\r\n"), "\n", " ", -1)
	c.Assert(actual, checker.Equals, "search mydomain mydomain2 nameserver 1.1.1.1 nameserver 2.2.2.2 options ndots:9 timeout:3", check.Commentf("expected 'search mydomain mydomain2 nameserver 1.1.1.1 nameserver 2.2.2.2 options ndots:9 timeout:3', but says: %q", actual))
}

func (s *DockerSuite) TestRunDnsOptionsBasedOnHostResolvConf(c *check.C) {
	// Not applicable on Windows as testing Unix specific functionality
	testRequires(c, SameHostDaemon, DaemonIsLinux)

	origResolvConf, err := ioutil.ReadFile("/etc/resolv.conf")
	c.Assert(os.IsNotExist(err), checker.False, check.Commentf("/etc/resolv.conf does not exist"))

	hostNamservers := resolvconf.GetNameservers(origResolvConf)
	hostSearch := resolvconf.GetSearchDomains(origResolvConf)

	var out string
	out, _ = dockerCmd(c, "run", "--dns=127.0.0.1", "busybox", "cat", "/etc/resolv.conf")

	actualNameservers := resolvconf.GetNameservers([]byte(out))
	c.Assert(string(actualNameservers[0]), checker.Equals, "127.0.0.1", check.Commentf("expected '127.0.0.1', but says: %q", string(actualNameservers[0])))

	actualSearch := resolvconf.GetSearchDomains([]byte(out))
	c.Assert(len(actualSearch), checker.Equals, len(hostSearch), check.Commentf("expected %q search domain(s), but it has: %q", len(hostSearch), len(actualSearch)))

	c.Assert(actualSearch, checker.DeepEquals, hostSearch)
	// FIXME validate this
	// for i := range actualSearch {
	// 	if actualSearch[i] != hostSearch[i] {
	// 		c.Fatalf("expected %q domain, but says: %q", actualSearch[i], hostSearch[i])
	// 	}
	// }

	out, _ = dockerCmd(c, "run", "--dns-search=mydomain", "busybox", "cat", "/etc/resolv.conf")

	actualNameservers = resolvconf.GetNameservers([]byte(out))
	c.Assert(len(actualNameservers), checker.Equals, len(hostNamservers), check.Commentf("expected %q nameserver(s), but it has: %q", len(hostNamservers), len(actualNameservers)))

	c.Assert(actualSearch, checker.DeepEquals, hostSearch)
	// FIXME validate this
	// for i := range actualNameservers {
	// 	if actualNameservers[i] != hostNamservers[i] {
	// 		c.Fatalf("expected %q nameserver, but says: %q", actualNameservers[i], hostNamservers[i])
	// 	}
	// }

	actualSearch = resolvconf.GetSearchDomains([]byte(out))
	c.Assert(string(actualSearch[0]), checker.Equals, "mydomain", check.Commentf("expected 'mydomain', but says: %q", string(actualSearch[0])))

	// test with file
	tmpResolvConf := []byte("search example.com\nnameserver 12.34.56.78\nnameserver 127.0.0.1")
	err = ioutil.WriteFile("/etc/resolv.conf", tmpResolvConf, 0644)
	c.Assert(err, checker.IsNil)
	// put the old resolvconf back
	defer func() {
		err := ioutil.WriteFile("/etc/resolv.conf", origResolvConf, 0644)
		c.Assert(err, checker.IsNil)
	}()

	resolvConf, err := ioutil.ReadFile("/etc/resolv.conf")
	c.Assert(os.IsNotExist(err), checker.False, check.Commentf("/etc/resolv.conf does not exist"))

	hostNamservers = resolvconf.GetNameservers(resolvConf)
	hostSearch = resolvconf.GetSearchDomains(resolvConf)

	out, _ = dockerCmd(c, "run", "busybox", "cat", "/etc/resolv.conf")
	actualNameservers = resolvconf.GetNameservers([]byte(out))
	c.Assert(actualNameservers, checker.HasLen, 1)
	c.Assert(string(actualNameservers[0]), checker.Equals, "12.34.56.78", check.Commentf("expected '12.34.56.78', but has: %v", actualNameservers))

	actualSearch = resolvconf.GetSearchDomains([]byte(out))
	c.Assert(len(actualSearch), checker.Equals, len(hostSearch), check.Commentf("expected %q search domain(s), but it has: %q", len(hostSearch), len(actualSearch)))

	c.Assert(actualSearch, checker.DeepEquals, hostSearch)
	// FIXME validate this
	// for i := range actualSearch {
	// 	if actualSearch[i] != hostSearch[i] {
	// 		c.Fatalf("expected %q domain, but says: %q", actualSearch[i], hostSearch[i])
	// 	}
	// }
}

// Test to see if a non-root user can resolve a DNS name. Also
// check if the container resolv.conf file has at least 0644 perm.
func (s *DockerSuite) TestRunNonRootUserResolvName(c *check.C) {
	// Not applicable on Windows as Windows does not support --user
	testRequires(c, SameHostDaemon, Network, DaemonIsLinux)

	dockerCmd(c, "run", "--name=testperm", "--user=nobody", "busybox", "nslookup", "apt.dockerproject.org")

	cID, err := getIDByName("testperm")
	c.Assert(err, checker.IsNil)

	fmode := (os.FileMode)(0644)
	finfo, err := os.Stat(containerStorageFile(cID, "resolv.conf"))
	c.Assert(err, checker.IsNil)

	c.Assert(finfo.Mode()&fmode, checker.Equals, fmode, check.Commentf("Expected container resolv.conf mode to be at least %s, instead got %s", fmode.String(), finfo.Mode().String()))
}

// Test if container resolv.conf gets updated the next time it restarts
// if host /etc/resolv.conf has changed. This only applies if the container
// uses the host's /etc/resolv.conf and does not have any dns options provided.
func (s *DockerSuite) TestRunResolvconfUpdate(c *check.C) {
	// Not applicable on Windows as testing unix specific functionality
	testRequires(c, SameHostDaemon, DaemonIsLinux, NativeExecDriver)

	tmpResolvConf := []byte("search pommesfrites.fr\nnameserver 12.34.56.78\n")
	tmpLocalhostResolvConf := []byte("nameserver 127.0.0.1")

	//take a copy of resolv.conf for restoring after test completes
	resolvConfSystem, err := ioutil.ReadFile("/etc/resolv.conf")
	c.Assert(err, checker.IsNil)

	// This test case is meant to test monitoring resolv.conf when it is
	// a regular file not a bind mounc. So we unmount resolv.conf and replace
	// it with a file containing the original settings.
	cmd := exec.Command("umount", "/etc/resolv.conf")
	_, err = runCommand(cmd)
	c.Assert(err, checker.IsNil)

	//cleanup
	defer func() {
		err := ioutil.WriteFile("/etc/resolv.conf", resolvConfSystem, 0644)
		c.Assert(err, checker.IsNil)
	}()

	//1. test that a restarting container gets an updated resolv.conf
	dockerCmd(c, "run", "--name='first'", "busybox", "true")
	containerID1, err := getIDByName("first")
	c.Assert(err, checker.IsNil)

	// replace resolv.conf with our temporary copy
	bytesResolvConf := []byte(tmpResolvConf)
	err = ioutil.WriteFile("/etc/resolv.conf", bytesResolvConf, 0644)
	c.Assert(err, checker.IsNil)

	// start the container again to pickup changes
	dockerCmd(c, "start", "first")

	// check for update in container
	containerResolv, err := readContainerFile(containerID1, "resolv.conf")
	c.Assert(err, checker.IsNil)
	c.Assert(containerResolv, checker.Equals, bytesResolvConf, check.Commentf("Restarted container does not have updated resolv.conf; expected %q, got %q", tmpResolvConf, string(containerResolv)))

	/*	//make a change to resolv.conf (in this case replacing our tmp copy with orig copy)
		if err := ioutil.WriteFile("/etc/resolv.conf", resolvConfSystem, 0644); err != nil {
						c.Fatal(err)
								} */
	//2. test that a restarting container does not receive resolv.conf updates
	//   if it modified the container copy of the starting point resolv.conf
	dockerCmd(c, "run", "--name='second'", "busybox", "sh", "-c", "echo 'search mylittlepony.com' >>/etc/resolv.conf")
	containerID2, err := getIDByName("second")
	c.Assert(err, checker.IsNil)

	//make a change to resolv.conf (in this case replacing our tmp copy with orig copy)
	err = ioutil.WriteFile("/etc/resolv.conf", resolvConfSystem, 0644)
	c.Assert(err, checker.IsNil)

	// start the container again
	dockerCmd(c, "start", "second")

	// check for update in container
	containerResolv, err = readContainerFile(containerID2, "resolv.conf")
	c.Assert(err, checker.IsNil)

	c.Assert(containerResolv, checker.Equals, resolvConfSystem, check.Commentf("Restarting  a container after container updated resolv.conf should not pick up host changes; expected %q, got %q", string(containerResolv), string(resolvConfSystem)))

	//3. test that a running container's resolv.conf is not modified while running
	out, _ := dockerCmd(c, "run", "-d", "busybox", "top")
	runningContainerID := strings.TrimSpace(out)

	// replace resolv.conf
	err = ioutil.WriteFile("/etc/resolv.conf", bytesResolvConf, 0644)
	c.Assert(err, checker.IsNil)

	// check for update in container
	containerResolv, err = readContainerFile(runningContainerID, "resolv.conf")
	c.Assert(err, checker.IsNil)

	c.Assert(containerResolv, checker.Equals, bytesResolvConf, check.Commentf("Running container should not have updated resolv.conf; expected %q, got %q", string(resolvConfSystem), string(containerResolv)))

	//4. test that a running container's resolv.conf is updated upon restart
	//   (the above container is still running..)
	dockerCmd(c, "restart", runningContainerID)

	// check for update in container
	containerResolv, err = readContainerFile(runningContainerID, "resolv.conf")
	c.Assert(err, checker.IsNil)
	c.Assert(containerResolv, checker.Equals, bytesResolvConf, check.Commentf("Restarted container should not have updated resolv.conf; expected %q, got %q", string(resolvConfSystem), string(containerResolv)))

	//5. test that additions of a localhost resolver are cleaned from
	//   host resolv.conf before updating container's resolv.conf copies

	// replace resolv.conf with a localhost-only nameserver copy
	bytesResolvConf = []byte(tmpLocalhostResolvConf)
	err = ioutil.WriteFile("/etc/resolv.conf", bytesResolvConf, 0644)
	c.Assert(err, checker.IsNil)

	// start the container again to pickup changes
	dockerCmd(c, "start", "first")

	// our first exited container ID should have been updated, but with default DNS
	// after the cleanup of resolv.conf found only a localhost nameserver:
	containerResolv, err = readContainerFile(containerID1, "resolv.conf")
	c.Assert(err, checker.IsNil)

	expected := "\nnameserver 8.8.8.8\nnameserver 8.8.4.4\n"
	c.Assert(containerResolv, checker.Equals, []byte(expected), check.Commentf("Container does not have cleaned/replaced DNS in resolv.conf; expected %q, got %q", expected, string(containerResolv)))

	//6. Test that replacing (as opposed to modifying) resolv.conf triggers an update
	//   of containers' resolv.conf.

	// Restore the original resolv.conf
	err = ioutil.WriteFile("/etc/resolv.conf", resolvConfSystem, 0644)
	c.Assert(err, checker.IsNil)

	// Run the container so it picks up the old settings
	dockerCmd(c, "run", "--name='third'", "busybox", "true")
	containerID3, err := getIDByName("third")
	c.Assert(err, checker.IsNil)

	// Create a modified resolv.conf.aside and override resolv.conf with it
	bytesResolvConf = []byte(tmpResolvConf)
	err = ioutil.WriteFile("/etc/resolv.conf.aside", bytesResolvConf, 0644)
	c.Assert(err, checker.IsNil)

	err = os.Rename("/etc/resolv.conf.aside", "/etc/resolv.conf")
	c.Assert(err, checker.IsNil)

	// start the container again to pickup changes
	dockerCmd(c, "start", "third")

	// check for update in container
	containerResolv, err = readContainerFile(containerID3, "resolv.conf")
	c.Assert(err, checker.IsNil)
	c.Assert(containerResolv, checker.Equals, bytesResolvConf, check.Commentf("Stopped container does not have updated resolv.conf; expected\n%q\n got\n%q", tmpResolvConf, string(containerResolv)))

	//cleanup, restore original resolv.conf happens in defer func()
}

func (s *DockerSuite) TestRunAddHost(c *check.C) {
	// Not applicable on Windows as it does not support --add-host
	testRequires(c, DaemonIsLinux)
	out, _ := dockerCmd(c, "run", "--add-host=extra:86.75.30.9", "busybox", "grep", "extra", "/etc/hosts")

	c.Assert(out, checker.Contains, "86.75.30.9\textra", check.Commentf("expected '86.75.30.9\textra', but says: %q", out))
}

// Regression test for #6983
func (s *DockerSuite) TestRunAttachStdErrOnlyTTYMode(c *check.C) {
	dockerCmd(c, "run", "-t", "-a", "stderr", "busybox", "true")
}

// Regression test for #6983
func (s *DockerSuite) TestRunAttachStdOutOnlyTTYMode(c *check.C) {
	dockerCmd(c, "run", "-t", "-a", "stdout", "busybox", "true")
}

// Regression test for #6983
func (s *DockerSuite) TestRunAttachStdOutAndErrTTYMode(c *check.C) {
	dockerCmd(c, "run", "-t", "-a", "stdout", "-a", "stderr", "busybox", "true")
}

// Test for #10388 - this will run the same test as TestRunAttachStdOutAndErrTTYMode
// but using --attach instead of -a to make sure we read the flag correctly
func (s *DockerSuite) TestRunAttachWithDetach(c *check.C) {
	cmd := exec.Command(dockerBinary, "run", "-d", "--attach", "stdout", "busybox", "true")
	_, stderr, _, err := runCommandWithStdoutStderr(cmd)
	c.Assert(err, checker.NotNil, check.Commentf("Container should have exited with error code different than 0"))
	c.Assert(stderr, checker.Contains, "Conflicting options: -a and -d", check.Commentf("Should have been returned an error with conflicting options -a and -d"))
}

func (s *DockerSuite) TestRunState(c *check.C) {
	// TODO Windows: This needs some rework as Windows busybox does not support top
	testRequires(c, DaemonIsLinux)
	out, _ := dockerCmd(c, "run", "-d", "busybox", "top")

	id := strings.TrimSpace(out)
	state, err := inspectField(id, "State.Running")
	c.Assert(err, checker.IsNil)
	c.Assert(state, checker.Equals, "true", check.Commentf("Container state is 'not running'"))

	pid1, err := inspectField(id, "State.Pid")
	c.Assert(err, checker.IsNil)
	c.Assert(pid1, checker.Not(checker.Equals), "0", check.Commentf("Container state Pid 0"))

	dockerCmd(c, "stop", id)
	state, err = inspectField(id, "State.Running")
	c.Assert(err, checker.IsNil)
	c.Assert(state, checker.Equals, "false", check.Commentf("Container state is 'running'"))

	pid2, err := inspectField(id, "State.Pid")
	c.Assert(err, checker.IsNil)
	c.Assert(pid2, checker.Not(checker.Equals), pid1, check.Commentf("Container state Pid %s, but expected %s", pid2, pid1))

	dockerCmd(c, "start", id)
	state, err = inspectField(id, "State.Running")
	c.Assert(err, checker.IsNil)
	c.Assert(state, checker.Equals, "true", check.Commentf("Container state is 'not running'"))

	pid3, err := inspectField(id, "State.Pid")
	c.Assert(err, check.IsNil)
	c.Assert(pid3, checker.Not(checker.Equals), pid1, check.Commentf("Container state Pid %s, but expected %s", pid2, pid1))
}

// Test for #1737
func (s *DockerSuite) TestRunCopyVolumeUidGid(c *check.C) {
	// Not applicable on Windows as it does not support volumes, uid or gid
	testRequires(c, DaemonIsLinux)
	name := "testrunvolumesuidgid"
	_, err := buildImage(name,
		`FROM busybox
		RUN echo 'dockerio:x:1001:1001::/bin:/bin/false' >> /etc/passwd
		RUN echo 'dockerio:x:1001:' >> /etc/group
		RUN mkdir -p /hello && touch /hello/test && chown dockerio.dockerio /hello`,
		true)
	c.Assert(err, checker.IsNil)

	// Test that the uid and gid is copied from the image to the volume
	out, _ := dockerCmd(c, "run", "--rm", "-v", "/hello", name, "sh", "-c", "ls -l / | grep hello | awk '{print $3\":\"$4}'")
	out = strings.TrimSpace(out)
	c.Assert(out, checker.Equals, "dockerio:dockerio", check.Commentf("Wrong /hello ownership: %s, expected dockerio:dockerio", out))
}

// Test for #1582
func (s *DockerSuite) TestRunCopyVolumeContent(c *check.C) {
	// Not applicable on Windows as it does not support volumes
	testRequires(c, DaemonIsLinux)
	name := "testruncopyvolumecontent"
	_, err := buildImage(name,
		`FROM busybox
		RUN mkdir -p /hello/local && echo hello > /hello/local/world`,
		true)
	c.Assert(err, checker.IsNil)

	// Test that the content is copied from the image to the volume
	out, _ := dockerCmd(c, "run", "--rm", "-v", "/hello", name, "find", "/hello")
	c.Assert(out, checker.Contains, "/hello/local/world", check.Commentf("Container failed to transfer content to volume"))
	c.Assert(out, checker.Not(checker.Contains), "/hello/local", check.Commentf("Container failed to transfer content to volume"))
}

func (s *DockerSuite) TestRunCleanupCmdOnEntrypoint(c *check.C) {
	name := "testrunmdcleanuponentrypoint"
	_, err := buildImage(name,
		`FROM busybox
		ENTRYPOINT ["echo"]
		CMD ["testingpoint"]`,
		true)
	c.Assert(err, checker.IsNil)

	out, _ := dockerCmd(c, "run", "--entrypoint", "whoami", name)
	out = strings.TrimSpace(out)
	expected := "root"
	if daemonPlatform == "windows" {
		expected = `nt authority\system`
	}
	c.Assert(out, checker.Equals, expected)
}

// TestRunWorkdirExistsAndIsFile checks that if 'docker run -w' with existing file can be detected
func (s *DockerSuite) TestRunWorkdirExistsAndIsFile(c *check.C) {
	existingFile := "/bin/cat"
	expected := "Cannot mkdir: /bin/cat is not a directory"
	if daemonPlatform == "windows" {
		existingFile = `\windows\system32\ntdll.dll`
		expected = "The directory name is invalid"
	}

	out, exit, err := dockerCmdWithError("run", "-w", existingFile, "busybox")
	c.Assert(err, checker.NotNil, check.Commentf("Docker must complains about making dir, but we got out: %s, exit: %d, err: %s", out, exit, err))
	c.Assert(exit, checker.Equals, 1, check.Commentf("Docker must complains about making dir, but we got out: %s, exit: %d, err: %s", out, exit, err))
	c.Assert(out, checker.Contains, expected, check.Commentf("Docker must complains about making dir, but we got out: %s, exit: %d, err: %s", out, exit, err))
}

func (s *DockerSuite) TestRunExitOnStdinClose(c *check.C) {
	name := "testrunexitonstdinclose"

	meow := "/bin/cat"
	delay := 1
	if daemonPlatform == "windows" {
		meow = "cat"
		delay = 5
	}
	runCmd := exec.Command(dockerBinary, "run", "--name", name, "-i", "busybox", meow)

	stdin, err := runCmd.StdinPipe()
	c.Assert(err, checker.IsNil)

	stdout, err := runCmd.StdoutPipe()
	c.Assert(err, checker.IsNil)

	err = runCmd.Start()
	c.Assert(err, checker.IsNil)

	_, err = stdin.Write([]byte("hello\n"))
	c.Assert(err, checker.IsNil)

	r := bufio.NewReader(stdout)
	line, err := r.ReadString('\n')
	c.Assert(err, checker.IsNil)
	line = strings.TrimSpace(line)
	c.Assert(line, checker.Equals, "hello")

	err = stdin.Close()
	c.Assert(err, checker.IsNil)

	finish := make(chan error)
	go func() {
		finish <- runCmd.Wait()
		close(finish)
	}()

	select {
	case err := <-finish:
		c.Assert(err, check.IsNil)
	case <-time.After(time.Duration(delay) * time.Second):
		c.Fatal("docker run failed to exit on stdin close")
	}

	state, err := inspectField(name, "State.Running")
	c.Assert(err, check.IsNil)

	c.Assert(state, checker.Equals, "false", check.Commentf("Container must be stopped after stdin closing"))
}

// Test for #2267
func (s *DockerSuite) TestRunWriteHostsFileAndNotCommit(c *check.C) {
	// Cannot run on Windows as Windows does not support diff.
	testRequires(c, DaemonIsLinux)
	name := "writehosts"
	out, _ := dockerCmd(c, "run", "--name", name, "busybox", "sh", "-c", "echo test2267 >> /etc/hosts && cat /etc/hosts")
	c.Assert(out, checker.Contains, "test2267", check.Commentf("/etc/hosts should contain 'test2267'"))

	out, _ = dockerCmd(c, "diff", name)

	// FIXME update this
	if len(strings.Trim(out, "\r\n")) != 0 && !eqToBaseDiff(out, c) {
		c.Fatal("diff should be empty")
	}
}

func eqToBaseDiff(out string, c *check.C) bool {
	out1, _ := dockerCmd(c, "run", "-d", "busybox", "echo", "hello")
	cID := strings.TrimSpace(out1)

	baseDiff, _ := dockerCmd(c, "diff", cID)
	baseArr := strings.Split(baseDiff, "\n")
	sort.Strings(baseArr)
	outArr := strings.Split(out, "\n")
	sort.Strings(outArr)
	return sliceEq(baseArr, outArr)
}

func sliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

// Test for #2267
func (s *DockerSuite) TestRunWriteHostnameFileAndNotCommit(c *check.C) {
	// Cannot run on Windows as Windows does not support diff.
	testRequires(c, DaemonIsLinux)
	name := "writehostname"
	out, _ := dockerCmd(c, "run", "--name", name, "busybox", "sh", "-c", "echo test2267 >> /etc/hostname && cat /etc/hostname")
	c.Assert(out, checker.Contains, "test2267", check.Commentf("/etc/hostname should contain 'test2267'"))

	out, _ = dockerCmd(c, "diff", name)
	// FIXME update this
	if len(strings.Trim(out, "\r\n")) != 0 && !eqToBaseDiff(out, c) {
		c.Fatal("diff should be empty")
	}
}

// Test for #2267
func (s *DockerSuite) TestRunWriteResolvFileAndNotCommit(c *check.C) {
	// Cannot run on Windows as Windows does not support diff.
	testRequires(c, DaemonIsLinux)
	name := "writeresolv"
	out, _ := dockerCmd(c, "run", "--name", name, "busybox", "sh", "-c", "echo test2267 >> /etc/resolv.conf && cat /etc/resolv.conf")
	c.Assert(out, checker.Contains, "test2267", check.Commentf("/etc/hostname should contain 'test2267'"))

	out, _ = dockerCmd(c, "diff", name)
	// FIXME update this
	if len(strings.Trim(out, "\r\n")) != 0 && !eqToBaseDiff(out, c) {
		c.Fatal("diff should be empty")
	}
}

func (s *DockerSuite) TestRunWithBadDevice(c *check.C) {
	// Cannot run on Windows as Windows does not support --device
	testRequires(c, DaemonIsLinux)
	name := "baddevice"
	out, _, err := dockerCmdWithError("run", "--name", name, "--device", "/etc", "busybox", "true")

	c.Assert(err, checker.NotNil, check.Commentf("Run should fail with bad device"))

	expected := `"/etc": not a device node`
	c.Assert(out, checker.Contains, expected, check.Commentf("Output should contain %q, actual out: %q", expected, out))
}

func (s *DockerSuite) TestRunEntrypoint(c *check.C) {
	name := "entrypoint"

	// Note Windows does not have an echo.exe built in.
	var out, expected string
	if daemonPlatform == "windows" {
		out, _ = dockerCmd(c, "run", "--name", name, "--entrypoint", "cmd /s /c echo", "busybox", "foobar")
		expected = "foobar\r\n"
	} else {
		out, _ = dockerCmd(c, "run", "--name", name, "--entrypoint", "/bin/echo", "busybox", "-n", "foobar")
		expected = "foobar"
	}

	c.Assert(out, checker.Equals, expected, check.Commentf("Output should be %q, actual out: %q", expected, out))
}

func (s *DockerSuite) TestRunBindMounts(c *check.C) {
	// /tmp gets permission denied
	testRequires(c, NotUserNamespace)
	// Cannot run on Windows as Windows does not support volumes
	testRequires(c, DaemonIsLinux, SameHostDaemon)

	tmpDir, err := ioutil.TempDir("", "docker-test-container")
	c.Assert(err, checker.IsNil)

	defer os.RemoveAll(tmpDir)
	writeFile(path.Join(tmpDir, "touch-me"), "", c)

	// Test reading from a read-only bind mount
	out, _ := dockerCmd(c, "run", "-v", fmt.Sprintf("%s:/tmp:ro", tmpDir), "busybox", "ls", "/tmp")
	c.Assert(out, checker.Equals, "touch-me\n", check.Commentf("Container failed to read from bind mount"))

	// test writing to bind mount
	dockerCmd(c, "run", "-v", fmt.Sprintf("%s:/tmp:rw", tmpDir), "busybox", "touch", "/tmp/holla")

	readFile(path.Join(tmpDir, "holla"), c) // Will fail if the file doesn't exist

	// test mounting to an illegal destination directory
	_, _, err = dockerCmdWithError("run", "-v", fmt.Sprintf("%s:.", tmpDir), "busybox", "ls", ".")
	c.Assert(err, checker.NotNil, check.Commentf("Container bind mounted illegal directory"))

	// test mount a file
	dockerCmd(c, "run", "-v", fmt.Sprintf("%s/holla:/tmp/holla:rw", tmpDir), "busybox", "sh", "-c", "echo -n 'yotta' > /tmp/holla")
	content := readFile(path.Join(tmpDir, "holla"), c) // Will fail if the file doesn't exist
	expected := "yotta"
	c.Assert(content, checker.Equals, expected, check.Commentf("Output should be %q, actual out: %q", expected, content))
}

// Ensure that CIDFile gets deleted if it's empty
// Perform this test by making `docker run` fail
func (s *DockerSuite) TestRunCidFileCleanupIfEmpty(c *check.C) {
	tmpDir, err := ioutil.TempDir("", "TestRunCidFile")
	c.Assert(err, checker.IsNil)
	defer os.RemoveAll(tmpDir)
	tmpCidFile := path.Join(tmpDir, "cid")

	image := "emptyfs"
	if daemonPlatform == "windows" {
		// Windows can't support an emptyfs image. Just use the regular Windows image
		image = WindowsBaseImage
	}
	out, _, err := dockerCmdWithError("run", "--cidfile", tmpCidFile, image)
	c.Assert(err, checker.NotNil, check.Commentf("Run without command must fail. out=%s", out))
	c.Assert(out, checker.Contains, "No command specified", check.Commentf("Run without command failed with wrong output. out=%s\nerr=%v", out, err))

	_, err = os.Stat(tmpCidFile)
	c.Assert(err, checker.NotNil, check.Commentf("empty CIDFile %q should've been deleted", tmpCidFile))
}

// #2098 - Docker cidFiles only contain short version of the containerId
//sudo docker run --cidfile /tmp/docker_tesc.cid ubuntu echo "test"
// TestRunCidFile tests that run --cidfile returns the longid
func (s *DockerSuite) TestRunCidFileCheckIDLength(c *check.C) {
	tmpDir, err := ioutil.TempDir("", "TestRunCidFile")
	c.Assert(err, checker.IsNil)
	tmpCidFile := path.Join(tmpDir, "cid")
	defer os.RemoveAll(tmpDir)

	out, _ := dockerCmd(c, "run", "-d", "--cidfile", tmpCidFile, "busybox", "true")

	id := strings.TrimSpace(out)
	buffer, err := ioutil.ReadFile(tmpCidFile)
	c.Assert(err, checker.IsNil)
	cid := string(buffer)
	c.Assert(cid, checker.HasLen, 64, check.Commentf("--cidfile should be a long id, not %q", id))
	c.Assert(cid, checker.Equals, id)
}

func (s *DockerSuite) TestRunSetMacAddress(c *check.C) {
	// TODO Windows. Test modified to be theoretically Windows compatible,
	// but the version of busybox being used on Windows doesn't handle
	// sh -c ipconfig -all (or /all). It ignores the *all bit. This could
	// be a bug in busybox.
	testRequires(c, DaemonIsLinux)
	mac := "12:34:56:78:9a:bc"

	var out string
	if daemonPlatform == "windows" {
		out, _ = dockerCmd(c, "run", "-i", "--rm", fmt.Sprintf("--mac-address=%s", mac), "busybox", "sh", "-c", "ipconfig /all | grep 'Physical Address' | awk '{print $12}'")
	} else {
		out, _ = dockerCmd(c, "run", "-i", "--rm", fmt.Sprintf("--mac-address=%s", mac), "busybox", "/bin/sh", "-c", "ip link show eth0 | tail -1 | awk '{print $2}'")
	}

	actualMac := strings.TrimSpace(out)
	c.Assert(actualMac, checker.Equals, mac, check.Commentf("Set MAC address with --mac-address failed. The container has an incorrect MAC address: %q, expected: %q", actualMac, mac))
}

func (s *DockerSuite) TestRunInspectMacAddress(c *check.C) {
	// TODO Windows. Network settings are not propagated back to inspect.
	testRequires(c, DaemonIsLinux)
	mac := "12:34:56:78:9a:bc"
	out, _ := dockerCmd(c, "run", "-d", "--mac-address="+mac, "busybox", "top")

	id := strings.TrimSpace(out)
	inspectedMac, err := inspectField(id, "NetworkSettings.MacAddress")
	c.Assert(err, check.IsNil)
	c.Assert(inspectedMac, checker.Equals, mac, check.Commentf("docker inspect outputs wrong MAC address: %q, should be: %q", inspectedMac, mac))
}

// test docker run use a invalid mac address
func (s *DockerSuite) TestRunWithInvalidMacAddress(c *check.C) {
	out, _, err := dockerCmdWithError("run", "--mac-address", "92:d0:c6:0a:29", "busybox")
	//use a invalid mac address should with a error out
	c.Assert(err, checker.NotNil, check.Commentf("run with an invalid --mac-address should with error out"))
	c.Assert(out, checker.Contains, "is not a valid mac address", check.Commentf("run with an invalid --mac-address should with error out"))
}

func (s *DockerSuite) TestRunDeallocatePortOnMissingIptablesRule(c *check.C) {
	// TODO Windows. Network settings are not propagated back to inspect.
	testRequires(c, SameHostDaemon, DaemonIsLinux)

	out, _ := dockerCmd(c, "run", "-d", "-p", "23:23", "busybox", "top")

	id := strings.TrimSpace(out)
	ip, err := inspectField(id, "NetworkSettings.IPAddress")
	c.Assert(err, checker.IsNil)
	iptCmd := exec.Command("iptables", "-D", "DOCKER", "-d", fmt.Sprintf("%s/32", ip),
		"!", "-i", "docker0", "-o", "docker0", "-p", "tcp", "-m", "tcp", "--dport", "23", "-j", "ACCEPT")
	out, _, err = runCommandWithOutput(iptCmd)
	c.Assert(err, checker.IsNil, check.Commentf(out))
	c.Assert(deleteContainer(id), checker.IsNil)

	dockerCmd(c, "run", "-d", "-p", "23:23", "busybox", "top")
}

func (s *DockerSuite) TestRunPortInUse(c *check.C) {
	// TODO Windows. The duplicate NAT message returned by Windows will be
	// changing as is currently completely undecipherable. Does need modifying
	// to run sh rather than top though as top isn't in Windows busybox.
	testRequires(c, SameHostDaemon, DaemonIsLinux)

	port := "1234"
	dockerCmd(c, "run", "-d", "-p", port+":80", "busybox", "top")

	out, _, err := dockerCmdWithError("run", "-d", "-p", port+":80", "busybox", "top")
	c.Assert(err, checker.NotNil, check.Commentf("Binding on used port must fail"))
	c.Assert(out, checker.Contains, "port is already allocated", check.Commentf("Out must be about \"port is already allocated\""))
}

// https://github.com/docker/docker/issues/12148
func (s *DockerSuite) TestRunAllocatePortInReservedRange(c *check.C) {
	// TODO Windows. -P is not yet supported
	testRequires(c, DaemonIsLinux)
	// allocate a dynamic port to get the most recent
	out, _ := dockerCmd(c, "run", "-d", "-P", "-p", "80", "busybox", "top")

	id := strings.TrimSpace(out)
	out, _ = dockerCmd(c, "port", id, "80")

	strPort := strings.Split(strings.TrimSpace(out), ":")[1]
	port, err := strconv.ParseInt(strPort, 10, 64)
	c.Assert(err, checker.IsNil, check.Commentf("invalid port, got: %s, error: %s", strPort, err))

	// allocate a static port and a dynamic port together, with static port
	// takes the next recent port in dynamic port range.
	dockerCmd(c, "run", "-d", "-P", "-p", "80", "-p", fmt.Sprintf("%d:8080", port+1), "busybox", "top")
}

// Regression test for #7792
func (s *DockerSuite) TestRunMountOrdering(c *check.C) {
	// tmp gets permission denied
	testRequires(c, NotUserNamespace)
	// Not applicable on Windows as Windows does not support volumes
	testRequires(c, SameHostDaemon, DaemonIsLinux)

	tmpDir, err := ioutil.TempDir("", "docker_nested_mount_test")
	c.Assert(err, checker.IsNil)
	defer os.RemoveAll(tmpDir)

	tmpDir2, err := ioutil.TempDir("", "docker_nested_mount_test2")
	c.Assert(err, checker.IsNil)
	defer os.RemoveAll(tmpDir2)

	// Create a temporary tmpfs mounc.
	fooDir := filepath.Join(tmpDir, "foo")
	c.Assert(os.MkdirAll(filepath.Join(tmpDir, "foo"), 0755), checker.IsNil)
	c.Assert(ioutil.WriteFile(fmt.Sprintf("%s/touch-me", fooDir), []byte{}, 0644), checker.IsNil)
	c.Assert(ioutil.WriteFile(fmt.Sprintf("%s/touch-me", tmpDir), []byte{}, 0644), checker.IsNil)
	c.Assert(ioutil.WriteFile(fmt.Sprintf("%s/touch-me", tmpDir2), []byte{}, 0644), checker.IsNil)

	dockerCmd(c, "run",
		"-v", fmt.Sprintf("%s:/tmp", tmpDir),
		"-v", fmt.Sprintf("%s:/tmp/foo", fooDir),
		"-v", fmt.Sprintf("%s:/tmp/tmp2", tmpDir2),
		"-v", fmt.Sprintf("%s:/tmp/tmp2/foo", fooDir),
		"busybox:latest", "sh", "-c",
		"ls /tmp/touch-me && ls /tmp/foo/touch-me && ls /tmp/tmp2/touch-me && ls /tmp/tmp2/foo/touch-me")
}

// Regression test for https://github.com/docker/docker/issues/8259
func (s *DockerSuite) TestRunReuseBindVolumeThatIsSymlink(c *check.C) {
	// /tmp gets permission denied
	testRequires(c, NotUserNamespace)
	// Not applicable on Windows as Windows does not support volumes
	testRequires(c, SameHostDaemon, DaemonIsLinux)

	tmpDir, err := ioutil.TempDir(os.TempDir(), "testlink")
	c.Assert(err, checker.IsNil)
	defer os.RemoveAll(tmpDir)

	linkPath := os.TempDir() + "/testlink2"
	c.Assert(os.Symlink(tmpDir, linkPath), checker.IsNil)
	defer os.RemoveAll(linkPath)

	// Create first container
	dockerCmd(c, "run", "-v", fmt.Sprintf("%s:/tmp/test", linkPath), "busybox", "ls", "-lh", "/tmp/test")

	// Create second container with same symlinked path
	// This will fail if the referenced issue is hit with a "Volume exists" error
	dockerCmd(c, "run", "-v", fmt.Sprintf("%s:/tmp/test", linkPath), "busybox", "ls", "-lh", "/tmp/test")
}

//GH#10604: Test an "/etc" volume doesn't overlay special bind mounts in container
func (s *DockerSuite) TestRunCreateVolumeEtc(c *check.C) {
	// Not applicable on Windows as Windows does not support volumes
	testRequires(c, DaemonIsLinux)
	out, _ := dockerCmd(c, "run", "--dns=127.0.0.1", "-v", "/etc", "busybox", "cat", "/etc/resolv.conf")
	c.Assert(out, checker.Contains, "nameserver 127.0.0.1", check.Commentf("/etc volume mount hides /etc/resolv.conf"))

	out, _ = dockerCmd(c, "run", "-h=test123", "-v", "/etc", "busybox", "cat", "/etc/hostname")
	c.Assert(out, checker.Contains, "test123", check.Commentf("/etc volume mount hides /etc/hostname"))

	out, _ = dockerCmd(c, "run", "--add-host=test:192.168.0.1", "-v", "/etc", "busybox", "cat", "/etc/hosts")
	out = strings.Replace(out, "\n", " ", -1)
	c.Assert(out, checker.Contains, "192.168.0.1\ttest", check.Commentf("/etc volume mount hides /etc/hosts"))
	c.Assert(out, checker.Contains, "127.0.0.1\tlocalhost", check.Commentf("/etc volume mount hides /etc/hosts"))
}

func (s *DockerSuite) TestVolumesNoCopyData(c *check.C) {
	// Not applicable on Windows as Windows does not support volumes
	testRequires(c, DaemonIsLinux)
	_, err := buildImage("dataimage",
		`FROM busybox
		RUN mkdir -p /foo
		RUN touch /foo/bar`,
		true)
	c.Assert(err, checker.IsNil)

	dockerCmd(c, "run", "--name", "test", "-v", "/foo", "busybox")

	out, _, err := dockerCmdWithError("run", "--volumes-from", "test", "dataimage", "ls", "-lh", "/foo/bar")
	c.Assert(err, checker.NotNil, check.Commentf("Data was copied on volumes-from but shouldn't be:\n%q", out))
	c.Assert(out, checker.Contains, "No such file or directory", check.Commentf("Data was copied on volumes-from but shouldn't be"))

	tmpDir := randomTmpDirPath("docker_test_bind_mount_copy_data", daemonPlatform)
	out, _, err = dockerCmdWithError("run", "-v", tmpDir+":/foo", "dataimage", "ls", "-lh", "/foo/bar")
	c.Assert(err, checker.NotNil, check.Commentf("Data was copied on bind-mount but shouldn't be:\n%q", out))
	c.Assert(out, checker.Contains, "No such file or directory", check.Commentf("Data was copied on bind-mount but shouldn't be"))
}

func (s *DockerSuite) TestRunNoOutputFromPullInStdout(c *check.C) {
	// just run with unknown image
	cmd := exec.Command(dockerBinary, "run", "asdfsg")
	stdout := bytes.NewBuffer(nil)
	cmd.Stdout = stdout
	c.Assert(cmd.Run(), checker.NotNil, check.Commentf("Run with unknown image should fail"))
	c.Assert(stdout, checker.HasLen, 0, check.Commentf("Stdout contains output from pull: %s", stdout))
}

func (s *DockerSuite) TestRunVolumesCleanPaths(c *check.C) {
	// Not applicable on Windows as Windows does not support volumes
	testRequires(c, DaemonIsLinux)
	_, err := buildImage("run_volumes_clean_paths",
		`FROM busybox
		VOLUME /foo/`,
		true)
	c.Assert(err, checker.IsNil)

	dockerCmd(c, "run", "-v", "/foo", "-v", "/bar/", "--name", "dark_helmet", "run_volumes_clean_paths")

	out, err := inspectMountSourceField("dark_helmet", "/foo/")
	c.Assert(err, checker.Equals, errMountNotFound, check.Commentf("Found unexpected volume entry for '/foo/' in volumes\n%q", out))

	out, err = inspectMountSourceField("dark_helmet", "/foo")
	c.Assert(err, check.IsNil)
	c.Assert(out, checker.Contains, volumesConfigPath, check.Commentf("Volume was not defined for /foo"))

	out, err = inspectMountSourceField("dark_helmet", "/bar/")
	c.Assert(err, checker.Equals, errMountNotFound, check.Commentf("Found unexpected volume entry for '/bar/' in volumes\n%q", out))

	out, err = inspectMountSourceField("dark_helmet", "/bar")
	c.Assert(err, check.IsNil)
	c.Assert(out, checker.Contains, volumesConfigPath, check.Commentf("Volume was not defined for /bar"))
}

// Regression test for #3631
func (s *DockerSuite) TestRunSlowStdoutConsumer(c *check.C) {
	// TODO Windows: This should be able to run on Windows if can find an
	// alternate to /dev/zero and /dev/stdout.
	testRequires(c, DaemonIsLinux)
	cont := exec.Command(dockerBinary, "run", "--rm", "busybox", "/bin/sh", "-c", "dd if=/dev/zero of=/dev/stdout bs=1024 count=2000 | catv")

	stdout, err := cont.StdoutPipe()
	c.Assert(err, checker.IsNil)

	c.Assert(cont.Start(), checker.IsNil)
	n, err := consumeWithSpeed(stdout, 10000, 5*time.Millisecond, nil)
	c.Assert(err, checker.IsNil)

	expected := 2 * 1024 * 2000
	c.Assert(n, checker.Equals, expected)
}

func (s *DockerSuite) TestRunAllowPortRangeThroughExpose(c *check.C) {
	// TODO Windows: -P is not currently supported. Also network
	// settings are not propagated back.
	testRequires(c, DaemonIsLinux)
	out, _ := dockerCmd(c, "run", "-d", "--expose", "3000-3003", "-P", "busybox", "top")

	id := strings.TrimSpace(out)
	portstr, err := inspectFieldJSON(id, "NetworkSettings.Ports")
	c.Assert(err, check.IsNil)
	var ports nat.PortMap
	c.Assert(unmarshalJSON([]byte(portstr), &ports), checker.IsNil)
	for port, binding := range ports {
		portnum, _ := strconv.Atoi(strings.Split(string(port), "/")[0])
		c.Assert(portnum >= 3000, checker.True, check.Commentf("Port %d is out of range ", portnum))
		c.Assert(portnum <= 3003, checker.True, check.Commentf("Port %d is out of range ", portnum))

		c.Assert(binding, checker.NotNil, check.Commentf("Port is not mapped for the port %s", port))
		c.Assert(binding, checker.HasLen, 1, check.Commentf("Port is not mapped for the port %s", port))
		c.Assert(binding[0].HostPort, checker.HasLen, 0, check.Commentf("Port is not mapped for the port %s", port))
	}
}

// test docker run expose a invalid port
func (s *DockerSuite) TestRunExposePort(c *check.C) {
	out, _, err := dockerCmdWithError("run", "--expose", "80000", "busybox")
	//expose a invalid port should with a error out
	c.Assert(err, checker.NotNil)
	c.Assert(out, checker.Contains, "Invalid range format for --expose", check.Commentf("run --expose a invalid port should with error out"))
}

func (s *DockerSuite) TestRunUnknownCommand(c *check.C) {
	if daemonPlatform != "windows" {
		testRequires(c, NativeExecDriver)
	}
	out, _, _ := dockerCmdWithStdoutStderr(c, "create", "busybox", "/bin/nada")

	cID := strings.TrimSpace(out)
	_, _, err := dockerCmdWithError("start", cID)

	// Windows and Linux are different here by architectural design. Linux will
	// fail to start the container, so an error is expected. Windows will
	// successfully start the container, and once started attempt to execute
	// the command which will fail.
	if daemonPlatform == "windows" {
		// Wait for it to exit.
		waitExited(cID, 30*time.Second)
		c.Assert(err, check.IsNil)
	} else {
		c.Assert(err, check.NotNil)
	}

	rc, err := inspectField(cID, "State.ExitCode")
	c.Assert(err, checker.IsNil)
	c.Assert(rc, checker.Equals, "0", check.Commentf("ExitCode(%v) cannot be 0", rc))
}

func (s *DockerSuite) TestRunModeIpcHost(c *check.C) {
	// Not applicable on Windows as uses Unix-specific capabilities
	testRequires(c, SameHostDaemon, DaemonIsLinux, NotUserNamespace)

	hostIpc, err := os.Readlink("/proc/1/ns/ipc")
	c.Assert(err, checker.IsNil)

	out, _ := dockerCmd(c, "run", "--ipc=host", "busybox", "readlink", "/proc/self/ns/ipc")
	out = strings.Trim(out, "\n")
	c.Assert(out, checker.Equals, hostIpc, check.Commentf("IPC different with --ipc=host"))

	out, _ = dockerCmd(c, "run", "busybox", "readlink", "/proc/self/ns/ipc")
	out = strings.Trim(out, "\n")
	c.Assert(out, checker.Not(checker.Equals), hostIpc, check.Commentf("IPC should be different without --ipc=host\n"))
}

func (s *DockerSuite) TestRunModeIpcContainer(c *check.C) {
	// Not applicable on Windows as uses Unix-specific capabilities
	testRequires(c, SameHostDaemon, DaemonIsLinux, NotUserNamespace)

	out, _ := dockerCmd(c, "run", "-d", "busybox", "sh", "-c", "echo -n test > /dev/shm/test && top")

	id := strings.TrimSpace(out)
	state, err := inspectField(id, "State.Running")
	c.Assert(err, check.IsNil)
	c.Assert(state, checker.Equals, "true", check.Commentf("Container state is 'not running'"))

	pid1, err := inspectField(id, "State.Pid")
	c.Assert(err, check.IsNil)

	parentContainerIpc, err := os.Readlink(fmt.Sprintf("/proc/%s/ns/ipc", pid1))
	c.Assert(err, checker.IsNil)

	out, _ = dockerCmd(c, "run", fmt.Sprintf("--ipc=container:%s", id), "busybox", "readlink", "/proc/self/ns/ipc")
	out = strings.Trim(out, "\n")
	c.Assert(out, checker.Equals, parentContainerIpc, check.Commentf("IPC different with --ipc=container:%s %s != %s\n", id, parentContainerIpc, out))

	catOutput, _ := dockerCmd(c, "run", fmt.Sprintf("--ipc=container:%s", id), "busybox", "cat", "/dev/shm/test")
	c.Assert(catOutput, checker.Equals, "test", check.Commentf("Output of /dev/shm/test expected test"))
}

func (s *DockerSuite) TestRunModeIpcContainerNotExists(c *check.C) {
	// Not applicable on Windows as uses Unix-specific capabilities
	testRequires(c, DaemonIsLinux, NotUserNamespace)
	out, _, err := dockerCmdWithError("run", "-d", "--ipc", "container:abcd1234", "busybox", "top")
	c.Assert(err, checker.NotNil)
	c.Assert(out, checker.Contains, "abcd1234", check.Commentf("run IPC from a non exists container should with correct error out"))
}

func (s *DockerSuite) TestRunModeIpcContainerNotRunning(c *check.C) {
	// Not applicable on Windows as uses Unix-specific capabilities
	testRequires(c, SameHostDaemon, DaemonIsLinux, NotUserNamespace)

	out, _ := dockerCmd(c, "create", "busybox")

	id := strings.TrimSpace(out)
	out, _, err := dockerCmdWithError("run", fmt.Sprintf("--ipc=container:%s", id), "busybox")
	c.Assert(err, checker.NotNil, check.Commentf("Run container with ipc mode container should fail with non running container: %s", out))
}

func (s *DockerSuite) TestRunMountShmMqueueFromHost(c *check.C) {
	// Not applicable on Windows as uses Unix-specific capabilities
	testRequires(c, SameHostDaemon, DaemonIsLinux)

	dockerCmd(c, "run", "-d", "--name", "shmfromhost", "-v", "/dev/shm:/dev/shm", "busybox", "sh", "-c", "echo -n test > /dev/shm/test && top")
	volPath, err := inspectMountSourceField("shmfromhost", "/dev/shm")
	c.Assert(err, checker.IsNil)
	c.Assert(volPath, checker.Equals, "/dev/shm", check.Commentf("volumePath should have been /dev/shm"))

	out, _ := dockerCmd(c, "run", "--name", "ipchost", "--ipc", "host", "busybox", "cat", "/dev/shm/test")
	c.Assert(out, checker.Equals, "test", check.Commentf("Output of /dev/shm/test expected test"))
}

func (s *DockerSuite) TestContainerNetworkMode(c *check.C) {
	// Not applicable on Windows as uses Unix-specific capabilities
	testRequires(c, SameHostDaemon, DaemonIsLinux, NotUserNamespace)

	out, _ := dockerCmd(c, "run", "-d", "busybox", "top")
	id := strings.TrimSpace(out)
	c.Assert(waitRun(id), checker.IsNil)
	pid1, err := inspectField(id, "State.Pid")
	c.Assert(err, checker.IsNil)

	parentContainerNet, err := os.Readlink(fmt.Sprintf("/proc/%s/ns/net", pid1))
	c.Assert(err, checker.IsNil)

	out, _ = dockerCmd(c, "run", fmt.Sprintf("--net=container:%s", id), "busybox", "readlink", "/proc/self/ns/net")
	out = strings.Trim(out, "\n")
	c.Assert(out, checker.Equals, parentContainerNet, check.Commentf("NET different with --net=container:%s %s != %s\n", id, parentContainerNet, out))
}

func (s *DockerSuite) TestRunModePidHost(c *check.C) {
	// Not applicable on Windows as uses Unix-specific capabilities
	testRequires(c, NativeExecDriver, SameHostDaemon, DaemonIsLinux, NotUserNamespace)

	hostPid, err := os.Readlink("/proc/1/ns/pid")
	c.Assert(err, checker.IsNil)

	out, _ := dockerCmd(c, "run", "--pid=host", "busybox", "readlink", "/proc/self/ns/pid")
	out = strings.Trim(out, "\n")
	c.Assert(out, checker.Equals, hostPid, check.Commentf("PID different with --pid=host"))

	out, _ = dockerCmd(c, "run", "busybox", "readlink", "/proc/self/ns/pid")
	out = strings.Trim(out, "\n")
	c.Assert(out, checker.Equals, hostPid, check.Commentf("PID should be different without --pid=host"))
}

func (s *DockerSuite) TestRunModeUTSHost(c *check.C) {
	// Not applicable on Windows as uses Unix-specific capabilities
	testRequires(c, NativeExecDriver, SameHostDaemon, DaemonIsLinux)

	hostUTS, err := os.Readlink("/proc/1/ns/uts")
	c.Assert(err, checker.IsNil)

	out, _ := dockerCmd(c, "run", "--uts=host", "busybox", "readlink", "/proc/self/ns/uts")
	out = strings.Trim(out, "\n")
	c.Assert(out, checker.Equals, hostUTS, check.Commentf("UTS different with --uts=host"))

	out, _ = dockerCmd(c, "run", "busybox", "readlink", "/proc/self/ns/uts")
	out = strings.Trim(out, "\n")
	c.Assert(out, checker.Equals, hostUTS, check.Commentf("UTS should be different without --uts=host"))
}

func (s *DockerSuite) TestRunTLSverify(c *check.C) {
	dockerCmd(c, "ps") // should have worked

	// Regardless of whether we specify true or false we need to
	// test to make sure tls is turned on if --tlsverify is specified at all
	out, code, err := dockerCmdWithError("--tlsverify=false", "ps")
	c.Assert(err, checker.NotNil, check.Commentf("Should have failed: \net:%v\nout:%v\nerr:%v", code, out, err))
	c.Assert(code, checker.Not(checker.Equals), 0, check.Commentf("Should have failed: \net:%v\nout:%v\nerr:%v", code, out, err))
	c.Assert(out, checker.Contains, "trying to connect", check.Commentf("Should have failed: \net:%v\nout:%v\nerr:%v", code, out, err))

	out, code, err = dockerCmdWithError("--tlsverify=true", "ps")
	c.Assert(err, checker.NotNil, check.Commentf("Should have failed: \net:%v\nout:%v\nerr:%v", code, out, err))
	c.Assert(code, checker.Not(checker.Equals), 0, check.Commentf("Should have failed: \net:%v\nout:%v\nerr:%v", code, out, err))
	c.Assert(out, checker.Contains, "cert", check.Commentf("Should have failed: \net:%v\nout:%v\nerr:%v", code, out, err))
}

func (s *DockerSuite) TestRunPortFromDockerRangeInUse(c *check.C) {
	// TODO Windows. Once moved to libnetwork/CNM, this may be able to be
	// re-instated.
	testRequires(c, DaemonIsLinux)
	// first find allocator current position
	out, _ := dockerCmd(c, "run", "-d", "-p", ":80", "busybox", "top")

	id := strings.TrimSpace(out)
	out, _ = dockerCmd(c, "port", id)

	out = strings.TrimSpace(out)
	c.Assert(out, checker.Not(checker.Equals), "", check.Commentf("docker port command output is empty"))

	out = strings.Split(out, ":")[1]
	lastPort, err := strconv.Atoi(out)
	c.Assert(err, checker.IsNil)
	port := lastPort + 1
	l, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	c.Assert(err, checker.IsNil)
	defer l.Close()

	out, _ = dockerCmd(c, "run", "-d", "-p", ":80", "busybox", "top")

	id = strings.TrimSpace(out)
	dockerCmd(c, "port", id)
}

func (s *DockerSuite) TestRunTtyWithPipe(c *check.C) {
	errChan := make(chan error)
	go func() {
		defer close(errChan)

		cmd := exec.Command(dockerBinary, "run", "-ti", "busybox", "true")
		if _, err := cmd.StdinPipe(); err != nil {
			errChan <- err
			return
		}

		expected := "cannot enable tty mode"
		if out, _, err := runCommandWithOutput(cmd); err == nil {
			errChan <- fmt.Errorf("run should have failed")
			return
		} else if !strings.Contains(out, expected) {
			errChan <- fmt.Errorf("run failed with error %q: expected %q", out, expected)
			return
		}
	}()

	select {
	case err := <-errChan:
		c.Assert(err, checker.IsNil)
	case <-time.After(6 * time.Second):
		c.Fatal("container is running but should have failed")
	}
}

func (s *DockerSuite) TestRunNonLocalMacAddress(c *check.C) {
	addr := "00:16:3E:08:00:50"
	cmd := "ifconfig"
	image := "busybox"
	expected := addr

	if daemonPlatform == "windows" {
		cmd = "ipconfig /all"
		image = WindowsBaseImage
		expected = strings.Replace(addr, ":", "-", -1)

	}

	out, _ := dockerCmd(c, "run", "--mac-address", addr, image, cmd)
	c.Assert(out, checker.Contains, expected)
}

func (s *DockerSuite) TestRunNetHost(c *check.C) {
	// Not applicable on Windows as uses Unix-specific capabilities
	testRequires(c, SameHostDaemon, DaemonIsLinux, NotUserNamespace)

	hostNet, err := os.Readlink("/proc/1/ns/net")
	c.Assert(err, checker.IsNil)

	out, _ := dockerCmd(c, "run", "--net=host", "busybox", "readlink", "/proc/self/ns/net")
	out = strings.Trim(out, "\n")
	c.Assert(out, checker.Equals, hostNet, check.Commentf("Net namespace different with --net=host"))

	out, _ = dockerCmd(c, "run", "busybox", "readlink", "/proc/self/ns/net")
	out = strings.Trim(out, "\n")
	c.Assert(out, checker.Not(checker.Equals), hostNet, check.Commentf("Net namespace should be different without --net=host"))
}

func (s *DockerSuite) TestRunNetHostTwiceSameName(c *check.C) {
	// TODO Windows. As Windows networking evolves and converges towards
	// CNM, this test may be possible to enable on Windows.
	testRequires(c, SameHostDaemon, DaemonIsLinux, NotUserNamespace)

	dockerCmd(c, "run", "--rm", "--name=thost", "--net=host", "busybox", "true")
	dockerCmd(c, "run", "--rm", "--name=thost", "--net=host", "busybox", "true")
}

func (s *DockerSuite) TestRunNetContainerWhichHost(c *check.C) {
	// Not applicable on Windows as uses Unix-specific capabilities
	testRequires(c, SameHostDaemon, DaemonIsLinux, NotUserNamespace)

	hostNet, err := os.Readlink("/proc/1/ns/net")
	c.Assert(err, checker.IsNil)

	dockerCmd(c, "run", "-d", "--net=host", "--name=test", "busybox", "top")

	out, _ := dockerCmd(c, "run", "--net=container:test", "busybox", "readlink", "/proc/self/ns/net")
	out = strings.Trim(out, "\n")
	c.Assert(out, checker.Equals, hostNet, check.Commentf("Container should have host network namespace"))
}

func (s *DockerSuite) TestRunAllowPortRangeThroughPublish(c *check.C) {
	// TODO Windows. This may be possible to enable in the future. However,
	// Windows does not currently support --expose, or populate the network
	// settings seen through inspect.
	testRequires(c, DaemonIsLinux)
	out, _ := dockerCmd(c, "run", "-d", "--expose", "3000-3003", "-p", "3000-3003", "busybox", "top")

	id := strings.TrimSpace(out)
	portstr, err := inspectFieldJSON(id, "NetworkSettings.Ports")
	c.Assert(err, checker.IsNil)

	var ports nat.PortMap
	err = unmarshalJSON([]byte(portstr), &ports)
	for port, binding := range ports {
		portnum, _ := strconv.Atoi(strings.Split(string(port), "/")[0])
		c.Assert(portnum >= 3000, checker.True, check.Commentf("Port %d is out of range ", portnum))
		c.Assert(portnum <= 3003, checker.True, check.Commentf("Port %d is out of range ", portnum))

		c.Assert(binding, checker.NotNil, check.Commentf("Port is not mapped for the port %s", port))
		c.Assert(binding, checker.HasLen, 1, check.Commentf("Port is not mapped for the port %s", port))
		c.Assert(binding[0].HostPort, checker.HasLen, 0, check.Commentf("Port is not mapped for the port %s", port))
	}
}

func (s *DockerSuite) TestRunSetDefaultRestartPolicy(c *check.C) {
	dockerCmd(c, "run", "-d", "--name", "test", "busybox", "sleep", "30")
	out, err := inspectField("test", "HostConfig.RestartPolicy.Name")
	c.Assert(err, checker.IsNil)
	c.Assert(out, checker.Equals, "no", check.Commentf("Set default restart policy failed"))
}

func (s *DockerSuite) TestRunRestartMaxRetries(c *check.C) {
	out, _ := dockerCmd(c, "run", "-d", "--restart=on-failure:3", "busybox", "false")
	timeout := 10 * time.Second
	if daemonPlatform == "windows" {
		timeout = 45 * time.Second
	}

	id := strings.TrimSpace(string(out))
	if err := waitInspect(id, "{{ .State.Restarting }} {{ .State.Running }}", "false false", timeout); err != nil {
		c.Fatal(err)
	}

	count, err := inspectField(id, "RestartCount")
	c.Assert(err, checker.IsNil)
	c.Assert(count, checker.Equals, "3", check.Commentf("Container was restarted %s times, expected %d", count, 3))

	maximumRetryCount, err := inspectField(id, "HostConfig.RestartPolicy.MaximumRetryCount")
	c.Assert(err, checker.IsNil)
	c.Assert(maximumRetryCount, checker.Equals, "3", check.Commentf("Container Maximum Retry Count is %s, expected %s", maximumRetryCount, "3"))
}

func (s *DockerSuite) TestRunContainerWithWritableRootfs(c *check.C) {
	dockerCmd(c, "run", "--rm", "busybox", "touch", "/file")
}

func (s *DockerSuite) TestRunContainerWithReadonlyRootfs(c *check.C) {
	// Not applicable on Windows which does not support --read-only
	testRequires(c, NativeExecDriver, DaemonIsLinux)

	for _, f := range []string{"/file", "/etc/hosts", "/etc/resolv.conf", "/etc/hostname", "/sys/kernel", "/dev/.dont.touch.me"} {
		testReadOnlyFile(f, c)
	}
}

func (s *DockerSuite) TestPermissionsPtsReadonlyRootfs(c *check.C) {
	// Not applicable on Windows due to use of Unix specific functionality, plus
	// the use of --read-only which is not supported.
	// --read-only + userns has remount issues
	testRequires(c, DaemonIsLinux, NativeExecDriver, NotUserNamespace)

	// Ensure we have not broken writing /dev/pts
	out, status := dockerCmd(c, "run", "--read-only", "--rm", "busybox", "mount")
	c.Assert(status, checker.Equals, 0, check.Commentf("Could not obtain mounts when checking /dev/pts mntpnt."))

	expected := "type devpts (rw,"
	c.Assert(string(out), checker.Contains, expected)
}

func testReadOnlyFile(filename string, c *check.C) {
	// Not applicable on Windows which does not support --read-only
	testRequires(c, NativeExecDriver, DaemonIsLinux, NotUserNamespace)

	out, _, err := dockerCmdWithError("run", "--read-only", "--rm", "busybox", "touch", filename)
	c.Assert(err, checker.NotNil, check.Commentf("expected container to error on run with read only error"))

	expected := "Read-only file system"
	c.Assert(string(out), checker.Contains, expected)

	out, _, err = dockerCmdWithError("run", "--read-only", "--privileged", "--rm", "busybox", "touch", filename)
	c.Assert(err, checker.NotNil, check.Commentf("expected container to error on run with read only error"))

	expected = "Read-only file system"
	c.Assert(string(out), checker.Contains, expected)
}

func (s *DockerSuite) TestRunContainerWithReadonlyEtcHostsAndLinkedContainer(c *check.C) {
	// Not applicable on Windows which does not support --link
	// --read-only + userns has remount issues
	testRequires(c, NativeExecDriver, DaemonIsLinux, NotUserNamespace)

	dockerCmd(c, "run", "-d", "--name", "test-etc-hosts-ro-linked", "busybox", "top")

	out, _ := dockerCmd(c, "run", "--read-only", "--link", "test-etc-hosts-ro-linked:testlinked", "busybox", "cat", "/etc/hosts")
	c.Assert(string(out), checker.Contains, "testlinked", check.Commentf("Expected /etc/hosts to be updated even if --read-only enabled"))
}

func (s *DockerSuite) TestRunContainerWithReadonlyRootfsWithDnsFlag(c *check.C) {
	// Not applicable on Windows which does not support either --read-only or --dns.
	// --read-only + userns has remount issues
	testRequires(c, NativeExecDriver, DaemonIsLinux, NotUserNamespace)

	out, _ := dockerCmd(c, "run", "--read-only", "--dns", "1.1.1.1", "busybox", "/bin/cat", "/etc/resolv.conf")
	c.Assert(string(out), checker.Contains, "1.1.1.1", check.Commentf("Expected /etc/resolv.conf to be updated even if --read-only enabled and --dns flag used"))
}

func (s *DockerSuite) TestRunContainerWithReadonlyRootfsWithAddHostFlag(c *check.C) {
	// Not applicable on Windows which does not support --read-only
	// --read-only + userns has remount issues
	testRequires(c, NativeExecDriver, DaemonIsLinux, NotUserNamespace)

	out, _ := dockerCmd(c, "run", "--read-only", "--add-host", "testreadonly:127.0.0.1", "busybox", "/bin/cat", "/etc/hosts")
	c.Assert(string(out), checker.Contains, "testreadonly", check.Commentf("Expected /etc/hosts to be updated even if --read-only enabled and --add-host flag used"))
}

func (s *DockerSuite) TestRunVolumesFromRestartAfterRemoved(c *check.C) {
	// TODO Windows. Not applicable on Windows which does not support volumes.
	// This may be possible to add in the future.
	testRequires(c, DaemonIsLinux)
	dockerCmd(c, "run", "-d", "--name", "voltest", "-v", "/foo", "busybox")
	dockerCmd(c, "run", "-d", "--name", "restarter", "--volumes-from", "voltest", "busybox", "top")

	// Remove the main volume container and restart the consuming container
	dockerCmd(c, "rm", "-f", "voltest")

	// This should not fail since the volumes-from were already applied
	dockerCmd(c, "restart", "restarter")
}

// run container with --rm should remove container if exit code != 0
func (s *DockerSuite) TestRunContainerWithRmFlagExitCodeNotEqualToZero(c *check.C) {
	name := "flowers"
	out, _, err := dockerCmdWithError("run", "--name", name, "--rm", "busybox", "ls", "/notexists")
	c.Assert(err, checker.NotNil)

	out, err = getAllContainers()
	c.Assert(err, checker.IsNil)

	c.Assert(out, checker.Equals, "", check.Commentf("Expected not to have containers"))
}

func (s *DockerSuite) TestRunContainerWithRmFlagCannotStartContainer(c *check.C) {
	name := "sparkles"
	out, _, err := dockerCmdWithError("run", "--name", name, "--rm", "busybox", "commandNotFound")
	c.Assert(err, checker.NotNil)

	out, err = getAllContainers()
	c.Assert(err, checker.IsNil)

	c.Assert(out, checker.Equals, "", check.Commentf("Expected not to have containers"))
}

func (s *DockerSuite) TestRunPidHostWithChildIsKillable(c *check.C) {
	// Not applicable on Windows as uses Unix specific functionality
	testRequires(c, DaemonIsLinux, NotUserNamespace)
	name := "ibuildthecloud"
	dockerCmd(c, "run", "-d", "--pid=host", "--name", name, "busybox", "sh", "-c", "sleep 30; echo hi")

	c.Assert(waitRun(name), checker.IsNil)

	errchan := make(chan error)
	go func() {
		if out, _, err := dockerCmdWithError("kill", name); err != nil {
			errchan <- fmt.Errorf("%v:\n%s", err, out)
		}
		close(errchan)
	}()
	select {
	case err := <-errchan:
		c.Assert(err, checker.IsNil)
	case <-time.After(5 * time.Second):
		c.Fatal("Kill container timed out")
	}
}

func (s *DockerSuite) TestRunWithTooSmallMemoryLimit(c *check.C) {
	// TODO Windows. This may be possible to enable once Windows supports
	// memory limits on containers
	testRequires(c, DaemonIsLinux)
	// this memory limit is 1 byte less than the min, which is 4MB
	// https://github.com/docker/docker/blob/v1.5.0/daemon/create.go#L22
	out, _, err := dockerCmdWithError("run", "-m", "4194303", "busybox")

	c.Assert(err, checker.NotNil, check.Commentf("expected run to fail when using too low a memory limit: %q", out))
	c.Assert(out, checker.Contains, check.Commentf("Minimum memory limit allowed is 4MB"))
}

func (s *DockerSuite) TestRunWriteToProcAsound(c *check.C) {
	// Not applicable on Windows as uses Unix specific functionality
	testRequires(c, DaemonIsLinux)
	_, code, err := dockerCmdWithError("run", "busybox", "sh", "-c", "echo 111 >> /proc/asound/version")

	c.Assert(err, checker.NotNil, check.Commentf("standard container should not be able to write to /proc/asound"))
	c.Assert(code, checker.Not(checker.Equals), 0, check.Commentf("standard container should not be able to write to /proc/asound"))
}

func (s *DockerSuite) TestRunReadProcTimer(c *check.C) {
	// Not applicable on Windows as uses Unix specific functionality
	testRequires(c, NativeExecDriver, DaemonIsLinux)
	out, _ := dockerCmd(c, "run", "busybox", "cat", "/proc/timer_stats")
	// FIXME WAAATTT ?
	// if code != 0 {
	// 	return
	// }
	c.Assert(strings.Trim(out, "\n "), checker.Equals, "", check.Commentf("expected to receive no output from /proc/timer_stats"))
}

func (s *DockerSuite) TestRunReadProcLatency(c *check.C) {
	// Not applicable on Windows as uses Unix specific functionality
	testRequires(c, NativeExecDriver, DaemonIsLinux)
	// some kernels don't have this configured so skip the test if this file is not found
	// on the host running the tests.
	if _, err := os.Stat("/proc/latency_stats"); err != nil {
		c.Skip("kernel doesnt have latency_stats configured")
		return
	}
	out, _ := dockerCmd(c, "run", "busybox", "cat", "/proc/latency_stats")
	// FIXME WAAATTT ?
	// if code != 0 {
	// 	return
	// }
	c.Assert(strings.Trim(out, "\n "), checker.Equals, "", check.Commentf("expected to receive no output from /proc/latency_stats"))
}

func (s *DockerSuite) TestRunReadFilteredProc(c *check.C) {
	// Not applicable on Windows as uses Unix specific functionality
	testRequires(c, Apparmor, DaemonIsLinux, NotUserNamespace)

	testReadPaths := []string{
		"/proc/latency_stats",
		"/proc/timer_stats",
		"/proc/kcore",
	}
	for i, filePath := range testReadPaths {
		name := fmt.Sprintf("procsieve-%d", i)
		shellCmd := fmt.Sprintf("exec 3<%s", filePath)

		dockerCmd(c, "run", "--privileged", "--security-opt", "apparmor:docker-default", "--name", name, "busybox", "sh", "-c", shellCmd)
		// out, _ := dockerCmd(c, "run", "--privileged", "--security-opt", "apparmor:docker-default", "--name", name, "busybox", "sh", "-c", shellCmd)
		// FIXME WAAATTT ?
		// if exitCode != 0 {
		// 	return
		// }
		// if err != nil {
		// 	c.Fatalf("Open FD for read should have failed with permission denied, got: %s, %v", out, err)
		// }
	}
}

func (s *DockerSuite) TestMountIntoProc(c *check.C) {
	// Not applicable on Windows as uses Unix specific functionality
	testRequires(c, DaemonIsLinux)
	testRequires(c, NativeExecDriver)
	_, code, err := dockerCmdWithError("run", "-v", "/proc//sys", "busybox", "true")
	c.Assert(err, checker.NotNil, check.Commentf("container should not be able to mount into /proc"))
	c.Assert(code, checker.Not(checker.Equals), 0, check.Commentf("container should not be able to mount into /proc"))
}

func (s *DockerSuite) TestMountIntoSys(c *check.C) {
	// Not applicable on Windows as uses Unix specific functionality
	testRequires(c, DaemonIsLinux)
	testRequires(c, NativeExecDriver, NotUserNamespace)
	dockerCmd(c, "run", "-v", "/sys/fs/cgroup", "busybox", "true")
}

func (s *DockerSuite) TestRunUnshareProc(c *check.C) {
	c.Skip("unstable test: is apparmor in a container reliable?")

	// Not applicable on Windows as uses Unix specific functionality
	testRequires(c, Apparmor, NativeExecDriver, DaemonIsLinux)

	name := "acidburn"
	out, _, err := dockerCmdWithError("run", "--name", name, "jess/unshare", "unshare", "-p", "-m", "-f", "-r", "--mount-proc=/proc", "mount")
	c.Assert(err, checker.NotNil, check.Commentf("unshare should have failed with permission denied, got: %s, %v", out, err))
	c.Assert(out, checker.Contains, "Permission denied", check.Commentf("unshare should have failed with permission denied, got: %s, %v", out, err))

	name = "cereal"
	out, _, err = dockerCmdWithError("run", "--name", name, "jess/unshare", "unshare", "-p", "-m", "-f", "-r", "mount", "-t", "proc", "none", "/proc")
	c.Assert(err, checker.NotNil, check.Commentf("unshare should have failed with permission denied, got: %s, %v", out, err))
	c.Assert(out, checker.Contains, "Permission denied", check.Commentf("unshare should have failed with permission denied, got: %s, %v", out, err))

	/* Ensure still fails if running privileged with the default policy */
	name = "crashoverride"
	out, _, err = dockerCmdWithError("run", "--privileged", "--security-opt", "apparmor:docker-default", "--name", name, "jess/unshare", "unshare", "-p", "-m", "-f", "-r", "mount", "-t", "proc", "none", "/proc")
	c.Assert(err, checker.NotNil, check.Commentf("unshare should have failed with permission denied, got: %s, %v", out, err))
	c.Assert(out, checker.Contains, "Permission denied", check.Commentf("unshare should have failed with permission denied, got: %s, %v", out, err))
}

func (s *DockerSuite) TestRunPublishPort(c *check.C) {
	// TODO Windows: This may be possible once Windows moves to libnetwork and CNM
	testRequires(c, DaemonIsLinux)
	dockerCmd(c, "run", "-d", "--name", "test", "--expose", "8080", "busybox", "top")
	out, _ := dockerCmd(c, "port", "test")
	out = strings.Trim(out, "\r\n")
	c.Assert(out, checker.Equals, "", check.Commentf("run without --publish-all should not publish port, out should be nil"))
}

// Issue #10184.
func (s *DockerSuite) TestDevicePermissions(c *check.C) {
	// Not applicable on Windows as uses Unix specific functionality
	testRequires(c, DaemonIsLinux)
	testRequires(c, NativeExecDriver)
	const permissions = "crw-rw-rw-"
	out, _ := dockerCmd(c, "run", "--device", "/dev/fuse:/dev/fuse:mrw", "busybox:latest", "ls", "-l", "/dev/fuse")
	c.Assert(out, checker.HasPrefix, permissions)
}

func (s *DockerSuite) TestRunCapAddCHOWN(c *check.C) {
	// Not applicable on Windows as uses Unix specific functionality
	testRequires(c, DaemonIsLinux)
	testRequires(c, NativeExecDriver)
	out, _ := dockerCmd(c, "run", "--cap-drop=ALL", "--cap-add=CHOWN", "busybox", "sh", "-c", "adduser -D -H newuser && chown newuser /home && echo ok")

	c.Assert(strings.Trim(out, "\r\n"), checker.Equals, "ok")
}

// https://github.com/docker/docker/pull/14498
func (s *DockerSuite) TestVolumeFromMixedRWOptions(c *check.C) {
	// Not applicable on Windows as volumes are not supported on Winodws
	testRequires(c, DaemonIsLinux)
	dockerCmd(c, "run", "--name", "parent", "-v", "/test", "busybox", "true")
	dockerCmd(c, "run", "--volumes-from", "parent:ro", "--name", "test-volumes-1", "busybox", "true")
	dockerCmd(c, "run", "--volumes-from", "parent:rw", "--name", "test-volumes-2", "busybox", "true")

	mRO, err := inspectMountPoint("test-volumes-1", "/test")
	c.Assert(err, checker.IsNil)
	c.Assert(mRO.RW, checker.False, check.Commentf("Expected RO volume was RW"))

	mRW, err := inspectMountPoint("test-volumes-2", "/test")
	c.Assert(err, checker.IsNil)
	c.Assert(mRW.RW, checker.True, check.Commentf("Expected RW volume was RO"))
}

func (s *DockerSuite) TestRunWriteFilteredProc(c *check.C) {
	// Not applicable on Windows as uses Unix specific functionality
	testRequires(c, Apparmor, NativeExecDriver, DaemonIsLinux, NotUserNamespace)

	testWritePaths := []string{
		/* modprobe and core_pattern should both be denied by generic
		 * policy of denials for /proc/sys/kernel. These files have been
		 * picked to be checked as they are particularly sensitive to writes */
		"/proc/sys/kernel/modprobe",
		"/proc/sys/kernel/core_pattern",
		"/proc/sysrq-trigger",
		"/proc/kcore",
	}
	for i, filePath := range testWritePaths {
		name := fmt.Sprintf("writeprocsieve-%d", i)

		shellCmd := fmt.Sprintf("exec 3>%s", filePath)
		// out, _ := dockerCmd(c, "run", "--privileged", "--security-opt", "apparmor:docker-default", "--name", name, "busybox", "sh", "-c", shellCmd)
		dockerCmd(c, "run", "--privileged", "--security-opt", "apparmor:docker-default", "--name", name, "busybox", "sh", "-c", shellCmd)
		// FIXME Waaatt
		// if code != 0 {
		// 	return
		// }
		// if err != nil {
		// 	c.Fatalf("Open FD for write should have failed with permission denied, got: %s, %v", out, err)
		// }
	}
}

func (s *DockerSuite) TestRunNetworkFilesBindMount(c *check.C) {
	// Not applicable on Windows as uses Unix specific functionality
	testRequires(c, SameHostDaemon, DaemonIsLinux)

	expected := "test123"

	filename := createTmpFile(c, expected)
	defer os.Remove(filename)

	nwfiles := []string{"/etc/resolv.conf", "/etc/hosts", "/etc/hostname"}

	for i := range nwfiles {
		actual, _ := dockerCmd(c, "run", "-v", filename+":"+nwfiles[i], "busybox", "cat", nwfiles[i])
		c.Assert(actual, checker.Equals, expected)
	}
}

func (s *DockerSuite) TestRunNetworkFilesBindMountRO(c *check.C) {
	// Not applicable on Windows as uses Unix specific functionality
	testRequires(c, SameHostDaemon, DaemonIsLinux)

	filename := createTmpFile(c, "test123")
	defer os.Remove(filename)

	nwfiles := []string{"/etc/resolv.conf", "/etc/hosts", "/etc/hostname"}

	for i := range nwfiles {
		_, exitCode, err := dockerCmdWithError("run", "-v", filename+":"+nwfiles[i]+":ro", "busybox", "touch", nwfiles[i])
		c.Assert(err, checker.NotNil, check.Commentf("run should fail because bind mount of %s is ro: exit code %d", nwfiles[i], exitCode))
		c.Assert(exitCode, checker.Equals, 0, check.Commentf("run should fail because bind mount of %s is ro: exit code %d", nwfiles[i], exitCode))
	}
}

func (s *DockerSuite) TestRunNetworkFilesBindMountROFilesystem(c *check.C) {
	// Not applicable on Windows as uses Unix specific functionality
	// --read-only + userns has remount issues
	testRequires(c, SameHostDaemon, DaemonIsLinux, NotUserNamespace)

	filename := createTmpFile(c, "test123")
	defer os.Remove(filename)

	nwfiles := []string{"/etc/resolv.conf", "/etc/hosts", "/etc/hostname"}

	for i := range nwfiles {
		dockerCmd(c, "run", "-v", filename+":"+nwfiles[i], "--read-only", "busybox", "touch", nwfiles[i])
	}

	for i := range nwfiles {
		_, exitCode, err := dockerCmdWithError("run", "-v", filename+":"+nwfiles[i]+":ro", "--read-only", "busybox", "touch", nwfiles[i])
		c.Assert(err, checker.NotNil, check.Commentf("run should fail because %s is mounted read-only on read-only root filesystem: exit code %d", nwfiles[i], exitCode))
		c.Assert(exitCode, checker.Not(checker.Equals), 0, check.Commentf("run should fail because %s is mounted read-only on read-only root filesystem: exit code %d", nwfiles[i], exitCode))
	}
}

func (s *DockerTrustSuite) TestTrustedRun(c *check.C) {
	// Windows does not support this functionality
	testRequires(c, DaemonIsLinux)
	repoName := s.setupTrustedImage(c, "trusted-run")

	// Try run
	runCmd := exec.Command(dockerBinary, "run", repoName)
	s.trustedCmd(runCmd)
	out, _, err := runCommandWithOutput(runCmd)
	c.Assert(err, checker.IsNil, check.Commentf("Error running trusted run: %s\n%s\n", err, out))
	c.Assert(string(out), checker.Contains, "Tagging", check.Commentf("Missing expected output on trusted push"))

	dockerCmd(c, "rmi", repoName)

	// Try untrusted run to ensure we pushed the tag to the registry
	runCmd = exec.Command(dockerBinary, "run", "--disable-content-trust=true", repoName)
	s.trustedCmd(runCmd)
	out, _, err = runCommandWithOutput(runCmd)
	c.Assert(err, checker.IsNil)

	c.Assert(string(out), checker.Contains, "Status: Downloaded", check.Commentf("Missing expected output on trusted run with --disable-content-trust"))
}

func (s *DockerTrustSuite) TestUntrustedRun(c *check.C) {
	// Windows does not support this functionality
	testRequires(c, DaemonIsLinux)
	repoName := fmt.Sprintf("%v/dockercli/trusted:latest", privateRegistryURL)
	// tag the image and upload it to the private registry
	dockerCmd(c, "tag", "busybox", repoName)
	dockerCmd(c, "push", repoName)
	dockerCmd(c, "rmi", repoName)

	// Try trusted run on untrusted tag
	runCmd := exec.Command(dockerBinary, "run", repoName)
	s.trustedCmd(runCmd)
	out, _, err := runCommandWithOutput(runCmd)
	c.Assert(err, checker.NotNil)
	c.Assert(string(out), checker.Contains, "no trust data available")
}

func (s *DockerTrustSuite) TestRunWhenCertExpired(c *check.C) {
	// Windows does not support this functionality
	testRequires(c, DaemonIsLinux)
	c.Skip("Currently changes system time, causing instability")
	repoName := s.setupTrustedImage(c, "trusted-run-expired")

	// Certificates have 10 years of expiration
	elevenYearsFromNow := time.Now().Add(time.Hour * 24 * 365 * 11)

	runAtDifferentDate(elevenYearsFromNow, func() {
		// Try run
		runCmd := exec.Command(dockerBinary, "run", repoName)
		s.trustedCmd(runCmd)
		out, _, err := runCommandWithOutput(runCmd)
		c.Assert(err, checker.NotNil, check.Commentf("Error running trusted run in the distant future"))
		c.Assert(string(out), checker.Contains, "could not validate the path to a trusted root", check.Commentf("Error running trusted run in the distant future"))
	})

	runAtDifferentDate(elevenYearsFromNow, func() {
		// Try run
		runCmd := exec.Command(dockerBinary, "run", "--disable-content-trust", repoName)
		s.trustedCmd(runCmd)
		out, _, err := runCommandWithOutput(runCmd)
		c.Assert(err, checker.IsNil, check.Commentf("Error running untrusted run in the distant future"))
		c.Assert(string(out), checker.Contains, "Status: Downloaded", check.Commentf("Error running untrusted run in the distant future"))
	})
}

func (s *DockerTrustSuite) TestTrustedRunFromBadTrustServer(c *check.C) {
	// Windows does not support this functionality
	testRequires(c, DaemonIsLinux)
	repoName := fmt.Sprintf("%v/dockerclievilrun/trusted:latest", privateRegistryURL)
	evilLocalConfigDir, err := ioutil.TempDir("", "evil-local-config-dir")
	c.Assert(err, checker.IsNil)

	// tag the image and upload it to the private registry
	dockerCmd(c, "tag", "busybox", repoName)

	pushCmd := exec.Command(dockerBinary, "push", repoName)
	s.trustedCmd(pushCmd)
	out, _, err := runCommandWithOutput(pushCmd)
	c.Assert(err, checker.IsNil, check.Commentf("Error running trusted push: %s\n%s", err, out))
	c.Assert(string(out), checker.Contains, "Signing and pushing trust metadata", check.Commentf("Missing expected output on trusted push"))

	dockerCmd(c, "rmi", repoName)

	// Try run
	runCmd := exec.Command(dockerBinary, "run", repoName)
	s.trustedCmd(runCmd)
	out, _, err = runCommandWithOutput(runCmd)
	c.Assert(err, checker.NotNil, check.Commentf("Error running trusted run: %s\n%s", err, out))
	c.Assert(string(out), checker.Contains, "Tagging", check.Commentf("Missing expected output on trusted push"))

	dockerCmd(c, "rmi", repoName)

	// Kill the notary server, start a new "evil" one.
	s.not.Close()
	s.not, err = newTestNotary(c)
	c.Assert(err, checker.IsNil)

	// In order to make an evil server, lets re-init a client (with a different trust dir) and push new data.
	// tag an image and upload it to the private registry
	dockerCmd(c, "--config", evilLocalConfigDir, "tag", "busybox", repoName)

	// Push up to the new server
	pushCmd = exec.Command(dockerBinary, "--config", evilLocalConfigDir, "push", repoName)
	s.trustedCmd(pushCmd)
	out, _, err = runCommandWithOutput(pushCmd)
	c.Assert(err, checker.IsNil, check.Commentf("Error running trusted push: %s\n%s", err, out))
	c.Assert(string(out), checker.Contains, "Signing and pushing trust metadata", check.Commentf("Missing expected output on trusted push"))

	// Now, try running with the original client from this new trust server. This should fail.
	runCmd = exec.Command(dockerBinary, "run", repoName)
	s.trustedCmd(runCmd)
	out, _, err = runCommandWithOutput(runCmd)
	c.Assert(err, checker.NotNil, check.Commentf("Expected to fail on this run due to different remote data: %s\n%s", err, out))
	c.Assert(string(out), checker.Contains, "failed to validate data with current trusted certificates", check.Commentf("Missing expected output on trusted push"))
}

func (s *DockerSuite) TestPtraceContainerProcsFromHost(c *check.C) {
	// Not applicable on Windows as uses Unix specific functionality
	testRequires(c, DaemonIsLinux, SameHostDaemon)

	out, _ := dockerCmd(c, "run", "-d", "busybox", "top")
	id := strings.TrimSpace(out)
	c.Assert(waitRun(id), checker.IsNil)
	pid1, err := inspectField(id, "State.Pid")
	c.Assert(err, checker.IsNil)

	_, err = os.Readlink(fmt.Sprintf("/proc/%s/ns/net", pid1))
	c.Assert(err, checker.IsNil)
}

func (s *DockerSuite) TestAppArmorDeniesPtrace(c *check.C) {
	// Not applicable on Windows as uses Unix specific functionality
	testRequires(c, SameHostDaemon, NativeExecDriver, Apparmor, DaemonIsLinux, NotGCCGO)

	// Run through 'sh' so we are NOT pid 1. Pid 1 may be able to trace
	// itself, but pid>1 should not be able to trace pid1.
	_, _, err := dockerCmdWithError("run", "busybox", "sh", "-c", "sh -c readlink /proc/1/ns/net")
	c.Assert(err, checker.NotNil, check.Commentf("ptrace was not successfully restricted by AppArmor"))
}

func (s *DockerSuite) TestAppArmorTraceSelf(c *check.C) {
	// Not applicable on Windows as uses Unix specific functionality
	testRequires(c, DaemonIsLinux, SameHostDaemon, Apparmor)

	dockerCmd(c, "run", "busybox", "readlink", "/proc/1/ns/net")
}

func (s *DockerSuite) TestAppArmorDeniesChmodProc(c *check.C) {
	c.Skip("Test is failing, and what it tests is unclear")

	// Not applicable on Windows as uses Unix specific functionality
	testRequires(c, SameHostDaemon, NativeExecDriver, Apparmor, DaemonIsLinux)
	_, exitCode, _ := dockerCmdWithError("run", "busybox", "chmod", "744", "/proc/cpuinfo")
	if exitCode == 0 {
		// If our test failed, attempt to repair the host system...
		_, exitCode, _ := dockerCmdWithError("run", "busybox", "chmod", "444", "/proc/cpuinfo")
		if exitCode == 0 {
			c.Fatal("AppArmor was unsuccessful in prohibiting chmod of /proc/* files.")
		}
	}
}

func (s *DockerSuite) TestRunCapAddSYSTIME(c *check.C) {
	// Not applicable on Windows as uses Unix specific functionality
	testRequires(c, DaemonIsLinux, NativeExecDriver)

	dockerCmd(c, "run", "--cap-drop=ALL", "--cap-add=SYS_TIME", "busybox", "sh", "-c", "grep ^CapEff /proc/self/status | sed 's/^CapEff:\t//' | grep ^0000000002000000$")
}

// run create container failed should clean up the container
func (s *DockerSuite) TestRunCreateContainerFailedCleanUp(c *check.C) {
	// TODO Windows. This may be possible to enable once link is supported
	testRequires(c, DaemonIsLinux)
	name := "unique_name"
	_, _, err := dockerCmdWithError("run", "--name", name, "--link", "nothing:nothing", "busybox")
	c.Assert(err, check.NotNil, check.Commentf("Expected docker run to fail!"))

	containerID, err := inspectField(name, "Id")
	c.Assert(containerID, check.Equals, "", check.Commentf("Expected not to have this container: %s!", containerID))
}

func (s *DockerSuite) TestRunNamedVolume(c *check.C) {
	// TODO Windows: This may be possible to modify once Windows supports volumes
	testRequires(c, DaemonIsLinux)
	dockerCmd(c, "run", "--name=test", "-v", "testing:/foo", "busybox", "sh", "-c", "echo hello > /foo/bar")

	out, _ := dockerCmd(c, "run", "--volumes-from", "test", "busybox", "sh", "-c", "cat /foo/bar")
	c.Assert(strings.TrimSpace(out), check.Equals, "hello")

	out, _ = dockerCmd(c, "run", "-v", "testing:/foo", "busybox", "sh", "-c", "cat /foo/bar")
	c.Assert(strings.TrimSpace(out), check.Equals, "hello")
}

func (s *DockerSuite) TestRunWithUlimits(c *check.C) {
	// Not applicable on Windows as uses Unix specific functionality
	testRequires(c, DaemonIsLinux, NativeExecDriver)

	out, _ := dockerCmd(c, "run", "--name=testulimits", "--ulimit", "nofile=42", "busybox", "/bin/sh", "-c", "ulimit -n")
	ul := strings.TrimSpace(out)
	c.Assert(ul, checker.Equals, "42", check.Commentf("expected `ulimit -n` to be 42"))
}

func (s *DockerSuite) TestRunContainerWithCgroupParent(c *check.C) {
	// Not applicable on Windows as uses Unix specific functionality
	testRequires(c, DaemonIsLinux, NativeExecDriver)

	cgroupParent := "test"
	name := "cgroup-test"

	out, _ := dockerCmd(c, "run", "--cgroup-parent", cgroupParent, "--name", name, "busybox", "cat", "/proc/self/cgroup")
	cgroupPaths := parseCgroupPaths(string(out))
	c.Assert(cgroupPaths, checker.Not(checker.HasLen), 0)

	id, err := getIDByName(name)
	c.Assert(err, checker.IsNil)
	expectedCgroup := path.Join(cgroupParent, id)
	found := false
	for _, path := range cgroupPaths {
		if strings.HasSuffix(path, expectedCgroup) {
			found = true
			break
		}
	}
	c.Assert(found, checker.True, check.Commentf("unexpected cgroup paths. Expected at least one cgroup path to have suffix %q. Cgroup Paths: %v", expectedCgroup, cgroupPaths))
}

func (s *DockerSuite) TestRunContainerWithCgroupParentAbsPath(c *check.C) {
	// Not applicable on Windows as uses Unix specific functionality
	testRequires(c, DaemonIsLinux, NativeExecDriver)

	cgroupParent := "/cgroup-parent/test"
	name := "cgroup-test"
	out, _ := dockerCmd(c, "run", "--cgroup-parent", cgroupParent, "--name", name, "busybox", "cat", "/proc/self/cgroup")
	cgroupPaths := parseCgroupPaths(string(out))
	c.Assert(cgroupPaths, checker.Not(checker.HasLen), 0)

	id, err := getIDByName(name)
	c.Assert(err, checker.IsNil)
	expectedCgroup := path.Join(cgroupParent, id)
	found := false
	for _, path := range cgroupPaths {
		if strings.HasSuffix(path, expectedCgroup) {
			found = true
			break
		}
	}
	c.Assert(found, checker.True, check.Commentf("unexpected cgroup paths. Expected at least one cgroup path to have suffix %q. Cgroup Paths: %v", expectedCgroup, cgroupPaths))
}

func (s *DockerSuite) TestRunContainerWithCgroupMountRO(c *check.C) {
	// Not applicable on Windows as uses Unix specific functionality
	// --read-only + userns has remount issues
	testRequires(c, DaemonIsLinux, NativeExecDriver, NotUserNamespace)

	filename := "/sys/fs/cgroup/devices/test123"
	out, _, err := dockerCmdWithError("run", "busybox", "touch", filename)
	c.Assert(err, checker.NotNil, check.Commentf("expected cgroup mount point to be read-only, touch file should fail"))

	expected := "Read-only file system"
	c.Assert(out, checker.Contains, expected)
}

func (s *DockerSuite) TestRunContainerNetworkModeToSelf(c *check.C) {
	// Not applicable on Windows which does not support --net=container
	testRequires(c, DaemonIsLinux, NotUserNamespace)
	out, _, err := dockerCmdWithError("run", "--name=me", "--net=container:me", "busybox", "true")
	c.Assert(err, checker.NotNil, check.Commentf("using container net mode to self should result in an error"))
	c.Assert(out, checker.Contains, "cannot join own network", check.Commentf("using container net mode to self should result in an error"))
}

func (s *DockerSuite) TestRunContainerNetModeWithDnsMacHosts(c *check.C) {
	// Not applicable on Windows which does not support --net=container
	testRequires(c, DaemonIsLinux, NotUserNamespace)
	out, _ := dockerCmd(c, "run", "-d", "--name", "parent", "busybox", "top")

	out, _, err := dockerCmdWithError("run", "--dns", "1.2.3.4", "--net=container:parent", "busybox")
	c.Assert(err, checker.NotNil, check.Commentf("run --net=container with --dns should error out"))
	c.Assert(out, checker.Contains, "Conflicting options: --dns and the network mode", check.Commentf("run --net=container with --dns should error out"))

	out, _, err = dockerCmdWithError("run", "--mac-address", "92:d0:c6:0a:29:33", "--net=container:parent", "busybox")
	c.Assert(err, checker.NotNil, check.Commentf("run --net=container with --mac-address should error out"))
	c.Assert(out, checker.Contains, "--mac-address and the network mode", check.Commentf("run --net=container with --mac-address should error out"))

	out, _, err = dockerCmdWithError("run", "--add-host", "test:192.168.2.109", "--net=container:parent", "busybox")
	c.Assert(err, checker.NotNil, check.Commentf("run --net=container with --add-host should error out"))
	c.Assert(out, checker.Contains, "--add-host and the network mode", check.Commentf("run --net=container with --add-host should error out"))
}

func (s *DockerSuite) TestRunContainerNetModeWithExposePort(c *check.C) {
	// Not applicable on Windows which does not support --net=container
	testRequires(c, DaemonIsLinux, NotUserNamespace)
	dockerCmd(c, "run", "-d", "--name", "parent", "busybox", "top")

	out, _, err := dockerCmdWithError("run", "-p", "5000:5000", "--net=container:parent", "busybox")
	c.Assert(err, checker.NotNil, check.Commentf("run --net=container with -p should error out"))
	c.Assert(out, checker.Contains, "Conflicting options: -p, -P, --publish-all, --publish and the network mode (--net)", check.Commentf("run --net=container with -p should error out"))

	out, _, err = dockerCmdWithError("run", "-P", "--net=container:parent", "busybox")
	c.Assert(err, checker.NotNil, check.Commentf("run --net=container with -P should error out"))
	c.Assert(out, checker.Contains, "Conflicting options: -p, -P, --publish-all, --publish and the network mode (--net)", check.Commentf("run --net=container with -P should error out"))

	out, _, err = dockerCmdWithError("run", "--expose", "5000", "--net=container:parent", "busybox")
	c.Assert(err, checker.NotNil, check.Commentf("run --net=container with --expose should error out"))
	c.Assert(out, checker.Contains, "Conflicting options: --expose and the network mode (--expose)", check.Commentf("run --net=container with --expose should error out"))
}

func (s *DockerSuite) TestRunLinkToContainerNetMode(c *check.C) {
	// Not applicable on Windows which does not support --net=container or --link
	testRequires(c, DaemonIsLinux, NotUserNamespace)
	dockerCmd(c, "run", "--name", "test", "-d", "busybox", "top")
	dockerCmd(c, "run", "--name", "parent", "-d", "--net=container:test", "busybox", "top")
	dockerCmd(c, "run", "-d", "--link=parent:parent", "busybox", "top")
	dockerCmd(c, "run", "--name", "child", "-d", "--net=container:parent", "busybox", "top")
	dockerCmd(c, "run", "-d", "--link=child:child", "busybox", "top")
}

func (s *DockerSuite) TestRunLoopbackOnlyExistsWhenNetworkingDisabled(c *check.C) {
	// TODO Windows: This may be possible to convert.
	testRequires(c, DaemonIsLinux)
	out, _ := dockerCmd(c, "run", "--net=none", "busybox", "ip", "-o", "-4", "a", "show", "up")

	var (
		count = 0
		parts = strings.Split(out, "\n")
	)

	for _, l := range parts {
		if l != "" {
			count++
		}
	}

	c.Assert(count, checker.Equals, 1, check.Commentf("Wrong interface count in container"))
	c.Assert(out, checker.HasPrefix, "1: lo", check.Commentf("Wrong interface in test container: expected [1: lo]"))
}

// Issue #4681
func (s *DockerSuite) TestRunLoopbackWhenNetworkDisabled(c *check.C) {
	if daemonPlatform == "windows" {
		dockerCmd(c, "run", "--net=none", WindowsBaseImage, "ping", "-n", "1", "127.0.0.1")
	} else {
		dockerCmd(c, "run", "--net=none", "busybox", "ping", "-c", "1", "127.0.0.1")
	}
}

func (s *DockerSuite) TestRunModeNetContainerHostname(c *check.C) {
	// Windows does not support --net=container
	testRequires(c, DaemonIsLinux, ExecSupport, NotUserNamespace)

	dockerCmd(c, "run", "-i", "-d", "--name", "parent", "busybox", "top")
	out, _ := dockerCmd(c, "exec", "parent", "cat", "/etc/hostname")
	out1, _ := dockerCmd(c, "run", "--net=container:parent", "busybox", "cat", "/etc/hostname")

	c.Assert(out, checker.Equals, out1, check.Commentf("containers with shared net namespace should have same hostname"))
}

func (s *DockerSuite) TestRunNetworkNotInitializedNoneMode(c *check.C) {
	// TODO Windows: Network settings are not currently propagated. This may
	// be resolved in the future with the move to libnetwork and CNM.
	testRequires(c, DaemonIsLinux)
	out, _ := dockerCmd(c, "run", "-d", "--net=none", "busybox", "top")
	id := strings.TrimSpace(out)
	res, err := inspectField(id, "NetworkSettings.IPAddress")
	c.Assert(err, checker.IsNil)
	c.Assert(res, checker.Equals, "", check.Commentf("For 'none' mode network must not be initialized"))
}

func (s *DockerSuite) TestTwoContainersInNetHost(c *check.C) {
	// Not applicable as Windows does not support --net=host
	testRequires(c, DaemonIsLinux, NotUserNamespace, NotUserNamespace)
	dockerCmd(c, "run", "-d", "--net=host", "--name=first", "busybox", "top")
	dockerCmd(c, "run", "-d", "--net=host", "--name=second", "busybox", "top")
	dockerCmd(c, "stop", "first")
	dockerCmd(c, "stop", "second")
}

func (s *DockerSuite) TestContainersInUserDefinedNetwork(c *check.C) {
	testRequires(c, DaemonIsLinux, NotUserNamespace)
	dockerCmd(c, "network", "create", "-d", "bridge", "testnetwork")
	dockerCmd(c, "run", "-d", "--net=testnetwork", "--name=first", "busybox", "top")
	c.Assert(waitRun("first"), checker.IsNil)
	dockerCmd(c, "run", "-t", "--net=testnetwork", "--name=second", "busybox", "ping", "-c", "1", "first")
}

func (s *DockerSuite) TestContainersInMultipleNetworks(c *check.C) {
	testRequires(c, DaemonIsLinux, NotUserNamespace, NativeExecDriver)
	// Create 2 networks using bridge driver
	dockerCmd(c, "network", "create", "-d", "bridge", "testnetwork1")
	dockerCmd(c, "network", "create", "-d", "bridge", "testnetwork2")
	// Run and connect containers to testnetwork1
	dockerCmd(c, "run", "-d", "--net=testnetwork1", "--name=first", "busybox", "top")
	c.Assert(waitRun("first"), checker.IsNil)
	dockerCmd(c, "run", "-d", "--net=testnetwork1", "--name=second", "busybox", "top")
	c.Assert(waitRun("second"), checker.IsNil)
	// Check connectivity between containers in testnetwork2
	dockerCmd(c, "exec", "first", "ping", "-c", "1", "second.testnetwork1")
	// Connect containers to testnetwork2
	dockerCmd(c, "network", "connect", "testnetwork2", "first")
	dockerCmd(c, "network", "connect", "testnetwork2", "second")
	// Check connectivity between containers
	dockerCmd(c, "exec", "second", "ping", "-c", "1", "first.testnetwork2")
}

func (s *DockerSuite) TestContainersNetworkIsolation(c *check.C) {
	testRequires(c, DaemonIsLinux, NotUserNamespace, NativeExecDriver)
	// Create 2 networks using bridge driver
	dockerCmd(c, "network", "create", "-d", "bridge", "testnetwork1")
	dockerCmd(c, "network", "create", "-d", "bridge", "testnetwork2")
	// Run 1 container in testnetwork1 and another in testnetwork2
	dockerCmd(c, "run", "-d", "--net=testnetwork1", "--name=first", "busybox", "top")
	c.Assert(waitRun("first"), checker.IsNil)
	dockerCmd(c, "run", "-d", "--net=testnetwork2", "--name=second", "busybox", "top")
	c.Assert(waitRun("second"), checker.IsNil)

	// Check Isolation between containers : ping must fail
	_, _, err := dockerCmdWithError("exec", "first", "ping", "-c", "1", "second")
	c.Assert(err, check.NotNil)
	// Connect first container to testnetwork2
	dockerCmd(c, "network", "connect", "testnetwork2", "first")
	// ping must succeed now
	_, _, err = dockerCmdWithError("exec", "first", "ping", "-c", "1", "second")
	c.Assert(err, checker.IsNil)

	// Disconnect first container from testnetwork2
	dockerCmd(c, "network", "disconnect", "testnetwork2", "first")
	// ping must fail again
	_, _, err = dockerCmdWithError("exec", "first", "ping", "-c", "1", "second")
	c.Assert(err, check.NotNil)
}

func (s *DockerSuite) TestNetworkRmWithActiveContainers(c *check.C) {
	testRequires(c, DaemonIsLinux, NotUserNamespace)
	// Create 2 networks using bridge driver
	dockerCmd(c, "network", "create", "-d", "bridge", "testnetwork1")
	// Run and connect containers to testnetwork1
	dockerCmd(c, "run", "-d", "--net=testnetwork1", "--name=first", "busybox", "top")
	c.Assert(waitRun("first"), checker.IsNil)
	dockerCmd(c, "run", "-d", "--net=testnetwork1", "--name=second", "busybox", "top")
	c.Assert(waitRun("second"), checker.IsNil)
	// Network delete with active containers must fail
	_, _, err := dockerCmdWithError("network", "rm", "testnetwork1")
	c.Assert(err, check.NotNil)

	dockerCmd(c, "stop", "first")
	_, _, err = dockerCmdWithError("network", "rm", "testnetwork1")
	c.Assert(err, check.NotNil)
}

func (s *DockerSuite) TestContainerRestartInMultipleNetworks(c *check.C) {
	testRequires(c, DaemonIsLinux, NotUserNamespace, NativeExecDriver)
	// Create 2 networks using bridge driver
	dockerCmd(c, "network", "create", "-d", "bridge", "testnetwork1")
	dockerCmd(c, "network", "create", "-d", "bridge", "testnetwork2")

	// Run and connect containers to testnetwork1
	dockerCmd(c, "run", "-d", "--net=testnetwork1", "--name=first", "busybox", "top")
	c.Assert(waitRun("first"), checker.IsNil)
	dockerCmd(c, "run", "-d", "--net=testnetwork1", "--name=second", "busybox", "top")
	c.Assert(waitRun("second"), checker.IsNil)
	// Check connectivity between containers in testnetwork2
	dockerCmd(c, "exec", "first", "ping", "-c", "1", "second.testnetwork1")
	// Connect containers to testnetwork2
	dockerCmd(c, "network", "connect", "testnetwork2", "first")
	dockerCmd(c, "network", "connect", "testnetwork2", "second")
	// Check connectivity between containers
	dockerCmd(c, "exec", "second", "ping", "-c", "1", "first.testnetwork2")

	// Stop second container and test ping failures on both networks
	dockerCmd(c, "stop", "second")
	_, _, err := dockerCmdWithError("exec", "first", "ping", "-c", "1", "second.testnetwork1")
	c.Assert(err, check.NotNil)
	_, _, err = dockerCmdWithError("exec", "first", "ping", "-c", "1", "second.testnetwork2")
	c.Assert(err, check.NotNil)

	// Start second container and connectivity must be restored on both networks
	dockerCmd(c, "start", "second")
	dockerCmd(c, "exec", "first", "ping", "-c", "1", "second.testnetwork1")
	dockerCmd(c, "exec", "second", "ping", "-c", "1", "first.testnetwork2")
}

func (s *DockerSuite) TestContainerWithConflictingHostNetworks(c *check.C) {
	testRequires(c, DaemonIsLinux, NotUserNamespace)
	// Run a container with --net=host
	dockerCmd(c, "run", "-d", "--net=host", "--name=first", "busybox", "top")
	c.Assert(waitRun("first"), checker.IsNil)

	// Create a network using bridge driver
	dockerCmd(c, "network", "create", "-d", "bridge", "testnetwork1")

	// Connecting to the user defined network must fail
	_, _, err := dockerCmdWithError("network", "connect", "testnetwork1", "first")
	c.Assert(err, check.NotNil)
}

func (s *DockerSuite) TestContainerWithConflictingSharedNetwork(c *check.C) {
	testRequires(c, DaemonIsLinux, NotUserNamespace)
	dockerCmd(c, "run", "-d", "--name=first", "busybox", "top")
	c.Assert(waitRun("first"), checker.IsNil)
	// Run second container in first container's network namespace
	dockerCmd(c, "run", "-d", "--net=container:first", "--name=second", "busybox", "top")
	c.Assert(waitRun("second"), checker.IsNil)

	// Create a network using bridge driver
	dockerCmd(c, "network", "create", "-d", "bridge", "testnetwork1")

	// Connecting to the user defined network must fail
	out, _, err := dockerCmdWithError("network", "connect", "testnetwork1", "second")
	c.Assert(err, check.NotNil)
	c.Assert(out, checker.Contains, runconfig.ErrConflictSharedNetwork.Error())
}

func (s *DockerSuite) TestContainerWithConflictingNoneNetwork(c *check.C) {
	testRequires(c, DaemonIsLinux, NotUserNamespace)
	dockerCmd(c, "run", "-d", "--net=none", "--name=first", "busybox", "top")
	c.Assert(waitRun("first"), checker.IsNil)

	// Create a network using bridge driver
	dockerCmd(c, "network", "create", "-d", "bridge", "testnetwork1")

	// Connecting to the user defined network must fail
	out, _, err := dockerCmdWithError("network", "connect", "testnetwork1", "first")
	c.Assert(err, check.NotNil)
	c.Assert(out, checker.Contains, runconfig.ErrConflictNoNetwork.Error())

	// create a container connected to testnetwork1
	dockerCmd(c, "run", "-d", "--net=testnetwork1", "--name=second", "busybox", "top")
	c.Assert(waitRun("second"), checker.IsNil)

	// Connect second container to none network. it must fail as well
	_, _, err = dockerCmdWithError("network", "connect", "none", "second")
	c.Assert(err, check.NotNil)
}

// #11957 - stdin with no tty does not exit if stdin is not closed even though container exited
func (s *DockerSuite) TestRunStdinBlockedAfterContainerExit(c *check.C) {
	cmd := exec.Command(dockerBinary, "run", "-i", "--name=test", "busybox", "true")
	in, err := cmd.StdinPipe()
	c.Assert(err, checker.IsNil)
	defer in.Close()
	c.Assert(cmd.Start(), checker.IsNil)

	waitChan := make(chan error)
	go func() {
		waitChan <- cmd.Wait()
	}()

	select {
	case err := <-waitChan:
		c.Assert(err, checker.IsNil)
	case <-time.After(30 * time.Second):
		c.Fatal("timeout waiting for command to exit")
	}
}

func (s *DockerSuite) TestRunWrongCpusetCpusFlagValue(c *check.C) {
	out, _, err := dockerCmdWithError("run", "--cpuset-cpus", "1-10,11--", "busybox", "true")
	c.Assert(err, check.NotNil)
	expected := "Error response from daemon: Invalid value 1-10,11-- for cpuset cpus.\n"
	c.Assert(out, check.Equals, expected, check.Commentf("Expected output to contain %q, got %q", expected, out))
}

func (s *DockerSuite) TestRunWrongCpusetMemsFlagValue(c *check.C) {
	out, _, err := dockerCmdWithError("run", "--cpuset-mems", "1-42--", "busybox", "true")
	c.Assert(err, check.NotNil)
	expected := "Error response from daemon: Invalid value 1-42-- for cpuset mems.\n"
	c.Assert(out, check.Equals, expected, check.Commentf("Expected output to contain %q, got %q", expected, out))
}
