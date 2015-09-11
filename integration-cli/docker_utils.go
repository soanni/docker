package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/opts"
	"github.com/docker/docker/pkg/httputils"
	"github.com/docker/docker/pkg/integration"
	"github.com/docker/docker/pkg/ioutils"
	"github.com/docker/docker/pkg/stringutils"
	"github.com/go-check/check"
)

type Daemon struct {
	integration.Daemon
}

func NewDaemon(c *check.C) *Daemon {
	daemon := integration.NewDaemon(c, dockerBinary)
	return &Daemon{
		*daemon,
	}
}

func daemonHost() string {
	daemonURLStr := "unix://" + opts.DefaultUnixSocket
	if daemonHostVar := os.Getenv("DOCKER_HOST"); daemonHostVar != "" {
		daemonURLStr = daemonHostVar
	}
	return daemonURLStr
}

func sockConn(timeout time.Duration) (net.Conn, error) {
	daemon := daemonHost()
	daemonURL, err := url.Parse(daemon)
	if err != nil {
		return nil, fmt.Errorf("could not parse url %q: %v", daemon, err)
	}

	var c net.Conn
	switch daemonURL.Scheme {
	case "unix":
		return net.DialTimeout(daemonURL.Scheme, daemonURL.Path, timeout)
	case "tcp":
		return net.DialTimeout(daemonURL.Scheme, daemonURL.Host, timeout)
	default:
		return c, fmt.Errorf("unknown scheme %v (%s)", daemonURL.Scheme, daemon)
	}
}

func sockRequest(method, endpoint string, data interface{}) (int, []byte, error) {
	jsonData := bytes.NewBuffer(nil)
	if err := json.NewEncoder(jsonData).Encode(data); err != nil {
		return -1, nil, err
	}

	res, body, err := sockRequestRaw(method, endpoint, jsonData, "application/json")
	if err != nil {
		return -1, nil, err
	}
	b, err := readBody(body)
	return res.StatusCode, b, err
}

func sockRequestRaw(method, endpoint string, data io.Reader, ct string) (*http.Response, io.ReadCloser, error) {
	req, client, err := newRequestClient(method, endpoint, data, ct)
	if err != nil {
		return nil, nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		client.Close()
		return nil, nil, err
	}
	body := ioutils.NewReadCloserWrapper(resp.Body, func() error {
		defer resp.Body.Close()
		return client.Close()
	})

	return resp, body, nil
}

func sockRequestHijack(method, endpoint string, data io.Reader, ct string) (net.Conn, *bufio.Reader, error) {
	req, client, err := newRequestClient(method, endpoint, data, ct)
	if err != nil {
		return nil, nil, err
	}

	client.Do(req)
	conn, br := client.Hijack()
	return conn, br, nil
}

func newRequestClient(method, endpoint string, data io.Reader, ct string) (*http.Request, *httputil.ClientConn, error) {
	c, err := sockConn(time.Duration(10 * time.Second))
	if err != nil {
		return nil, nil, fmt.Errorf("could not dial docker daemon: %v", err)
	}

	client := httputil.NewClientConn(c, nil)

	req, err := http.NewRequest(method, endpoint, data)
	if err != nil {
		client.Close()
		return nil, nil, fmt.Errorf("could not create new request: %v", err)
	}

	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	return req, client, nil
}

func readBody(b io.ReadCloser) ([]byte, error) {
	defer b.Close()
	return ioutil.ReadAll(b)
}

func deleteContainer(container string) error {
	container = strings.TrimSpace(strings.Replace(container, "\n", " ", -1))
	rmArgs := strings.Split(fmt.Sprintf("rm -fv %v", container), " ")
	exitCode, err := runCommand(exec.Command(dockerBinary, rmArgs...))
	// set error manually if not set
	if exitCode != 0 && err == nil {
		err = fmt.Errorf("failed to remove container: `docker rm` exit is non-zero")
	}

	return err
}

func getAllContainers() (string, error) {
	getContainersCmd := exec.Command(dockerBinary, "ps", "-q", "-a")
	out, exitCode, err := runCommandWithOutput(getContainersCmd)
	if exitCode != 0 && err == nil {
		err = fmt.Errorf("failed to get a list of containers: %v\n", out)
	}

	return out, err
}

func deleteAllContainers() error {
	containers, err := getAllContainers()
	if err != nil {
		fmt.Println(containers)
		return err
	}

	if err = deleteContainer(containers); err != nil {
		return err
	}
	return nil
}

func deleteAllVolumes() error {
	volumes, err := getAllVolumes()
	if err != nil {
		return err
	}
	var errors []string
	for _, v := range volumes {
		status, b, err := sockRequest("DELETE", "/volumes/"+v.Name, nil)
		if err != nil {
			errors = append(errors, err.Error())
			continue
		}
		if status != http.StatusNoContent {
			errors = append(errors, fmt.Sprintf("error deleting volume %s: %s", v.Name, string(b)))
		}
	}
	if len(errors) > 0 {
		return fmt.Errorf(strings.Join(errors, "\n"))
	}
	return nil
}

func getAllVolumes() ([]*types.Volume, error) {
	var volumes types.VolumesListResponse
	_, b, err := sockRequest("GET", "/volumes", nil)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &volumes); err != nil {
		return nil, err
	}
	return volumes.Volumes, nil
}

var protectedImages = map[string]struct{}{}

func init() {
	out, err := exec.Command(dockerBinary, "images").CombinedOutput()
	if err != nil {
		panic(err)
	}
	lines := strings.Split(string(out), "\n")[1:]
	for _, l := range lines {
		if l == "" {
			continue
		}
		fields := strings.Fields(l)
		imgTag := fields[0] + ":" + fields[1]
		// just for case if we have dangling images in tested daemon
		if imgTag != "<none>:<none>" {
			protectedImages[imgTag] = struct{}{}
		}
	}

	// Obtain the daemon platform so that it can be used by tests to make
	// intelligent decisions about how to configure themselves, and validate
	// that the target platform is valid.
	res, _, err := sockRequestRaw("GET", "/version", nil, "application/json")
	if err != nil || res == nil || (res != nil && res.StatusCode != http.StatusOK) {
		panic(fmt.Errorf("Init failed to get version: %v. Res=%v", err.Error(), res))
	}
	svrHeader, _ := httputils.ParseServerHeader(res.Header.Get("Server"))
	daemonPlatform = svrHeader.OS
	if daemonPlatform != "linux" && daemonPlatform != "windows" {
		panic("Cannot run tests against platform: " + daemonPlatform)
	}
}

func deleteAllImages() error {
	out, err := exec.Command(dockerBinary, "images").CombinedOutput()
	if err != nil {
		return err
	}
	lines := strings.Split(string(out), "\n")[1:]
	var imgs []string
	for _, l := range lines {
		if l == "" {
			continue
		}
		fields := strings.Fields(l)
		imgTag := fields[0] + ":" + fields[1]
		if _, ok := protectedImages[imgTag]; !ok {
			if fields[0] == "<none>" {
				imgs = append(imgs, fields[2])
				continue
			}
			imgs = append(imgs, imgTag)
		}
	}
	if len(imgs) == 0 {
		return nil
	}
	args := append([]string{"rmi", "-f"}, imgs...)
	if err := exec.Command(dockerBinary, args...).Run(); err != nil {
		return err
	}
	return nil
}

func getPausedContainers() (string, error) {
	getPausedContainersCmd := exec.Command(dockerBinary, "ps", "-f", "status=paused", "-q", "-a")
	out, exitCode, err := runCommandWithOutput(getPausedContainersCmd)
	if exitCode != 0 && err == nil {
		err = fmt.Errorf("failed to get a list of paused containers: %v\n", out)
	}

	return out, err
}

func getSliceOfPausedContainers() ([]string, error) {
	out, err := getPausedContainers()
	if err == nil {
		if len(out) == 0 {
			return nil, err
		}
		slice := strings.Split(strings.TrimSpace(out), "\n")
		return slice, err
	}
	return []string{out}, err
}

func unpauseContainer(container string) error {
	unpauseCmd := exec.Command(dockerBinary, "unpause", container)
	exitCode, err := runCommand(unpauseCmd)
	if exitCode != 0 && err == nil {
		err = fmt.Errorf("failed to unpause container")
	}

	return nil
}

func unpauseAllContainers() error {
	containers, err := getPausedContainers()
	if err != nil {
		fmt.Println(containers)
		return err
	}

	containers = strings.Replace(containers, "\n", " ", -1)
	containers = strings.Trim(containers, " ")
	containerList := strings.Split(containers, " ")

	for _, value := range containerList {
		if err = unpauseContainer(value); err != nil {
			return err
		}
	}

	return nil
}

func deleteImages(images ...string) error {
	args := []string{"rmi", "-f"}
	args = append(args, images...)
	rmiCmd := exec.Command(dockerBinary, args...)
	exitCode, err := runCommand(rmiCmd)
	// set error manually if not set
	if exitCode != 0 && err == nil {
		err = fmt.Errorf("failed to remove image: `docker rmi` exit is non-zero")
	}
	return err
}

func imageExists(image string) error {
	inspectCmd := exec.Command(dockerBinary, "inspect", image)
	exitCode, err := runCommand(inspectCmd)
	if exitCode != 0 && err == nil {
		err = fmt.Errorf("couldn't find image %q", image)
	}
	return err
}

func pullImageIfNotExist(image string) (err error) {
	if err := imageExists(image); err != nil {
		pullCmd := exec.Command(dockerBinary, "pull", image)
		_, exitCode, err := runCommandWithOutput(pullCmd)

		if err != nil || exitCode != 0 {
			err = fmt.Errorf("image %q wasn't found locally and it couldn't be pulled: %s", image, err)
		}
	}
	return
}

func dockerCmdWithError(args ...string) (string, int, error) {
	return runCommandWithOutput(exec.Command(dockerBinary, args...))
}

func dockerCmdWithStdoutStderr(c *check.C, args ...string) (string, string, int) {
	stdout, stderr, status, err := runCommandWithStdoutStderr(exec.Command(dockerBinary, args...))
	if c != nil {
		c.Assert(err, check.IsNil, check.Commentf("%q failed with errors: %s, %v", strings.Join(args, " "), stderr, err))
	}
	return stdout, stderr, status
}

func dockerCmd(c *check.C, args ...string) (string, int) {
	out, status, err := runCommandWithOutput(exec.Command(dockerBinary, args...))
	c.Assert(err, check.IsNil, check.Commentf("%q failed with errors: %s, %v", strings.Join(args, " "), out, err))
	return out, status
}

// execute a docker command with a timeout
func dockerCmdWithTimeout(timeout time.Duration, args ...string) (string, int, error) {
	out, status, err := runCommandWithOutputAndTimeout(exec.Command(dockerBinary, args...), timeout)
	if err != nil {
		return out, status, fmt.Errorf("%q failed with errors: %v : %q)", strings.Join(args, " "), err, out)
	}
	return out, status, err
}

// execute a docker command in a directory
func dockerCmdInDir(c *check.C, path string, args ...string) (string, int, error) {
	dockerCommand := exec.Command(dockerBinary, args...)
	dockerCommand.Dir = path
	out, status, err := runCommandWithOutput(dockerCommand)
	if err != nil {
		return out, status, fmt.Errorf("%q failed with errors: %v : %q)", strings.Join(args, " "), err, out)
	}
	return out, status, err
}

// execute a docker command in a directory with a timeout
func dockerCmdInDirWithTimeout(timeout time.Duration, path string, args ...string) (string, int, error) {
	dockerCommand := exec.Command(dockerBinary, args...)
	dockerCommand.Dir = path
	out, status, err := runCommandWithOutputAndTimeout(dockerCommand, timeout)
	if err != nil {
		return out, status, fmt.Errorf("%q failed with errors: %v : %q)", strings.Join(args, " "), err, out)
	}
	return out, status, err
}

func getContainerCount() (int, error) {
	const containers = "Containers:"

	cmd := exec.Command(dockerBinary, "info")
	out, _, err := runCommandWithOutput(cmd)
	if err != nil {
		return 0, err
	}

	lines := strings.Split(out, "\n")
	for _, line := range lines {
		if strings.Contains(line, containers) {
			output := strings.TrimSpace(line)
			output = strings.TrimLeft(output, containers)
			output = strings.Trim(output, " ")
			containerCount, err := strconv.Atoi(output)
			if err != nil {
				return 0, err
			}
			return containerCount, nil
		}
	}
	return 0, fmt.Errorf("couldn't find the Container count in the output")
}

// FakeContext creates directories that can be used as a build context
type FakeContext struct {
	Dir string
}

// Add a file at a path, creating directories where necessary
func (f *FakeContext) Add(file, content string) error {
	return f.addFile(file, []byte(content))
}

func (f *FakeContext) addFile(file string, content []byte) error {
	filepath := path.Join(f.Dir, file)
	dirpath := path.Dir(filepath)
	if dirpath != "." {
		if err := os.MkdirAll(dirpath, 0755); err != nil {
			return err
		}
	}
	return ioutil.WriteFile(filepath, content, 0644)

}

// Delete a file at a path
func (f *FakeContext) Delete(file string) error {
	filepath := path.Join(f.Dir, file)
	return os.RemoveAll(filepath)
}

// Close deletes the context
func (f *FakeContext) Close() error {
	return os.RemoveAll(f.Dir)
}

func fakeContextFromNewTempDir() (*FakeContext, error) {
	tmp, err := ioutil.TempDir("", "fake-context")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(tmp, 0755); err != nil {
		return nil, err
	}
	return fakeContextFromDir(tmp), nil
}

func fakeContextFromDir(dir string) *FakeContext {
	return &FakeContext{dir}
}

func fakeContextWithFiles(files map[string]string) (*FakeContext, error) {
	ctx, err := fakeContextFromNewTempDir()
	if err != nil {
		return nil, err
	}
	for file, content := range files {
		if err := ctx.Add(file, content); err != nil {
			ctx.Close()
			return nil, err
		}
	}
	return ctx, nil
}

func fakeContextAddDockerfile(ctx *FakeContext, dockerfile string) error {
	if err := ctx.Add("Dockerfile", dockerfile); err != nil {
		ctx.Close()
		return err
	}
	return nil
}

func fakeContext(dockerfile string, files map[string]string) (*FakeContext, error) {
	ctx, err := fakeContextWithFiles(files)
	if err != nil {
		return nil, err
	}
	if err := fakeContextAddDockerfile(ctx, dockerfile); err != nil {
		return nil, err
	}
	return ctx, nil
}

// FakeStorage is a static file server. It might be running locally or remotely
// on test host.
type FakeStorage interface {
	Close() error
	URL() string
	CtxDir() string
}

func fakeBinaryStorage(archives map[string]*bytes.Buffer) (FakeStorage, error) {
	ctx, err := fakeContextFromNewTempDir()
	if err != nil {
		return nil, err
	}
	for name, content := range archives {
		if err := ctx.addFile(name, content.Bytes()); err != nil {
			return nil, err
		}
	}
	return fakeStorageWithContext(ctx)
}

// fakeStorage returns either a local or remote (at daemon machine) file server
func fakeStorage(files map[string]string) (FakeStorage, error) {
	ctx, err := fakeContextWithFiles(files)
	if err != nil {
		return nil, err
	}
	return fakeStorageWithContext(ctx)
}

// fakeStorageWithContext returns either a local or remote (at daemon machine) file server
func fakeStorageWithContext(ctx *FakeContext) (FakeStorage, error) {
	if isLocalDaemon {
		return newLocalFakeStorage(ctx)
	}
	return newRemoteFileServer(ctx)
}

// localFileStorage is a file storage on the running machine
type localFileStorage struct {
	*FakeContext
	*httptest.Server
}

func (s *localFileStorage) URL() string {
	return s.Server.URL
}

func (s *localFileStorage) CtxDir() string {
	return s.FakeContext.Dir
}

func (s *localFileStorage) Close() error {
	defer s.Server.Close()
	return s.FakeContext.Close()
}

func newLocalFakeStorage(ctx *FakeContext) (*localFileStorage, error) {
	handler := http.FileServer(http.Dir(ctx.Dir))
	server := httptest.NewServer(handler)
	return &localFileStorage{
		FakeContext: ctx,
		Server:      server,
	}, nil
}

// remoteFileServer is a containerized static file server started on the remote
// testing machine to be used in URL-accepting docker build functionality.
type remoteFileServer struct {
	host      string // hostname/port web server is listening to on docker host e.g. 0.0.0.0:43712
	container string
	image     string
	ctx       *FakeContext
}

func (f *remoteFileServer) URL() string {
	u := url.URL{
		Scheme: "http",
		Host:   f.host}
	return u.String()
}

func (f *remoteFileServer) CtxDir() string {
	return f.ctx.Dir
}

func (f *remoteFileServer) Close() error {
	defer func() {
		if f.ctx != nil {
			f.ctx.Close()
		}
		if f.image != "" {
			deleteImages(f.image)
		}
	}()
	if f.container == "" {
		return nil
	}
	return deleteContainer(f.container)
}

func newRemoteFileServer(ctx *FakeContext) (*remoteFileServer, error) {
	var (
		image     = fmt.Sprintf("fileserver-img-%s", strings.ToLower(stringutils.GenerateRandomAlphaOnlyString(10)))
		container = fmt.Sprintf("fileserver-cnt-%s", strings.ToLower(stringutils.GenerateRandomAlphaOnlyString(10)))
	)

	// Build the image
	if err := fakeContextAddDockerfile(ctx, `FROM httpserver
COPY . /static`); err != nil {
		return nil, fmt.Errorf("Cannot add Dockerfile to context: %v", err)
	}
	if _, err := buildImageFromContext(image, ctx, false); err != nil {
		return nil, fmt.Errorf("failed building file storage container image: %v", err)
	}

	// Start the container
	runCmd := exec.Command(dockerBinary, "run", "-d", "-P", "--name", container, image)
	if out, ec, err := runCommandWithOutput(runCmd); err != nil {
		return nil, fmt.Errorf("failed to start file storage container. ec=%v\nout=%s\nerr=%v", ec, out, err)
	}

	// Find out the system assigned port
	out, _, err := runCommandWithOutput(exec.Command(dockerBinary, "port", container, "80/tcp"))
	if err != nil {
		return nil, fmt.Errorf("failed to find container port: err=%v\nout=%s", err, out)
	}

	fileserverHostPort := strings.Trim(out, "\n")
	_, port, err := net.SplitHostPort(fileserverHostPort)
	if err != nil {
		return nil, fmt.Errorf("unable to parse file server host:port: %v", err)
	}

	dockerHostURL, err := url.Parse(daemonHost())
	if err != nil {
		return nil, fmt.Errorf("unable to parse daemon host URL: %v", err)
	}

	host, _, err := net.SplitHostPort(dockerHostURL.Host)
	if err != nil {
		return nil, fmt.Errorf("unable to parse docker daemon host:port: %v", err)
	}

	return &remoteFileServer{
		container: container,
		image:     image,
		host:      fmt.Sprintf("%s:%s", host, port),
		ctx:       ctx}, nil
}

func inspectFieldAndMarshall(name, field string, output interface{}) error {
	str, err := inspectFieldJSON(name, field)
	if err != nil {
		return err
	}

	return json.Unmarshal([]byte(str), output)
}

func inspectFilter(name, filter string) (string, error) {
	format := fmt.Sprintf("{{%s}}", filter)
	inspectCmd := exec.Command(dockerBinary, "inspect", "-f", format, name)
	out, exitCode, err := runCommandWithOutput(inspectCmd)
	if err != nil || exitCode != 0 {
		return "", fmt.Errorf("failed to inspect container %s: %s", name, out)
	}
	return strings.TrimSpace(out), nil
}

func inspectField(name, field string) (string, error) {
	return inspectFilter(name, fmt.Sprintf(".%s", field))
}

func inspectFieldJSON(name, field string) (string, error) {
	return inspectFilter(name, fmt.Sprintf("json .%s", field))
}

func inspectFieldMap(name, path, field string) (string, error) {
	return inspectFilter(name, fmt.Sprintf("index .%s %q", path, field))
}

func inspectMountSourceField(name, destination string) (string, error) {
	m, err := inspectMountPoint(name, destination)
	if err != nil {
		return "", err
	}
	return m.Source, nil
}

func inspectMountPoint(name, destination string) (types.MountPoint, error) {
	out, err := inspectFieldJSON(name, "Mounts")
	if err != nil {
		return types.MountPoint{}, err
	}

	return inspectMountPointJSON(out, destination)
}

var errMountNotFound = errors.New("mount point not found")

func inspectMountPointJSON(j, destination string) (types.MountPoint, error) {
	var mp []types.MountPoint
	if err := unmarshalJSON([]byte(j), &mp); err != nil {
		return types.MountPoint{}, err
	}

	var m *types.MountPoint
	for _, c := range mp {
		if c.Destination == destination {
			m = &c
			break
		}
	}

	if m == nil {
		return types.MountPoint{}, errMountNotFound
	}

	return *m, nil
}

func getIDByName(name string) (string, error) {
	return inspectField(name, "Id")
}

// getContainerState returns the exit code of the container
// and true if it's running
// the exit code should be ignored if it's running
func getContainerState(c *check.C, id string) (int, bool, error) {
	var (
		exitStatus int
		running    bool
	)
	out, exitCode := dockerCmd(c, "inspect", "--format={{.State.Running}} {{.State.ExitCode}}", id)
	if exitCode != 0 {
		return 0, false, fmt.Errorf("%q doesn't exist: %s", id, out)
	}

	out = strings.Trim(out, "\n")
	splitOutput := strings.Split(out, " ")
	if len(splitOutput) != 2 {
		return 0, false, fmt.Errorf("failed to get container state: output is broken")
	}
	if splitOutput[0] == "true" {
		running = true
	}
	if n, err := strconv.Atoi(splitOutput[1]); err == nil {
		exitStatus = n
	} else {
		return 0, false, fmt.Errorf("failed to get container state: couldn't parse integer")
	}

	return exitStatus, running, nil
}

func buildImageCmd(name, dockerfile string, useCache bool) *exec.Cmd {
	args := []string{"-D", "build", "-t", name}
	if !useCache {
		args = append(args, "--no-cache")
	}
	args = append(args, "-")
	buildCmd := exec.Command(dockerBinary, args...)
	buildCmd.Stdin = strings.NewReader(dockerfile)
	return buildCmd

}

func buildImageWithOut(name, dockerfile string, useCache bool) (string, string, error) {
	buildCmd := buildImageCmd(name, dockerfile, useCache)
	out, exitCode, err := runCommandWithOutput(buildCmd)
	if err != nil || exitCode != 0 {
		return "", out, fmt.Errorf("failed to build the image: %s", out)
	}
	id, err := getIDByName(name)
	if err != nil {
		return "", out, err
	}
	return id, out, nil
}

func buildImageWithStdoutStderr(name, dockerfile string, useCache bool) (string, string, string, error) {
	buildCmd := buildImageCmd(name, dockerfile, useCache)
	stdout, stderr, exitCode, err := runCommandWithStdoutStderr(buildCmd)
	if err != nil || exitCode != 0 {
		return "", stdout, stderr, fmt.Errorf("failed to build the image: %s", stdout)
	}
	id, err := getIDByName(name)
	if err != nil {
		return "", stdout, stderr, err
	}
	return id, stdout, stderr, nil
}

func buildImage(name, dockerfile string, useCache bool) (string, error) {
	id, _, err := buildImageWithOut(name, dockerfile, useCache)
	return id, err
}

func buildImageFromContext(name string, ctx *FakeContext, useCache bool) (string, error) {
	args := []string{"build", "-t", name}
	if !useCache {
		args = append(args, "--no-cache")
	}
	args = append(args, ".")
	buildCmd := exec.Command(dockerBinary, args...)
	buildCmd.Dir = ctx.Dir
	out, exitCode, err := runCommandWithOutput(buildCmd)
	if err != nil || exitCode != 0 {
		return "", fmt.Errorf("failed to build the image: %s", out)
	}
	return getIDByName(name)
}

func buildImageFromPath(name, path string, useCache bool) (string, error) {
	args := []string{"build", "-t", name}
	if !useCache {
		args = append(args, "--no-cache")
	}
	args = append(args, path)
	buildCmd := exec.Command(dockerBinary, args...)
	out, exitCode, err := runCommandWithOutput(buildCmd)
	if err != nil || exitCode != 0 {
		return "", fmt.Errorf("failed to build the image: %s", out)
	}
	return getIDByName(name)
}

type gitServer interface {
	URL() string
	Close() error
}

type localGitServer struct {
	*httptest.Server
}

func (r *localGitServer) Close() error {
	r.Server.Close()
	return nil
}

func (r *localGitServer) URL() string {
	return r.Server.URL
}

type fakeGit struct {
	root    string
	server  gitServer
	RepoURL string
}

func (g *fakeGit) Close() {
	g.server.Close()
	os.RemoveAll(g.root)
}

func newFakeGit(name string, files map[string]string, enforceLocalServer bool) (*fakeGit, error) {
	ctx, err := fakeContextWithFiles(files)
	if err != nil {
		return nil, err
	}
	defer ctx.Close()
	curdir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	defer os.Chdir(curdir)

	if output, err := exec.Command("git", "init", ctx.Dir).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("error trying to init repo: %s (%s)", err, output)
	}
	err = os.Chdir(ctx.Dir)
	if err != nil {
		return nil, err
	}
	if output, err := exec.Command("git", "config", "user.name", "Fake User").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("error trying to set 'user.name': %s (%s)", err, output)
	}
	if output, err := exec.Command("git", "config", "user.email", "fake.user@example.com").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("error trying to set 'user.email': %s (%s)", err, output)
	}
	if output, err := exec.Command("git", "add", "*").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("error trying to add files to repo: %s (%s)", err, output)
	}
	if output, err := exec.Command("git", "commit", "-a", "-m", "Initial commit").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("error trying to commit to repo: %s (%s)", err, output)
	}

	root, err := ioutil.TempDir("", "docker-test-git-repo")
	if err != nil {
		return nil, err
	}
	repoPath := filepath.Join(root, name+".git")
	if output, err := exec.Command("git", "clone", "--bare", ctx.Dir, repoPath).CombinedOutput(); err != nil {
		os.RemoveAll(root)
		return nil, fmt.Errorf("error trying to clone --bare: %s (%s)", err, output)
	}
	err = os.Chdir(repoPath)
	if err != nil {
		os.RemoveAll(root)
		return nil, err
	}
	if output, err := exec.Command("git", "update-server-info").CombinedOutput(); err != nil {
		os.RemoveAll(root)
		return nil, fmt.Errorf("error trying to git update-server-info: %s (%s)", err, output)
	}
	err = os.Chdir(curdir)
	if err != nil {
		os.RemoveAll(root)
		return nil, err
	}

	var server gitServer
	if !enforceLocalServer {
		// use fakeStorage server, which might be local or remote (at test daemon)
		server, err = fakeStorageWithContext(fakeContextFromDir(root))
		if err != nil {
			return nil, fmt.Errorf("cannot start fake storage: %v", err)
		}
	} else {
		// always start a local http server on CLI test machin
		httpServer := httptest.NewServer(http.FileServer(http.Dir(root)))
		server = &localGitServer{httpServer}
	}
	return &fakeGit{
		root:    root,
		server:  server,
		RepoURL: fmt.Sprintf("%s/%s.git", server.URL(), name),
	}, nil
}

// Write `content` to the file at path `dst`, creating it if necessary,
// as well as any missing directories.
// The file is truncated if it already exists.
// Call c.Fatal() at the first error.
func writeFile(dst, content string, c *check.C) {
	// Create subdirectories if necessary
	if err := os.MkdirAll(path.Dir(dst), 0700); err != nil {
		c.Fatal(err)
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0700)
	if err != nil {
		c.Fatal(err)
	}
	defer f.Close()
	// Write content (truncate if it exists)
	if _, err := io.Copy(f, strings.NewReader(content)); err != nil {
		c.Fatal(err)
	}
}

// Return the contents of file at path `src`.
// Call c.Fatal() at the first error (including if the file doesn't exist)
func readFile(src string, c *check.C) (content string) {
	data, err := ioutil.ReadFile(src)
	if err != nil {
		c.Fatal(err)
	}

	return string(data)
}

func containerStorageFile(containerID, basename string) string {
	return filepath.Join("/var/lib/docker/containers", containerID, basename)
}

// docker commands that use this function must be run with the '-d' switch.
func runCommandAndReadContainerFile(filename string, cmd *exec.Cmd) ([]byte, error) {
	out, _, err := runCommandWithOutput(cmd)
	if err != nil {
		return nil, fmt.Errorf("%v: %q", err, out)
	}

	contID := strings.TrimSpace(out)

	if err := waitRun(contID); err != nil {
		return nil, fmt.Errorf("%v: %q", contID, err)
	}

	return readContainerFile(contID, filename)
}

func readContainerFile(containerID, filename string) ([]byte, error) {
	f, err := os.Open(containerStorageFile(containerID, filename))
	if err != nil {
		return nil, err
	}
	defer f.Close()

	content, err := ioutil.ReadAll(f)
	if err != nil {
		return nil, err
	}

	return content, nil
}

func readContainerFileWithExec(containerID, filename string) ([]byte, error) {
	out, _, err := runCommandWithOutput(exec.Command(dockerBinary, "exec", containerID, "cat", filename))
	return []byte(out), err
}

// daemonTime provides the current time on the daemon host
func daemonTime(c *check.C) time.Time {
	if isLocalDaemon {
		return time.Now()
	}

	status, body, err := sockRequest("GET", "/info", nil)
	c.Assert(status, check.Equals, http.StatusOK)
	c.Assert(err, check.IsNil)

	type infoJSON struct {
		SystemTime string
	}
	var info infoJSON
	if err = json.Unmarshal(body, &info); err != nil {
		c.Fatalf("unable to unmarshal /info response: %v", err)
	}

	dt, err := time.Parse(time.RFC3339Nano, info.SystemTime)
	if err != nil {
		c.Fatal(err)
	}
	return dt
}

func setupRegistry(c *check.C) *testRegistryV2 {
	testRequires(c, RegistryHosting)
	reg, err := newTestRegistryV2(c)
	if err != nil {
		c.Fatal(err)
	}

	// Wait for registry to be ready to serve requests.
	for i := 0; i != 5; i++ {
		if err = reg.Ping(); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if err != nil {
		c.Fatal("Timeout waiting for test registry to become available")
	}
	return reg
}

func setupNotary(c *check.C) *testNotary {
	testRequires(c, NotaryHosting)
	ts, err := newTestNotary(c)
	if err != nil {
		c.Fatal(err)
	}

	return ts
}

// appendBaseEnv appends the minimum set of environment variables to exec the
// docker cli binary for testing with correct configuration to the given env
// list.
func appendBaseEnv(env []string) []string {
	preserveList := []string{
		// preserve remote test host
		"DOCKER_HOST",

		// windows: requires preserving SystemRoot, otherwise dial tcp fails
		// with "GetAddrInfoW: A non-recoverable error occurred during a database lookup."
		"SystemRoot",
	}

	for _, key := range preserveList {
		if val := os.Getenv(key); val != "" {
			env = append(env, fmt.Sprintf("%s=%s", key, val))
		}
	}
	return env
}

func createTmpFile(c *check.C, content string) string {
	f, err := ioutil.TempFile("", "testfile")
	c.Assert(err, check.IsNil)

	filename := f.Name()

	err = ioutil.WriteFile(filename, []byte(content), 0644)
	c.Assert(err, check.IsNil)

	return filename
}

func buildImageWithOutInDamon(socket string, name, dockerfile string, useCache bool) (string, error) {
	args := []string{"--host", socket}
	buildCmd := buildImageCmdArgs(args, name, dockerfile, useCache)
	out, exitCode, err := runCommandWithOutput(buildCmd)
	if err != nil || exitCode != 0 {
		return out, fmt.Errorf("failed to build the image: %s, error: %v", out, err)
	}
	return out, nil
}

func buildImageCmdArgs(args []string, name, dockerfile string, useCache bool) *exec.Cmd {
	args = append(args, []string{"-D", "build", "-t", name}...)
	if !useCache {
		args = append(args, "--no-cache")
	}
	args = append(args, "-")
	buildCmd := exec.Command(dockerBinary, args...)
	buildCmd.Stdin = strings.NewReader(dockerfile)
	return buildCmd

}

func waitForContainer(contID string, args ...string) error {
	args = append([]string{"run", "--name", contID}, args...)
	cmd := exec.Command(dockerBinary, args...)
	if _, err := runCommand(cmd); err != nil {
		return err
	}

	if err := waitRun(contID); err != nil {
		return err
	}

	return nil
}

// waitRun will wait for the specified container to be running, maximum 5 seconds.
func waitRun(contID string) error {
	return waitInspect(contID, "{{.State.Running}}", "true", 5)
}

// waitInspect will wait for the specified container to have the specified string
// in the inspect output. It will wait until the specified timeout (in seconds)
// is reached.
func waitInspect(name, expr, expected string, timeout int) error {
	after := time.After(time.Duration(timeout) * time.Second)

	for {
		cmd := exec.Command(dockerBinary, "inspect", "-f", expr, name)
		out, _, err := runCommandWithOutput(cmd)
		if err != nil {
			if !strings.Contains(out, "No such") {
				return fmt.Errorf("error executing docker inspect: %v\n%s", err, out)
			}
			select {
			case <-after:
				return err
			default:
				time.Sleep(10 * time.Millisecond)
				continue
			}
		}

		out = strings.TrimSpace(out)
		if out == expected {
			break
		}

		select {
		case <-after:
			return fmt.Errorf("condition \"%q == %q\" not true in time", out, expected)
		default:
		}

		time.Sleep(100 * time.Millisecond)
	}
	return nil
}
