package integration

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-check/check"
)

// Daemon represents a Docker daemon for the testing framework.
type Daemon struct {
	dockerBinary string
	// Defaults to "daemon"
	// Useful to set to --daemon or -d for checking backwards compatibility
	Command     string
	GlobalFlags []string

	id             string
	c              *check.C
	LogFile        *os.File
	Folder         string
	stdin          io.WriteCloser
	stdout, stderr io.ReadCloser
	DaemonCmd      *exec.Cmd
	storageDriver  string
	execDriver     string
	wait           chan error
	userlandProxy  bool
}

// NewDaemon returns a Daemon instance to be used for testing.
// This will create a directory such as d123456789 in the folder specified by $DEST.
// The daemon will not automatically start.
func NewDaemon(c *check.C, dockerBinary string) *Daemon {
	dest := os.Getenv("DEST")
	if dest == "" {
		c.Fatal("Please set the DEST environment variable")
	}

	id := fmt.Sprintf("d%d", time.Now().UnixNano()%100000000)
	dir := filepath.Join(dest, id)
	daemonFolder, err := filepath.Abs(dir)
	if err != nil {
		c.Fatalf("Could not make %q an absolute path: %v", dir, err)
	}

	if err := os.MkdirAll(filepath.Join(daemonFolder, "graph"), 0600); err != nil {
		c.Fatalf("Could not create %s/graph directory", daemonFolder)
	}

	userlandProxy := false
	if env := os.Getenv("DOCKER_USERLANDPROXY"); env != "" {
		if val, err := strconv.ParseBool(env); err != nil {
			userlandProxy = val
		}
	}

	return &Daemon{
		Command:       "daemon",
		id:            id,
		c:             c,
		Folder:        daemonFolder,
		storageDriver: os.Getenv("DOCKER_GRAPHDRIVER"),
		execDriver:    os.Getenv("DOCKER_EXECDRIVER"),
		userlandProxy: userlandProxy,
	}
}

// Start will start the daemon and return once it is ready to receive requests.
// You can specify additional daemon flags.
func (d *Daemon) Start(arg ...string) error {
	_, err := exec.LookPath(d.dockerBinary)
	if err != nil {
		d.c.Fatalf("[%s] could not find docker binary in $PATH: %v", d.id, err)
	}

	args := append(d.GlobalFlags,
		d.Command,
		"--host", d.Sock(),
		"--graph", fmt.Sprintf("%s/graph", d.Folder),
		"--pidfile", fmt.Sprintf("%s/docker.pid", d.Folder),
		fmt.Sprintf("--userland-proxy=%t", d.userlandProxy),
	)

	// If we don't explicitly set the log-level or debug flag(-D) then
	// turn on debug mode
	foundIt := false
	for _, a := range arg {
		if strings.Contains(a, "--log-level") || strings.Contains(a, "-D") || strings.Contains(a, "--debug") {
			foundIt = true
		}
	}
	if !foundIt {
		args = append(args, "--debug")
	}

	if d.storageDriver != "" {
		args = append(args, "--storage-driver", d.storageDriver)
	}
	if d.execDriver != "" {
		args = append(args, "--exec-driver", d.execDriver)
	}

	args = append(args, arg...)
	d.DaemonCmd = exec.Command(d.dockerBinary, args...)

	d.LogFile, err = os.OpenFile(filepath.Join(d.Folder, "docker.log"), os.O_RDWR|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		d.c.Fatalf("[%s] Could not create %s/docker.log: %v", d.id, d.Folder, err)
	}

	d.DaemonCmd.Stdout = d.LogFile
	d.DaemonCmd.Stderr = d.LogFile

	if err := d.DaemonCmd.Start(); err != nil {
		return fmt.Errorf("[%s] could not start daemon container: %v", d.id, err)
	}

	wait := make(chan error)

	go func() {
		wait <- d.DaemonCmd.Wait()
		d.c.Logf("[%s] exiting daemon", d.id)
		close(wait)
	}()

	d.wait = wait

	tick := time.Tick(500 * time.Millisecond)
	// make sure daemon is ready to receive requests
	startTime := time.Now().Unix()
	for {
		d.c.Logf("[%s] waiting for daemon to start", d.id)
		if time.Now().Unix()-startTime > 5 {
			// After 5 seconds, give up
			return fmt.Errorf("[%s] Daemon exited and never started", d.id)
		}
		select {
		case <-time.After(2 * time.Second):
			return fmt.Errorf("[%s] timeout: daemon does not respond", d.id)
		case <-tick:
			c, err := net.Dial("unix", filepath.Join(d.Folder, "docker.Sock"))
			if err != nil {
				continue
			}

			client := httputil.NewClientConn(c, nil)
			defer client.Close()

			req, err := http.NewRequest("GET", "/_ping", nil)
			if err != nil {
				d.c.Fatalf("[%s] could not create new request: %v", d.id, err)
			}

			resp, err := client.Do(req)
			if err != nil {
				continue
			}
			if resp.StatusCode != http.StatusOK {
				d.c.Logf("[%s] received status != 200 OK: %s", d.id, resp.Status)
			}

			d.c.Logf("[%s] daemon started", d.id)
			return nil
		}
	}
}

// StartWithBusybox will first start the daemon with Daemon.Start()
// then save the busybox image from the main daemon and load it into this Daemon instance.
func (d *Daemon) StartWithBusybox(arg ...string) error {
	if err := d.Start(arg...); err != nil {
		return err
	}
	bb := filepath.Join(d.Folder, "busybox.tar")
	if _, err := os.Stat(bb); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("unexpected error on busybox.tar stat: %v", err)
		}
		// saving busybox image from main daemon
		if err := exec.Command(d.dockerBinary, "save", "--output", bb, "busybox:latest").Run(); err != nil {
			return fmt.Errorf("could not save busybox image: %v", err)
		}
	}
	// loading busybox image to this daemon
	if _, err := d.Cmd("load", "--input", bb); err != nil {
		return fmt.Errorf("could not load busybox image: %v", err)
	}
	if err := os.Remove(bb); err != nil {
		d.c.Logf("Could not remove %s: %v", bb, err)
	}
	return nil
}

// Stop will send a SIGINT every second and wait for the daemon to stop.
// If it timeouts, a SIGKILL is sent.
// Stop will not delete the daemon directory. If a purged daemon is needed,
// instantiate a new one with NewDaemon.
func (d *Daemon) Stop() error {
	if d.DaemonCmd == nil || d.wait == nil {
		return errors.New("daemon not started")
	}

	defer func() {
		d.LogFile.Close()
		d.DaemonCmd = nil
	}()

	i := 1
	tick := time.Tick(time.Second)

	if err := d.DaemonCmd.Process.Signal(os.Interrupt); err != nil {
		return fmt.Errorf("could not send signal: %v", err)
	}
out1:
	for {
		select {
		case err := <-d.wait:
			return err
		case <-time.After(15 * time.Second):
			// time for stopping jobs and run onShutdown hooks
			d.c.Log("timeout")
			break out1
		}
	}

out2:
	for {
		select {
		case err := <-d.wait:
			return err
		case <-tick:
			i++
			if i > 4 {
				d.c.Logf("tried to interrupt daemon for %d times, now try to kill it", i)
				break out2
			}
			d.c.Logf("Attempt #%d: daemon is still running with pid %d", i, d.DaemonCmd.Process.Pid)
			if err := d.DaemonCmd.Process.Signal(os.Interrupt); err != nil {
				return fmt.Errorf("could not send signal: %v", err)
			}
		}
	}

	if err := d.DaemonCmd.Process.Kill(); err != nil {
		d.c.Logf("Could not kill daemon: %v", err)
		return err
	}

	return nil
}

// Restart will restart the daemon by first stopping it and then starting it.
func (d *Daemon) Restart(arg ...string) error {
	d.Stop()
	return d.Start(arg...)
}

func (d *Daemon) Sock() string {
	return fmt.Sprintf("unix://%s/docker.sock", d.Folder)
}

// Cmd will execute a docker CLI command against this Daemon.
// Example: d.Cmd("version") will run docker -H unix://path/to/unix.sock version
func (d *Daemon) Cmd(name string, arg ...string) (string, error) {
	args := []string{"--host", d.Sock(), name}
	args = append(args, arg...)
	c := exec.Command(d.dockerBinary, args...)
	b, err := c.CombinedOutput()
	return string(b), err
}

// CmdWithArgs will execute a docker CLI command against a daemon with the
// given additional arguments
func (d *Daemon) CmdWithArgs(daemonArgs []string, name string, arg ...string) (string, error) {
	args := append(daemonArgs, name)
	args = append(args, arg...)
	c := exec.Command(d.dockerBinary, args...)
	b, err := c.CombinedOutput()
	return string(b), err
}

// LogfileName returns the path the the daemon's log file
func (d *Daemon) LogfileName() string {
	return d.LogFile.Name()
}

func FindContainerIP(c *check.C, dockerBinary, id string, vargs ...string) string {
	args := append(vargs, "inspect", "--format='{{ .NetworkSettings.IPAddress }}'", id)
	cmd := exec.Command(dockerBinary, args...)
	out, _, err := RunCommandWithOutput(cmd)
	if err != nil {
		c.Fatal(err, out)
	}

	return strings.Trim(out, " \r\n'")
}

func (d *Daemon) FindContainerIP(id string) string {
	return FindContainerIP(d.c, d.dockerBinary, id, "--host", d.Sock())
}
