// Package daemon exposes the functions that occur on the host server
// that the Docker daemon is running.
//
// In implementing the various functions of the daemon, there is often
// a method-specific struct for configuring the runtime behavior.
package daemon

import (
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Sirupsen/logrus"
	containerd "github.com/docker/containerd/api/grpc/types"
	"github.com/docker/docker/api"
	"github.com/docker/docker/container"
	"github.com/docker/docker/daemon/events"
	"github.com/docker/docker/daemon/exec"
	"github.com/docker/engine-api/types"
	containertypes "github.com/docker/engine-api/types/container"
	registrytypes "github.com/docker/engine-api/types/registry"
	// register graph drivers
	_ "github.com/docker/docker/daemon/graphdriver/register"
	dmetadata "github.com/docker/docker/distribution/metadata"
	"github.com/docker/docker/distribution/xfer"
	"github.com/docker/docker/dockerversion"
	"github.com/docker/docker/image"
	"github.com/docker/docker/layer"
	"github.com/docker/docker/libcontainerd"
	"github.com/docker/docker/migrate/v1"
	"github.com/docker/docker/pkg/fileutils"
	"github.com/docker/docker/pkg/graphdb"
	"github.com/docker/docker/pkg/idtools"
	"github.com/docker/docker/pkg/progress"
	"github.com/docker/docker/pkg/registrar"
	"github.com/docker/docker/pkg/signal"
	"github.com/docker/docker/pkg/streamformatter"
	"github.com/docker/docker/pkg/sysinfo"
	"github.com/docker/docker/pkg/system"
	"github.com/docker/docker/pkg/truncindex"
	"github.com/docker/docker/reference"
	"github.com/docker/docker/registry"
	"github.com/docker/docker/runconfig"
	"github.com/docker/docker/utils"
	volumedrivers "github.com/docker/docker/volume/drivers"
	"github.com/docker/docker/volume/local"
	"github.com/docker/docker/volume/store"
	"github.com/docker/engine-api/types/filters"
	"github.com/docker/libnetwork"
	nwconfig "github.com/docker/libnetwork/config"
	"github.com/docker/libtrust"
	"golang.org/x/net/context"
)

var (
	errSystemNotSupported = fmt.Errorf("The Docker daemon is not supported on this platform.")
)

// Daemon holds information about the Docker daemon.
type Daemon struct {
	ID                        string
	repository                string
	containers                container.Store
	execCommands              *exec.Store
	referenceStore            reference.Store
	downloadManager           *xfer.LayerDownloadManager
	uploadManager             *xfer.LayerUploadManager
	distributionMetadataStore dmetadata.Store
	trustKey                  libtrust.PrivateKey
	idIndex                   *truncindex.TruncIndex
	configStore               *Config
	statsCollector            *statsCollector
	defaultLogConfig          containertypes.LogConfig
	RegistryService           *registry.Service
	EventsService             *events.Events
	netController             libnetwork.NetworkController
	volumes                   *store.VolumeStore
	discoveryWatcher          discoveryReloader
	root                      string
	seccompEnabled            bool
	shutdown                  bool
	uidMaps                   []idtools.IDMap
	gidMaps                   []idtools.IDMap
	layerStore                layer.Store
	imageStore                image.Store
	nameIndex                 *registrar.Registrar
	linkIndex                 *linkIndex
	containerd                libcontainerd.Client
	defaultIsolation          containertypes.Isolation // Default isolation mode on Windows
}

// restore tries reload the existing containers at the start of the daemon.
func (daemon *Daemon) restore() error {
	var (
		debug         = utils.IsDebugEnabled()
		currentDriver = daemon.GraphDriverName()
		containers    = make(map[string]*container.Container)
	)

	if !debug {
		logrus.Info("Loading containers: start.")
	}
	dir, err := ioutil.ReadDir(daemon.repository)
	if err != nil {
		return err
	}

	for _, v := range dir {
		id := v.Name()
		container, err := daemon.load(id)
		if !debug && logrus.GetLevel() == logrus.InfoLevel {
			fmt.Print(".")
		}
		if err != nil {
			logrus.Errorf("Failed to load container %v: %v", id, err)
			continue
		}

		// Ignore the container if it does not support the current driver being used by the graph
		if (container.Driver == "" && currentDriver == "aufs") || container.Driver == currentDriver {
			rwlayer, err := daemon.layerStore.GetRWLayer(container.ID)
			if err != nil {
				logrus.Errorf("Failed to load container mount %v: %v", id, err)
				continue
			}
			container.RWLayer = rwlayer
			logrus.Debugf("Loaded container %v", container.ID)

			containers[container.ID] = container
		} else {
			logrus.Debugf("Cannot load container %s because it was created with another graph driver.", container.ID)
		}
	}

	var migrateLegacyLinks bool
	restartContainers := make(map[*container.Container]chan struct{})
	for _, c := range containers {
		if err := daemon.registerName(c); err != nil {
			logrus.Errorf("Failed to register container %s: %s", c.ID, err)
			continue
		}
		if err := daemon.Register(c); err != nil {
			logrus.Errorf("Failed to register container %s: %s", c.ID, err)
			continue
		}
	}
	var wg sync.WaitGroup
	var mapLock sync.Mutex
	for _, c := range containers {
		wg.Add(1)
		go func(c *container.Container) {
			defer wg.Done()
			rm := c.RestartManager(false)
			if c.IsRunning() || c.IsPaused() {
				// Fix activityCount such that graph mounts can be unmounted later
				if err := daemon.layerStore.ReinitRWLayer(c.RWLayer); err != nil {
					logrus.Errorf("Failed to ReinitRWLayer for %s due to %s", c.ID, err)
					return
				}
				if err := daemon.containerd.Restore(c.ID, libcontainerd.WithRestartManager(rm)); err != nil {
					logrus.Errorf("Failed to restore with containerd: %q", err)
					return
				}
			}
			// fixme: only if not running
			// get list of containers we need to restart
			if daemon.configStore.AutoRestart && !c.IsRunning() && !c.IsPaused() && c.ShouldRestart() {
				mapLock.Lock()
				restartContainers[c] = make(chan struct{})
				mapLock.Unlock()
			}

			if c.RemovalInProgress {
				// We probably crashed in the middle of a removal, reset
				// the flag.
				//
				// We DO NOT remove the container here as we do not
				// know if the user had requested for either the
				// associated volumes, network links or both to also
				// be removed. So we put the container in the "dead"
				// state and leave further processing up to them.
				logrus.Debugf("Resetting RemovalInProgress flag from %v", c.ID)
				c.ResetRemovalInProgress()
				c.SetDead()
				c.ToDisk()
			}

			// if c.hostConfig.Links is nil (not just empty), then it is using the old sqlite links and needs to be migrated
			if c.HostConfig != nil && c.HostConfig.Links == nil {
				migrateLegacyLinks = true
			}
		}(c)
	}
	wg.Wait()

	// migrate any legacy links from sqlite
	linkdbFile := filepath.Join(daemon.root, "linkgraph.db")
	var legacyLinkDB *graphdb.Database
	if migrateLegacyLinks {
		legacyLinkDB, err = graphdb.NewSqliteConn(linkdbFile)
		if err != nil {
			return fmt.Errorf("error connecting to legacy link graph DB %s, container links may be lost: %v", linkdbFile, err)
		}
		defer legacyLinkDB.Close()
	}

	// Now that all the containers are registered, register the links
	for _, c := range containers {
		if migrateLegacyLinks {
			if err := daemon.migrateLegacySqliteLinks(legacyLinkDB, c); err != nil {
				return err
			}
		}
		if err := daemon.registerLinks(c, c.HostConfig); err != nil {
			logrus.Errorf("failed to register link for container %s: %v", c.ID, err)
		}
	}

	group := sync.WaitGroup{}
	for c, notifier := range restartContainers {
		group.Add(1)

		go func(c *container.Container, chNotify chan struct{}) {
			defer group.Done()

			logrus.Debugf("Starting container %s", c.ID)

			// ignore errors here as this is a best effort to wait for children to be
			//   running before we try to start the container
			children := daemon.children(c)
			timeout := time.After(5 * time.Second)
			for _, child := range children {
				if notifier, exists := restartContainers[child]; exists {
					select {
					case <-notifier:
					case <-timeout:
					}
				}
			}

			// Make sure networks are available before starting
			daemon.waitForNetworks(c)
			if err := daemon.containerStart(c); err != nil {
				logrus.Errorf("Failed to start container %s: %s", c.ID, err)
			}
			close(chNotify)
		}(c, notifier)

	}
	group.Wait()

	// any containers that were started above would already have had this done,
	// however we need to now prepare the mountpoints for the rest of the containers as well.
	// This shouldn't cause any issue running on the containers that already had this run.
	// This must be run after any containers with a restart policy so that containerized plugins
	// can have a chance to be running before we try to initialize them.
	for _, c := range containers {
		// if the container has restart policy, do not
		// prepare the mountpoints since it has been done on restarting.
		// This is to speed up the daemon start when a restart container
		// has a volume and the volume dirver is not available.
		if _, ok := restartContainers[c]; ok {
			continue
		}
		group.Add(1)
		go func(c *container.Container) {
			defer group.Done()
			if err := daemon.prepareMountPoints(c); err != nil {
				logrus.Error(err)
			}
		}(c)
	}

	group.Wait()

	if !debug {
		if logrus.GetLevel() == logrus.InfoLevel {
			fmt.Println()
		}
		logrus.Info("Loading containers: done.")
	}

	return nil
}

func (daemon *Daemon) mergeAndVerifyConfig(config *containertypes.Config, img *image.Image) error {
	if img != nil && img.Config != nil {
		if err := merge(config, img.Config); err != nil {
			return err
		}
	}
	if len(config.Entrypoint) == 0 && len(config.Cmd) == 0 {
		return fmt.Errorf("No command specified")
	}
	return nil
}

// NewDaemon sets up everything for the daemon to be able to service
// requests from the webserver.
func NewDaemon(config *Config, registryService *registry.Service, containerdRemote libcontainerd.Remote) (daemon *Daemon, err error) {
	setDefaultMtu(config)

	// Ensure we have compatible and valid configuration options
	if err := verifyDaemonSettings(config); err != nil {
		return nil, err
	}

	// Do we have a disabled network?
	config.DisableBridge = isBridgeNetworkDisabled(config)

	// Verify the platform is supported as a daemon
	if !platformSupported {
		return nil, errSystemNotSupported
	}

	// Validate platform-specific requirements
	if err := checkSystem(); err != nil {
		return nil, err
	}

	// set up SIGUSR1 handler on Unix-like systems, or a Win32 global event
	// on Windows to dump Go routine stacks
	setupDumpStackTrap()

	uidMaps, gidMaps, err := setupRemappedRoot(config)
	if err != nil {
		return nil, err
	}
	rootUID, rootGID, err := idtools.GetRootUIDGID(uidMaps, gidMaps)
	if err != nil {
		return nil, err
	}

	// get the canonical path to the Docker root directory
	var realRoot string
	if _, err := os.Stat(config.Root); err != nil && os.IsNotExist(err) {
		realRoot = config.Root
	} else {
		realRoot, err = fileutils.ReadSymlinkedDirectory(config.Root)
		if err != nil {
			return nil, fmt.Errorf("Unable to get the full path to root (%s): %s", config.Root, err)
		}
	}

	if err = setupDaemonRoot(config, realRoot, rootUID, rootGID); err != nil {
		return nil, err
	}

	// set up the tmpDir to use a canonical path
	tmp, err := tempDir(config.Root, rootUID, rootGID)
	if err != nil {
		return nil, fmt.Errorf("Unable to get the TempDir under %s: %s", config.Root, err)
	}
	realTmp, err := fileutils.ReadSymlinkedDirectory(tmp)
	if err != nil {
		return nil, fmt.Errorf("Unable to get the full path to the TempDir (%s): %s", tmp, err)
	}
	os.Setenv("TMPDIR", realTmp)

	d := &Daemon{configStore: config}
	// Ensure the daemon is properly shutdown if there is a failure during
	// initialization
	defer func() {
		if err != nil {
			if err := d.Shutdown(); err != nil {
				logrus.Error(err)
			}
		}
	}()

	// Set the default isolation mode (only applicable on Windows)
	if err := d.setDefaultIsolation(); err != nil {
		return nil, fmt.Errorf("error setting default isolation mode: %v", err)
	}

	logrus.Debugf("Using default logging driver %s", config.LogConfig.Type)

	if err := configureMaxThreads(config); err != nil {
		logrus.Warnf("Failed to configure golang's threads limit: %v", err)
	}

	installDefaultAppArmorProfile()
	daemonRepo := filepath.Join(config.Root, "containers")
	if err := idtools.MkdirAllAs(daemonRepo, 0700, rootUID, rootGID); err != nil && !os.IsExist(err) {
		return nil, err
	}

	driverName := os.Getenv("DOCKER_DRIVER")
	if driverName == "" {
		driverName = config.GraphDriver
	}
	d.layerStore, err = layer.NewStoreFromOptions(layer.StoreOptions{
		StorePath:                 config.Root,
		MetadataStorePathTemplate: filepath.Join(config.Root, "image", "%s", "layerdb"),
		GraphDriver:               driverName,
		GraphDriverOptions:        config.GraphOptions,
		UIDMaps:                   uidMaps,
		GIDMaps:                   gidMaps,
	})
	if err != nil {
		return nil, err
	}

	graphDriver := d.layerStore.DriverName()
	imageRoot := filepath.Join(config.Root, "image", graphDriver)

	// Configure and validate the kernels security support
	if err := configureKernelSecuritySupport(config, graphDriver); err != nil {
		return nil, err
	}

	logrus.Debugf("Max Concurrent Downloads: %d", *config.MaxConcurrentDownloads)
	d.downloadManager = xfer.NewLayerDownloadManager(d.layerStore, *config.MaxConcurrentDownloads)
	logrus.Debugf("Max Concurrent Uploads: %d", *config.MaxConcurrentUploads)
	d.uploadManager = xfer.NewLayerUploadManager(*config.MaxConcurrentUploads)

	ifs, err := image.NewFSStoreBackend(filepath.Join(imageRoot, "imagedb"))
	if err != nil {
		return nil, err
	}

	d.imageStore, err = image.NewImageStore(ifs, d.layerStore)
	if err != nil {
		return nil, err
	}

	// Configure the volumes driver
	volStore, err := configureVolumes(config, rootUID, rootGID)
	if err != nil {
		return nil, err
	}

	trustKey, err := api.LoadOrCreateTrustKey(config.TrustKeyPath)
	if err != nil {
		return nil, err
	}

	trustDir := filepath.Join(config.Root, "trust")

	if err := system.MkdirAll(trustDir, 0700); err != nil {
		return nil, err
	}

	distributionMetadataStore, err := dmetadata.NewFSMetadataStore(filepath.Join(imageRoot, "distribution"))
	if err != nil {
		return nil, err
	}

	eventsService := events.New()

	referenceStore, err := reference.NewReferenceStore(filepath.Join(imageRoot, "repositories.json"))
	if err != nil {
		return nil, fmt.Errorf("Couldn't create Tag store repositories: %s", err)
	}

	if err := restoreCustomImage(d.imageStore, d.layerStore, referenceStore); err != nil {
		return nil, fmt.Errorf("Couldn't restore custom images: %s", err)
	}

	migrationStart := time.Now()
	if err := v1.Migrate(config.Root, graphDriver, d.layerStore, d.imageStore, referenceStore, distributionMetadataStore); err != nil {
		logrus.Errorf("Graph migration failed: %q. Your old graph data was found to be too inconsistent for upgrading to content-addressable storage. Some of the old data was probably not upgraded. We recommend starting over with a clean storage directory if possible.", err)
	}
	logrus.Infof("Graph migration to content-addressability took %.2f seconds", time.Since(migrationStart).Seconds())

	// Discovery is only enabled when the daemon is launched with an address to advertise.  When
	// initialized, the daemon is registered and we can store the discovery backend as its read-only
	if err := d.initDiscovery(config); err != nil {
		return nil, err
	}

	d.netController, err = d.initNetworkController(config)
	if err != nil {
		return nil, fmt.Errorf("Error initializing network controller: %v", err)
	}

	sysInfo := sysinfo.New(false)
	// Check if Devices cgroup is mounted, it is hard requirement for container security,
	// on Linux/FreeBSD.
	if runtime.GOOS != "windows" && !sysInfo.CgroupDevicesEnabled {
		return nil, fmt.Errorf("Devices cgroup isn't mounted")
	}

	d.ID = trustKey.PublicKey().KeyID()
	d.repository = daemonRepo
	d.containers = container.NewMemoryStore()
	d.execCommands = exec.NewStore()
	d.referenceStore = referenceStore
	d.distributionMetadataStore = distributionMetadataStore
	d.trustKey = trustKey
	d.idIndex = truncindex.NewTruncIndex([]string{})
	d.statsCollector = d.newStatsCollector(1 * time.Second)
	d.defaultLogConfig = containertypes.LogConfig{
		Type:   config.LogConfig.Type,
		Config: config.LogConfig.Config,
	}
	d.RegistryService = registryService
	d.EventsService = eventsService
	d.volumes = volStore
	d.root = config.Root
	d.uidMaps = uidMaps
	d.gidMaps = gidMaps
	d.seccompEnabled = sysInfo.Seccomp

	d.nameIndex = registrar.NewRegistrar()
	d.linkIndex = newLinkIndex()

	go d.execCommandGC()

	d.containerd, err = containerdRemote.Client(d)
	if err != nil {
		return nil, err
	}

	if err := d.restore(); err != nil {
		return nil, err
	}

	return d, nil
}

// Shutdown stops the daemon.
func (daemon *Daemon) Shutdown() error {
	daemon.shutdown = true
	if daemon.containers != nil {
		logrus.Debug("starting clean shutdown of all containers...")
		daemon.containers.ApplyAll(func(c *container.Container) {
			if !c.IsRunning() {
				return
			}
			logrus.Debugf("stopping %s", c.ID)
			if err := daemon.shutdownContainer(c); err != nil {
				logrus.Errorf("Stop container error: %v", err)
				return
			}
			if mountid, err := daemon.layerStore.GetMountID(c.ID); err == nil {
				daemon.cleanupMountsByID(mountid)
			}
			logrus.Debugf("container stopped %s", c.ID)
		})
	}

	// trigger libnetwork Stop only if it's initialized
	if daemon.netController != nil {
		daemon.netController.Stop()
	}

	if daemon.layerStore != nil {
		if err := daemon.layerStore.Cleanup(); err != nil {
			logrus.Errorf("Error during layer Store.Cleanup(): %v", err)
		}
	}

	if err := daemon.cleanupMounts(); err != nil {
		return err
	}

	return nil
}

func writeDistributionProgress(cancelFunc func(), outStream io.Writer, progressChan <-chan progress.Progress) {
	progressOutput := streamformatter.NewJSONStreamFormatter().NewProgressOutput(outStream, false)
	operationCancelled := false

	for prog := range progressChan {
		if err := progressOutput.WriteProgress(prog); err != nil && !operationCancelled {
			// don't log broken pipe errors as this is the normal case when a client aborts
			if isBrokenPipe(err) {
				logrus.Info("Pull session cancelled")
			} else {
				logrus.Errorf("error writing progress to client: %v", err)
			}
			cancelFunc()
			operationCancelled = true
			// Don't return, because we need to continue draining
			// progressChan until it's closed to avoid a deadlock.
		}
	}
}

func isBrokenPipe(e error) bool {
	if netErr, ok := e.(*net.OpError); ok {
		e = netErr.Err
		if sysErr, ok := netErr.Err.(*os.SyscallError); ok {
			e = sysErr.Err
		}
	}
	return e == syscall.EPIPE
}

// GraphDriverName returns the name of the graph driver used by the layer.Store
func (daemon *Daemon) GraphDriverName() string {
	return daemon.layerStore.DriverName()
}

// GetUIDGIDMaps returns the current daemon's user namespace settings
// for the full uid and gid maps which will be applied to containers
// started in this instance.
func (daemon *Daemon) GetUIDGIDMaps() ([]idtools.IDMap, []idtools.IDMap) {
	return daemon.uidMaps, daemon.gidMaps
}

// GetRemappedUIDGID returns the current daemon's uid and gid values
// if user namespaces are in use for this daemon instance.  If not
// this function will return "real" root values of 0, 0.
func (daemon *Daemon) GetRemappedUIDGID() (int, int) {
	uid, gid, _ := idtools.GetRootUIDGID(daemon.uidMaps, daemon.gidMaps)
	return uid, gid
}

// tempDir returns the default directory to use for temporary files.
func tempDir(rootDir string, rootUID, rootGID int) (string, error) {
	var tmpDir string
	if tmpDir = os.Getenv("DOCKER_TMPDIR"); tmpDir == "" {
		tmpDir = filepath.Join(rootDir, "tmp")
	}
	return tmpDir, idtools.MkdirAllAs(tmpDir, 0700, rootUID, rootGID)
}

func (daemon *Daemon) setupInitLayer(initPath string) error {
	rootUID, rootGID := daemon.GetRemappedUIDGID()
	return setupInitLayer(initPath, rootUID, rootGID)
}

func configureVolumes(config *Config, rootUID, rootGID int) (*store.VolumeStore, error) {
	volumesDriver, err := local.New(config.Root, rootUID, rootGID)
	if err != nil {
		return nil, err
	}

	volumedrivers.Register(volumesDriver, volumesDriver.Name())
	return store.New(config.Root)
}

// AuthenticateToRegistry checks the validity of credentials in authConfig
func (daemon *Daemon) AuthenticateToRegistry(ctx context.Context, authConfig *types.AuthConfig) (string, string, error) {
	return daemon.RegistryService.Auth(authConfig, dockerversion.DockerUserAgent(ctx))
}

var acceptedSearchFilterTags = map[string]bool{
	"is-automated": true,
	"is-official":  true,
	"stars":        true,
}

// SearchRegistryForImages queries the registry for images matching
// term. authConfig is used to login.
func (daemon *Daemon) SearchRegistryForImages(ctx context.Context, filtersArgs string, term string,
	authConfig *types.AuthConfig,
	headers map[string][]string) (*registrytypes.SearchResults, error) {

	searchFilters, err := filters.FromParam(filtersArgs)
	if err != nil {
		return nil, err
	}
	if err := searchFilters.Validate(acceptedSearchFilterTags); err != nil {
		return nil, err
	}

	unfilteredResult, err := daemon.RegistryService.Search(term, authConfig, dockerversion.DockerUserAgent(ctx), headers)
	if err != nil {
		return nil, err
	}

	var isAutomated, isOfficial bool
	var hasStarFilter = 0
	if searchFilters.Include("is-automated") {
		if searchFilters.ExactMatch("is-automated", "true") {
			isAutomated = true
		} else if !searchFilters.ExactMatch("is-automated", "false") {
			return nil, fmt.Errorf("Invalid filter 'is-automated=%s'", searchFilters.Get("is-automated"))
		}
	}
	if searchFilters.Include("is-official") {
		if searchFilters.ExactMatch("is-official", "true") {
			isOfficial = true
		} else if !searchFilters.ExactMatch("is-official", "false") {
			return nil, fmt.Errorf("Invalid filter 'is-official=%s'", searchFilters.Get("is-official"))
		}
	}
	if searchFilters.Include("stars") {
		hasStars := searchFilters.Get("stars")
		for _, hasStar := range hasStars {
			iHasStar, err := strconv.Atoi(hasStar)
			if err != nil {
				return nil, fmt.Errorf("Invalid filter 'stars=%s'", hasStar)
			}
			if iHasStar > hasStarFilter {
				hasStarFilter = iHasStar
			}
		}
	}

	filteredResults := []registrytypes.SearchResult{}
	for _, result := range unfilteredResult.Results {
		if searchFilters.Include("is-automated") {
			if isAutomated != result.IsAutomated {
				continue
			}
		}
		if searchFilters.Include("is-official") {
			if isOfficial != result.IsOfficial {
				continue
			}
		}
		if searchFilters.Include("stars") {
			if result.StarCount < hasStarFilter {
				continue
			}
		}
		filteredResults = append(filteredResults, result)
	}

	return &registrytypes.SearchResults{
		Query:      unfilteredResult.Query,
		NumResults: len(filteredResults),
		Results:    filteredResults,
	}, nil
}

// IsShuttingDown tells whether the daemon is shutting down or not
func (daemon *Daemon) IsShuttingDown() bool {
	return daemon.shutdown
}

// GetContainerStats collects all the stats published by a container
func (daemon *Daemon) GetContainerStats(container *container.Container) (*types.StatsJSON, error) {
	stats, err := daemon.stats(container)
	if err != nil {
		return nil, err
	}

	if stats.Networks, err = daemon.getNetworkStats(container); err != nil {
		return nil, err
	}

	return stats, nil
}

// Resolve Network SandboxID in case the container reuse another container's network stack
func (daemon *Daemon) getNetworkSandboxID(c *container.Container) (string, error) {
	curr := c
	for curr.HostConfig.NetworkMode.IsContainer() {
		containerID := curr.HostConfig.NetworkMode.ConnectedContainer()
		connected, err := daemon.GetContainer(containerID)
		if err != nil {
			return "", fmt.Errorf("Could not get container for %s", containerID)
		}
		curr = connected
	}
	return curr.NetworkSettings.SandboxID, nil
}

func (daemon *Daemon) getNetworkStats(c *container.Container) (map[string]types.NetworkStats, error) {
	sandboxID, err := daemon.getNetworkSandboxID(c)
	if err != nil {
		return nil, err
	}

	sb, err := daemon.netController.SandboxByID(sandboxID)
	if err != nil {
		return nil, err
	}

	lnstats, err := sb.Statistics()
	if err != nil {
		return nil, err
	}

	stats := make(map[string]types.NetworkStats)
	// Convert libnetwork nw stats into engine-api stats
	for ifName, ifStats := range lnstats {
		stats[ifName] = types.NetworkStats{
			RxBytes:   ifStats.RxBytes,
			RxPackets: ifStats.RxPackets,
			RxErrors:  ifStats.RxErrors,
			RxDropped: ifStats.RxDropped,
			TxBytes:   ifStats.TxBytes,
			TxPackets: ifStats.TxPackets,
			TxErrors:  ifStats.TxErrors,
			TxDropped: ifStats.TxDropped,
		}
	}

	return stats, nil
}

// newBaseContainer creates a new container with its initial
// configuration based on the root storage from the daemon.
func (daemon *Daemon) newBaseContainer(id string) *container.Container {
	return container.NewBaseContainer(id, daemon.containerRoot(id))
}

// initDiscovery initializes the discovery watcher for this daemon.
func (daemon *Daemon) initDiscovery(config *Config) error {
	advertise, err := parseClusterAdvertiseSettings(config.ClusterStore, config.ClusterAdvertise)
	if err != nil {
		if err == errDiscoveryDisabled {
			return nil
		}
		return err
	}

	config.ClusterAdvertise = advertise
	discoveryWatcher, err := initDiscovery(config.ClusterStore, config.ClusterAdvertise, config.ClusterOpts)
	if err != nil {
		return fmt.Errorf("discovery initialization failed (%v)", err)
	}

	daemon.discoveryWatcher = discoveryWatcher
	return nil
}

// Reload reads configuration changes and modifies the
// daemon according to those changes.
// These are the settings that Reload changes:
// - Daemon labels.
// - Daemon debug log level.
// - Daemon max concurrent downloads
// - Daemon max concurrent uploads
// - Cluster discovery (reconfigure and restart).
func (daemon *Daemon) Reload(config *Config) error {
	daemon.configStore.reloadLock.Lock()
	defer daemon.configStore.reloadLock.Unlock()
	if config.IsValueSet("labels") {
		daemon.configStore.Labels = config.Labels
	}
	if config.IsValueSet("debug") {
		daemon.configStore.Debug = config.Debug
	}

	// If no value is set for max-concurrent-downloads we assume it is the default value
	// We always "reset" as the cost is lightweight and easy to maintain.
	if config.IsValueSet("max-concurrent-downloads") && config.MaxConcurrentDownloads != nil {
		*daemon.configStore.MaxConcurrentDownloads = *config.MaxConcurrentDownloads
	} else {
		maxConcurrentDownloads := defaultMaxConcurrentDownloads
		daemon.configStore.MaxConcurrentDownloads = &maxConcurrentDownloads
	}
	logrus.Debugf("Reset Max Concurrent Downloads: %d", *daemon.configStore.MaxConcurrentDownloads)
	if daemon.downloadManager != nil {
		daemon.downloadManager.SetConcurrency(*daemon.configStore.MaxConcurrentDownloads)
	}

	// If no value is set for max-concurrent-upload we assume it is the default value
	// We always "reset" as the cost is lightweight and easy to maintain.
	if config.IsValueSet("max-concurrent-uploads") && config.MaxConcurrentUploads != nil {
		*daemon.configStore.MaxConcurrentUploads = *config.MaxConcurrentUploads
	} else {
		maxConcurrentUploads := defaultMaxConcurrentUploads
		daemon.configStore.MaxConcurrentUploads = &maxConcurrentUploads
	}
	logrus.Debugf("Reset Max Concurrent Uploads: %d", *daemon.configStore.MaxConcurrentUploads)
	if daemon.uploadManager != nil {
		daemon.uploadManager.SetConcurrency(*daemon.configStore.MaxConcurrentUploads)
	}

	return daemon.reloadClusterDiscovery(config)
}

func (daemon *Daemon) reloadClusterDiscovery(config *Config) error {
	var err error
	newAdvertise := daemon.configStore.ClusterAdvertise
	newClusterStore := daemon.configStore.ClusterStore
	if config.IsValueSet("cluster-advertise") {
		if config.IsValueSet("cluster-store") {
			newClusterStore = config.ClusterStore
		}
		newAdvertise, err = parseClusterAdvertiseSettings(newClusterStore, config.ClusterAdvertise)
		if err != nil && err != errDiscoveryDisabled {
			return err
		}
	}

	// check discovery modifications
	if !modifiedDiscoverySettings(daemon.configStore, newAdvertise, newClusterStore, config.ClusterOpts) {
		return nil
	}

	// enable discovery for the first time if it was not previously enabled
	if daemon.discoveryWatcher == nil {
		discoveryWatcher, err := initDiscovery(newClusterStore, newAdvertise, config.ClusterOpts)
		if err != nil {
			return fmt.Errorf("discovery initialization failed (%v)", err)
		}
		daemon.discoveryWatcher = discoveryWatcher
	} else {
		if err == errDiscoveryDisabled {
			// disable discovery if it was previously enabled and it's disabled now
			daemon.discoveryWatcher.Stop()
		} else {
			// reload discovery
			if err = daemon.discoveryWatcher.Reload(config.ClusterStore, newAdvertise, config.ClusterOpts); err != nil {
				return err
			}
		}
	}

	daemon.configStore.ClusterStore = newClusterStore
	daemon.configStore.ClusterOpts = config.ClusterOpts
	daemon.configStore.ClusterAdvertise = newAdvertise

	if daemon.netController == nil {
		return nil
	}
	netOptions, err := daemon.networkOptions(daemon.configStore)
	if err != nil {
		logrus.Warnf("Failed to reload configuration with network controller: %v", err)
		return nil
	}
	err = daemon.netController.ReloadConfiguration(netOptions...)
	if err != nil {
		logrus.Warnf("Failed to reload configuration with network controller: %v", err)
	}

	return nil
}

func validateID(id string) error {
	if id == "" {
		return fmt.Errorf("Invalid empty id")
	}
	return nil
}

func isBridgeNetworkDisabled(config *Config) bool {
	return config.bridgeConfig.Iface == disableNetworkBridge
}

func (daemon *Daemon) networkOptions(dconfig *Config) ([]nwconfig.Option, error) {
	options := []nwconfig.Option{}
	if dconfig == nil {
		return options, nil
	}

	options = append(options, nwconfig.OptionDataDir(dconfig.Root))

	dd := runconfig.DefaultDaemonNetworkMode()
	dn := runconfig.DefaultDaemonNetworkMode().NetworkName()
	options = append(options, nwconfig.OptionDefaultDriver(string(dd)))
	options = append(options, nwconfig.OptionDefaultNetwork(dn))

	if strings.TrimSpace(dconfig.ClusterStore) != "" {
		kv := strings.Split(dconfig.ClusterStore, "://")
		if len(kv) != 2 {
			return nil, fmt.Errorf("kv store daemon config must be of the form KV-PROVIDER://KV-URL")
		}
		options = append(options, nwconfig.OptionKVProvider(kv[0]))
		options = append(options, nwconfig.OptionKVProviderURL(kv[1]))
	}
	if len(dconfig.ClusterOpts) > 0 {
		options = append(options, nwconfig.OptionKVOpts(dconfig.ClusterOpts))
	}

	if daemon.discoveryWatcher != nil {
		options = append(options, nwconfig.OptionDiscoveryWatcher(daemon.discoveryWatcher))
	}

	if dconfig.ClusterAdvertise != "" {
		options = append(options, nwconfig.OptionDiscoveryAddress(dconfig.ClusterAdvertise))
	}

	options = append(options, nwconfig.OptionLabels(dconfig.Labels))
	options = append(options, driverOptions(dconfig)...)
	return options, nil
}

func copyBlkioEntry(entries []*containerd.BlkioStatsEntry) []types.BlkioStatEntry {
	out := make([]types.BlkioStatEntry, len(entries))
	for i, re := range entries {
		out[i] = types.BlkioStatEntry{
			Major: re.Major,
			Minor: re.Minor,
			Op:    re.Op,
			Value: re.Value,
		}
	}
	return out
}
