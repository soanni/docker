package daemon

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/docker/docker/container"
	"github.com/docker/docker/pkg/discovery"
	_ "github.com/docker/docker/pkg/discovery/memory"
	"github.com/docker/docker/volume"
	volumedrivers "github.com/docker/docker/volume/drivers"
	"github.com/docker/docker/volume/local"
	"github.com/docker/docker/volume/store"
)

func initDaemonWithVolumeStore(tmp string) (*Daemon, error) {
	var err error
	daemon := &Daemon{
		repository: tmp,
		root:       tmp,
	}
	daemon.volumes, err = store.New(tmp)
	if err != nil {
		return nil, err
	}

	volumesDriver, err := local.New(tmp, 0, 0)
	if err != nil {
		return nil, err
	}
	volumedrivers.Register(volumesDriver, volumesDriver.Name())

	return daemon, nil
}

func TestContainerInitDNS(t *testing.T) {
	tmp, err := ioutil.TempDir("", "docker-container-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmp)

	containerID := "d59df5276e7b219d510fe70565e0404bc06350e0d4b43fe961f22f339980170e"
	containerPath := filepath.Join(tmp, containerID)
	if err := os.MkdirAll(containerPath, 0755); err != nil {
		t.Fatal(err)
	}

	config := `{"State":{"Running":true,"Paused":false,"Restarting":false,"OOMKilled":false,"Dead":false,"Pid":2464,"ExitCode":0,
"Error":"","StartedAt":"2015-05-26T16:48:53.869308965Z","FinishedAt":"0001-01-01T00:00:00Z"},
"ID":"d59df5276e7b219d510fe70565e0404bc06350e0d4b43fe961f22f339980170e","Created":"2015-05-26T16:48:53.7987917Z","Path":"top",
"Args":[],"Config":{"Hostname":"d59df5276e7b","Domainname":"","User":"","Memory":0,"MemorySwap":0,"CpuShares":0,"Cpuset":"",
"AttachStdin":false,"AttachStdout":false,"AttachStderr":false,"PortSpecs":null,"ExposedPorts":null,"Tty":true,"OpenStdin":true,
"StdinOnce":false,"Env":null,"Cmd":["top"],"Image":"ubuntu:latest","Volumes":null,"WorkingDir":"","Entrypoint":null,
"NetworkDisabled":false,"MacAddress":"","OnBuild":null,"Labels":{}},"Image":"07f8e8c5e66084bef8f848877857537ffe1c47edd01a93af27e7161672ad0e95",
"NetworkSettings":{"IPAddress":"172.17.0.1","IPPrefixLen":16,"MacAddress":"02:42:ac:11:00:01","LinkLocalIPv6Address":"fe80::42:acff:fe11:1",
"LinkLocalIPv6PrefixLen":64,"GlobalIPv6Address":"","GlobalIPv6PrefixLen":0,"Gateway":"172.17.42.1","IPv6Gateway":"","Bridge":"docker0","Ports":{}},
"ResolvConfPath":"/var/lib/docker/containers/d59df5276e7b219d510fe70565e0404bc06350e0d4b43fe961f22f339980170e/resolv.conf",
"HostnamePath":"/var/lib/docker/containers/d59df5276e7b219d510fe70565e0404bc06350e0d4b43fe961f22f339980170e/hostname",
"HostsPath":"/var/lib/docker/containers/d59df5276e7b219d510fe70565e0404bc06350e0d4b43fe961f22f339980170e/hosts",
"LogPath":"/var/lib/docker/containers/d59df5276e7b219d510fe70565e0404bc06350e0d4b43fe961f22f339980170e/d59df5276e7b219d510fe70565e0404bc06350e0d4b43fe961f22f339980170e-json.log",
"Name":"/ubuntu","Driver":"aufs","MountLabel":"","ProcessLabel":"","AppArmorProfile":"","RestartCount":0,
"UpdateDns":false,"Volumes":{},"VolumesRW":{},"AppliedVolumesFrom":null}`

	// Container struct only used to retrieve path to config file
	container := &container.Container{CommonContainer: container.CommonContainer{Root: containerPath}}
	configPath, err := container.ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err = ioutil.WriteFile(configPath, []byte(config), 0644); err != nil {
		t.Fatal(err)
	}

	hostConfig := `{"Binds":[],"ContainerIDFile":"","Memory":0,"MemorySwap":0,"CpuShares":0,"CpusetCpus":"",
"Privileged":false,"PortBindings":{},"Links":null,"PublishAllPorts":false,"Dns":null,"DnsOptions":null,"DnsSearch":null,"ExtraHosts":null,"VolumesFrom":null,
"Devices":[],"NetworkMode":"bridge","IpcMode":"","PidMode":"","CapAdd":null,"CapDrop":null,"RestartPolicy":{"Name":"no","MaximumRetryCount":0},
"SecurityOpt":null,"ReadonlyRootfs":false,"Ulimits":null,"LogConfig":{"Type":"","Config":null},"CgroupParent":""}`

	hostConfigPath, err := container.HostConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err = ioutil.WriteFile(hostConfigPath, []byte(hostConfig), 0644); err != nil {
		t.Fatal(err)
	}

	daemon, err := initDaemonWithVolumeStore(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer volumedrivers.Unregister(volume.DefaultDriverName)

	c, err := daemon.load(containerID)
	if err != nil {
		t.Fatal(err)
	}

	if c.HostConfig.DNS == nil {
		t.Fatal("Expected container DNS to not be nil")
	}

	if c.HostConfig.DNSSearch == nil {
		t.Fatal("Expected container DNSSearch to not be nil")
	}

	if c.HostConfig.DNSOptions == nil {
		t.Fatal("Expected container DNSOptions to not be nil")
	}
}

func TestDaemonReloadLabels(t *testing.T) {
	daemon := &Daemon{}
	daemon.configStore = &Config{
		CommonConfig: CommonConfig{
			Labels: []string{"foo:bar"},
		},
	}

	valuesSets := make(map[string]interface{})
	valuesSets["labels"] = "foo:baz"
	newConfig := &Config{
		CommonConfig: CommonConfig{
			Labels:    []string{"foo:baz"},
			valuesSet: valuesSets,
		},
	}

	daemon.Reload(newConfig)
	label := daemon.configStore.Labels[0]
	if label != "foo:baz" {
		t.Fatalf("Expected daemon label `foo:baz`, got %s", label)
	}
}

func TestDaemonReloadNotAffectOthers(t *testing.T) {
	daemon := &Daemon{}
	daemon.configStore = &Config{
		CommonConfig: CommonConfig{
			Labels: []string{"foo:bar"},
			Debug:  true,
		},
	}

	valuesSets := make(map[string]interface{})
	valuesSets["labels"] = "foo:baz"
	newConfig := &Config{
		CommonConfig: CommonConfig{
			Labels:    []string{"foo:baz"},
			valuesSet: valuesSets,
		},
	}

	daemon.Reload(newConfig)
	label := daemon.configStore.Labels[0]
	if label != "foo:baz" {
		t.Fatalf("Expected daemon label `foo:baz`, got %s", label)
	}
	debug := daemon.configStore.Debug
	if !debug {
		t.Fatalf("Expected debug 'enabled', got 'disabled'")
	}
}

func TestDaemonDiscoveryReload(t *testing.T) {
	daemon := &Daemon{}
	daemon.configStore = &Config{
		CommonConfig: CommonConfig{
			ClusterStore:     "memory://127.0.0.1",
			ClusterAdvertise: "127.0.0.1:3333",
		},
	}

	if err := daemon.initDiscovery(daemon.configStore); err != nil {
		t.Fatal(err)
	}

	expected := discovery.Entries{
		&discovery.Entry{Host: "127.0.0.1", Port: "3333"},
	}

	select {
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for discovery")
	case <-daemon.discoveryWatcher.ReadyCh():
	}

	stopCh := make(chan struct{})
	defer close(stopCh)
	ch, errCh := daemon.discoveryWatcher.Watch(stopCh)

	select {
	case <-time.After(1 * time.Second):
		t.Fatal("failed to get discovery advertisements in time")
	case e := <-ch:
		if !reflect.DeepEqual(e, expected) {
			t.Fatalf("expected %v, got %v\n", expected, e)
		}
	case e := <-errCh:
		t.Fatal(e)
	}

	valuesSets := make(map[string]interface{})
	valuesSets["cluster-store"] = "memory://127.0.0.1:2222"
	valuesSets["cluster-advertise"] = "127.0.0.1:5555"
	newConfig := &Config{
		CommonConfig: CommonConfig{
			ClusterStore:     "memory://127.0.0.1:2222",
			ClusterAdvertise: "127.0.0.1:5555",
			valuesSet:        valuesSets,
		},
	}

	expected = discovery.Entries{
		&discovery.Entry{Host: "127.0.0.1", Port: "5555"},
	}

	if err := daemon.Reload(newConfig); err != nil {
		t.Fatal(err)
	}

	select {
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for discovery")
	case <-daemon.discoveryWatcher.ReadyCh():
	}

	ch, errCh = daemon.discoveryWatcher.Watch(stopCh)

	select {
	case <-time.After(1 * time.Second):
		t.Fatal("failed to get discovery advertisements in time")
	case e := <-ch:
		if !reflect.DeepEqual(e, expected) {
			t.Fatalf("expected %v, got %v\n", expected, e)
		}
	case e := <-errCh:
		t.Fatal(e)
	}
}

func TestDaemonDiscoveryReloadFromEmptyDiscovery(t *testing.T) {
	daemon := &Daemon{}
	daemon.configStore = &Config{}

	valuesSet := make(map[string]interface{})
	valuesSet["cluster-store"] = "memory://127.0.0.1:2222"
	valuesSet["cluster-advertise"] = "127.0.0.1:5555"
	newConfig := &Config{
		CommonConfig: CommonConfig{
			ClusterStore:     "memory://127.0.0.1:2222",
			ClusterAdvertise: "127.0.0.1:5555",
			valuesSet:        valuesSet,
		},
	}

	expected := discovery.Entries{
		&discovery.Entry{Host: "127.0.0.1", Port: "5555"},
	}

	if err := daemon.Reload(newConfig); err != nil {
		t.Fatal(err)
	}

	select {
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for discovery")
	case <-daemon.discoveryWatcher.ReadyCh():
	}

	stopCh := make(chan struct{})
	defer close(stopCh)
	ch, errCh := daemon.discoveryWatcher.Watch(stopCh)

	select {
	case <-time.After(1 * time.Second):
		t.Fatal("failed to get discovery advertisements in time")
	case e := <-ch:
		if !reflect.DeepEqual(e, expected) {
			t.Fatalf("expected %v, got %v\n", expected, e)
		}
	case e := <-errCh:
		t.Fatal(e)
	}
}

func TestDaemonDiscoveryReloadOnlyClusterAdvertise(t *testing.T) {
	daemon := &Daemon{}
	daemon.configStore = &Config{
		CommonConfig: CommonConfig{
			ClusterStore: "memory://127.0.0.1",
		},
	}
	valuesSets := make(map[string]interface{})
	valuesSets["cluster-advertise"] = "127.0.0.1:5555"
	newConfig := &Config{
		CommonConfig: CommonConfig{
			ClusterAdvertise: "127.0.0.1:5555",
			valuesSet:        valuesSets,
		},
	}
	expected := discovery.Entries{
		&discovery.Entry{Host: "127.0.0.1", Port: "5555"},
	}

	if err := daemon.Reload(newConfig); err != nil {
		t.Fatal(err)
	}

	select {
	case <-daemon.discoveryWatcher.ReadyCh():
	case <-time.After(10 * time.Second):
		t.Fatal("Timeout waiting for discovery")
	}
	stopCh := make(chan struct{})
	defer close(stopCh)
	ch, errCh := daemon.discoveryWatcher.Watch(stopCh)

	select {
	case <-time.After(1 * time.Second):
		t.Fatal("failed to get discovery advertisements in time")
	case e := <-ch:
		if !reflect.DeepEqual(e, expected) {
			t.Fatalf("expected %v, got %v\n", expected, e)
		}
	case e := <-errCh:
		t.Fatal(e)
	}

}
