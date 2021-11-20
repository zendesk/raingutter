package main

import (
	"bufio"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/DataDog/datadog-go/statsd"
	log "github.com/sirupsen/logrus"
)

var (
	version      string
	podName      = os.Getenv("POD_NAME")
	podNameSpace = os.Getenv("POD_NAMESPACE")
	project      = os.Getenv("PROJECT")
)

type raingutter struct {
	Calling             uint64
	Writing             uint64
	Active              uint64
	Queued              uint64
	ListenerSocketInode uint64

	raindropsStatus status
	workerCount     int
	serverProcesses *ServerProcessCollection

	socketStatsMode    string
	rnlc               *RaingutterNetlinkConnection
	httpClient         *http.Client
	procDir            string
	serverPort         uint16
	raindropsURL       string
	memoryStatsEnabled bool
	statsdEnabled      bool
	prometheusEnabled  bool
	logMetricsEnabled  bool
	statsdClient       *statsd.Client
	useThreads         bool
	staticWorkerCount  int
	workerCountMode    string
}

type status struct {
	Ready bool
}

func checkError(err error) {
	if err != nil {
		log.Error(err)
	}
}

func checkFatal(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

// Fetch the raindrops output and convert it to a slice on strings
func Fetch(c *http.Client, url string, s *status) *http.Response {
	request, err := http.NewRequest("GET", url, nil)
	if err != nil {
		log.Error(err)
	}
	request.Header.Set("User-Agent", "raingutter")
	response, err := c.Do(request)
	if err != nil {
		if !s.Ready {
			log.Warn(url, " is not ready yet")
		} else {
			log.Error(err)
		}
		return nil
	}
	if response.StatusCode != 200 {
		log.Error("raindrops return code is: ", response.StatusCode)
		return nil
	}
	if !s.Ready {
		s.Ready = true
	}
	return response
}

// Parse converts a slice to float64
func Parse(l string) uint64 {
	// get the value after the last ":"
	splitted := strings.Split(l, ":")[len(strings.Split(l, ":"))-1]
	// trim space and parse the int
	value, err := strconv.ParseUint(strings.TrimSpace(splitted), 10, 64)
	checkFatal(err)
	return value
}

func (r *raingutter) Scan(response *http.Response) raingutter {
	scanner := bufio.NewScanner(response.Body)
	defer response.Body.Close()
	for scanner.Scan() {
		l := scanner.Text()
		switch {
		case strings.Contains(l, "active"):
			r.Active = Parse(l)
		case strings.Contains(l, "writing"):
			r.Writing = Parse(l)
		case strings.Contains(l, "calling"):
			r.Calling = Parse(l)
		case strings.Contains(l, "queued"):
			r.Queued = Parse(l)
		default:
			continue
		}
	}
	return *r
}

func (r *raingutter) ScanSocketStats(s *SocketStats) raingutter {
	// `writing` and `calling` are not yet implemented
	r.Active = s.ActiveWorkers
	r.Queued = s.QueueSize
	r.ListenerSocketInode = s.ListenerInode
	return *r
}

// The histogram interface calculates the statistical distribution of any kind of value
// and it generates:
//  - 95percentile,
//  - max,
//  - median,
//  - avg,
//  - count
//
// according to what's specified in /etc/dd-agent/datadog.conf
//
// https://docs.datadoghq.com/guides/dogstatsd/
func (r *raingutter) sendSocketStats() {
	// calling: int
	// writing: int
	//
	// Middleware response includes extra stats for bound TCP and Unix domain sockets
	// <UNIX or TCP SOCKET> active: int
	// <UNIX or TCP SOCKET> queued: int

	// calling - the number of application dispatchers on your machine
	err := r.statsdClient.Histogram("calling", float64(r.Calling), nil, 1)
	checkError(err)
	// writing - the number of clients being written to on your machine
	err = r.statsdClient.Histogram("writing", float64(r.Writing), nil, 1)
	checkError(err)
	// queued - total number of queued (pre-accept()) clients on that listener
	err = r.statsdClient.Histogram("queued", float64(r.Queued), nil, 1)
	checkError(err)
	// active - total number of active clients on that listener
	err = r.statsdClient.Histogram("active", float64(r.Active), nil, 1)
	checkError(err)
}

func (r *raingutter) sendWorkerStats() {
	if r.useThreads {
		// threads.count - total number of allowed threads
		err := r.statsdClient.Histogram("threads.count", float64(r.workerCount), nil, 1)
		checkError(err)
	} else {
		// worker.count - total number of provisioned workers
		err := r.statsdClient.Histogram("worker.count", float64(r.workerCount), nil, 1)
		checkError(err)

		// Emit memory statistics, if available
		if r.serverProcesses != nil && r.memoryStatsEnabled {
			for _, proc := range r.serverProcesses.Processes {
				if !proc.IsMaster && proc.Index == -1 {
					// This is a very unlikely but possible race condition, where the unicorn master
					// has forked but the child has not yet changed /proc/self/cmdline, do we don't
					// know its index yet. Just skip reporting memory for this.
					continue
				}
				tags := []string{
					fmt.Sprintf("ismaster:%t", proc.IsMaster),
					fmt.Sprintf("index:%d", proc.Index),
				}

				err = r.statsdClient.Distribution("process.rss", float64(proc.RSS), tags, 1)
				checkError(err)
				err = r.statsdClient.Distribution("process.pss", float64(proc.PSS), tags, 1)
				checkError(err)
				err = r.statsdClient.Distribution("process.anon", float64(proc.Anon), tags, 1)
				checkError(err)
			}
		}
	}
}

func (r *raingutter) logSocketMetrics() {
	contextLogger := log.WithFields(log.Fields{
		"active":  r.Active,
		"queued":  r.Queued,
		"writing": r.Writing,
		"calling": r.Calling,
		"workers": r.workerCount,
	})
	contextLogger.Info(r.raindropsURL)
}

func (r *raingutter) logWorkerMetrics() {
	contextLogger := log.WithFields(log.Fields{
		"workers": r.workerCount,
	})
	contextLogger.Info(r.raindropsURL)
}

func main() {
	log.SetFormatter(&log.JSONFormatter{})
	log.SetOutput(os.Stdout)
	log.SetLevel(log.InfoLevel)

	boolVersion := flag.Bool("v", false, "Return the version")
	flag.Parse()

	if *boolVersion {
		fmt.Println("v" + version)
		os.Exit(0)
	}

	useThreads := os.Getenv("RG_THREADS")
	if useThreads == "" {
		log.Warning("RG_THREADS is not defined. Set to false by default")
		useThreads = "false"
	}
	log.Info("RG_THREADS: ", useThreads)

	// Whether to use /proc/net/tcp or netlink or raindrops to get socket stats
	socketStatsMode := os.Getenv("RG_SOCKET_STATS_MODE")
	if socketStatsMode == "" {
		useSocketStats := os.Getenv("RG_USE_SOCKET_STATS")
		if useSocketStats != "" {
			log.Warning("RG_USE_SOCKET_STATS is deprecated; set RG_SOCKET_STATS_MODE to obtain the same effect")
			if strings.ToLower(useSocketStats) == "true" {
				socketStatsMode = "proc_net"
			} else {
				socketStatsMode = "raindrops"
			}
		}
		socketStatsMode = "proc_net"
	}
	if socketStatsMode != "netlink" && socketStatsMode != "proc_net" && socketStatsMode != "raindrops" {
		log.Fatalf("Invalid value for RG_SOCKET_STATS_MODE %s (should be netlink, proc_net, or raindrops)", socketStatsMode)
	}
	log.Info("RG_SOCKET_STATS_MODE: ", socketStatsMode)

	statsdEnabled := os.Getenv("RG_STATSD_ENABLED")
	if statsdEnabled == "" {
		log.Warning("RG_STATSD_ENABLED is not defined. Set to true by default")
		statsdEnabled = "true"
	}

	logMetricsEnabled := os.Getenv("RG_LOG_METRICS_ENABLED")
	if logMetricsEnabled == "" {
		log.Warning("RG_LOG_METRICS_ENABLED is not defined. Set to false by default")
		logMetricsEnabled = "false"
	}

	prometheusEnabled := os.Getenv("RG_PROMETHEUS_ENABLED")
	if prometheusEnabled == "" {
		log.Warning("RG_PROMETHEUS_ENABLED is not defined. Set to false by default")
		prometheusEnabled = "false"
	} else if prometheusEnabled == "true" {
		setupPrometheus()
	}

	raindropsURL := os.Getenv("RG_RAINDROPS_URL")
	if raindropsURL == "" {
		if useThreads == "false" && socketStatsMode == "raindrops" {
			log.Fatal("RG_RAINDROPS_URL is missing")
		}
	} else {
		log.Info("RG_RAINDROPS_URL: ", raindropsURL)
	}

	statsdHost := os.Getenv("RG_STATSD_HOST")
	if statsdHost == "" {
		log.Warning("RG_STATSD_HOST is missing")
	}
	log.Info("RG_STATSD_HOST: ", statsdHost)

	statsdPort := os.Getenv("RG_STATSD_PORT")
	if statsdPort == "" {
		log.Warning("RG_STATSD_PORT is missing")
	}
	log.Info("RG_STATSD_PORT: ", statsdPort)

	statsdNamespace := os.Getenv("RG_STATSD_NAMESPACE")
	if statsdNamespace == "" {
		log.Warning("RG_STATSD_NAMESPACE is not defined. Using 'unicorn.raingutter.agg.'")
		statsdNamespace = "unicorn.raingutter.agg."
	}
	log.Info("RG_STATSD_NAMESPACE: ", statsdNamespace)

	statsdExtraTags := os.Getenv("RG_STATSD_EXTRA_TAGS")

	serverPort := os.Getenv("RG_SERVER_PORT")
	if serverPort == "" {
		serverPort = "3000"
		log.Warning("RG_SERVER_PORT is not defined. Set to 3000 by default")
	} else {
		log.Info("RG_SERVER_PORT: ", serverPort)
	}
	serverPortInt, err := strconv.Atoi(serverPort)
	if err != nil {
		log.Fatalf("Could not parse RG_SERVER_PORT %s: %s", serverPort, err)
	}
	serverPortShort := uint16(serverPortInt)

	procDir := os.Getenv("RG_PROC_DIRECTORY")
	if procDir == "" {
		procDir = "/proc"
	}
	log.Info("RG_PROC_DIRECTORY: ", procDir)

	// raingutter polling frequency expressed in ms
	frequency := os.Getenv("RG_FREQUENCY")
	if frequency == "" {
		frequency = "500"
	}
	freqInt, err := strconv.Atoi(frequency)
	if err != nil {
		log.Fatalf("Could not parse RG_FREQUENCY %s: %s", frequency, err)
	}
	log.Info("RG_FREQUENCY: ", freqInt)

	workerFrequencyStr := os.Getenv("RG_FREQUENCY_WORKER")
	if frequency == "" {
		frequency = "60000" // The old default for pgrep'ing.
	}
	workerFrequencyInt, err := strconv.Atoi(workerFrequencyStr)
	if err != nil {
		log.Fatalf("Could not parse RG_FREQUENCY_WORKER %s: %s", workerFrequencyStr, err)
	}
	log.Info("RG_FREQUENCY_WORKER: ", workerFrequencyInt)

	memoryStatsEnabledStr := os.Getenv("RG_MEMORY_STATS_ENABLED")
	memoryStatsEnabled := false
	if strings.ToLower(memoryStatsEnabledStr) == "true" {
		memoryStatsEnabled = true
	}

	if podName == "" {
		log.Warn("POD_NAME is missing")
	}

	if podNameSpace == "" {
		log.Warn("POD_NAMESPACE is missing")
	}

	if project == "" {
		log.Warn("PROJECT is missing")
	}

	r := raingutter{}
	r.socketStatsMode = socketStatsMode
	r.procDir = procDir
	r.serverPort = serverPortShort
	r.raindropsURL = raindropsURL
	r.memoryStatsEnabled = memoryStatsEnabled
	r.statsdEnabled = statsdEnabled == "true"
	r.prometheusEnabled = prometheusEnabled == "true"
	r.logMetricsEnabled = logMetricsEnabled == "true"
	r.useThreads = useThreads == "true"

	if r.useThreads {
		threadCountStr := os.Getenv("MAX_THREADS")
		if threadCountStr != "" {
			r.staticWorkerCount, err = strconv.Atoi(threadCountStr)
			if err != nil {
				log.Fatalf("Could not parse MAX_THREADS %s: %s", threadCountStr, err)
			}
			log.Infof("MAX_THREADS: %d", r.staticWorkerCount)
		}
	} else {
		unicornWorkerStr := os.Getenv("UNICORN_WORKERS")
		if unicornWorkerStr != "" {
			r.staticWorkerCount, err = strconv.Atoi(unicornWorkerStr)
			if err != nil {
				log.Fatalf("Could not parse UNICORN_WORKERS %s: %s", unicornWorkerStr, err)
			}
			log.Infof("UNICORN_WORKERS: %d", r.staticWorkerCount)
		}
	}

	r.workerCountMode = os.Getenv("RG_WORKER_COUNT_MODE")
	if r.workerCountMode == "" {
		// For backwards compatability, the default mode is socket_inode if MAX_THREADS
		// or UNICORN_WORKERS is not specified
		if r.staticWorkerCount == 0 {
			r.workerCountMode = "socket_inode"
		} else {
			r.workerCountMode = "static"
		}
	}
	if r.workerCountMode != "socket_inode" && r.workerCountMode != "static" {
		log.Fatalf("Invalid value for RG_WORKER_COUNT_MODE %s (should be socket_inode or static)", r.workerCountMode)
	}

	// Create an http client
	r.httpClient = &http.Client{
		Timeout: 3 * time.Second,
	}

	if socketStatsMode == "netlink" {
		r.rnlc, err = NewRaingutterNetlinkConnection()
		if err != nil {
			log.Fatal("error creating netlink connection: ", err)
		}
		defer r.rnlc.Close()
	}

	// Create a statsd udp client
	statsdURL := statsdHost + ":" + statsdPort
	r.statsdClient, err = statsd.New(statsdURL)
	if err != nil {
		log.Error(err)
	}

	// Define namespace
	r.statsdClient.Namespace = statsdNamespace

	// Add k8s tags
	if podName != "" {
		tag := "pod_name:" + podName
		r.statsdClient.Tags = append(r.statsdClient.Tags, tag)
	}

	if podNameSpace != "" {
		tag := "pod_namespace:" + podNameSpace
		r.statsdClient.Tags = append(r.statsdClient.Tags, tag)
	}

	if project != "" {
		tag := "project:" + project
		r.statsdClient.Tags = append(r.statsdClient.Tags, tag)
	}

	// Add extra tags
	if statsdExtraTags != "" {
		tags := strings.Split(statsdExtraTags, ",")
		r.statsdClient.Tags = append(r.statsdClient.Tags, tags...)
	}

	// Setup os signals catching
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)

	workerMetricsTicker := time.NewTicker(time.Duration(workerFrequencyInt) * time.Millisecond)
	defer workerMetricsTicker.Stop()
	socketMetricsTicker := time.NewTicker(time.Duration(freqInt) * time.Millisecond)
	defer socketMetricsTicker.Stop()

	defer func() {
		if r.serverProcesses != nil {
			r.serverProcesses.Close()
		}
	}()

	// Prime worker metrics once - because the socket metrics divide some numbers by the
	// total number of workers.
	r.collectAndEmitWorkerMetrics()

mainloop:
	for {
		select {
		case <-workerMetricsTicker.C:
			r.collectAndEmitWorkerMetrics()
		case <-socketMetricsTicker.C:
			r.collectAndEmitSocketMetrics()
		case s := <-sigs:
			log.Infof("received signal %s; exiting", s.String())
			break mainloop
		}
	}
}

func (r *raingutter) collectAndEmitSocketMetrics() {
	didScan := false
	switch r.socketStatsMode {
	case "proc_net":
		rawStats, err := GetSocketStats(r.procDir)
		if err != nil {
			log.Error(err)
		}

		stats, err := ParseSocketStats(r.serverPort, rawStats)
		if err != nil {
			log.Error(err)
		} else {
			r.ScanSocketStats(stats)
			didScan = true
		}
	case "raindrops":
		// if SocketStats is disabled, raingutter will use the raindrops endpoint
		// to retrieve metrics from the unicorn master
		body := Fetch(r.httpClient, r.raindropsURL, &r.raindropsStatus)
		if body != nil {
			r.Scan(body)
			didScan = true
		}
	case "netlink":
		stats, err := r.rnlc.ReadStats(r.serverPort)
		if err != nil {
			log.Error(err)
		} else {
			r.ScanSocketStats(&stats)
			didScan = true
		}
	}

	if didScan {
		if r.statsdEnabled {
			r.sendSocketStats()
		}
		if r.prometheusEnabled {
			r.recordSocketMetrics()
		}
		if r.logMetricsEnabled {
			r.logSocketMetrics()
		}
	}
}

func (r *raingutter) collectAndEmitWorkerMetrics() {
	switch r.workerCountMode {
	case "static":
		r.workerCount = r.staticWorkerCount
	case "socket_inode":
		if r.serverProcesses != nil {
			r.serverProcesses.Close()
		}
		var err error
		r.serverProcesses, err = FindProcessesListeningToSocket(r.procDir, r.ListenerSocketInode)
		if err != nil {
			log.Error(err)
			return
		}
		r.workerCount = r.serverProcesses.workerCount()
		if r.memoryStatsEnabled {
			r.serverProcesses.collectMemoryStats()
		}
	}

	if r.statsdEnabled {
		r.sendWorkerStats()
	}
	if r.prometheusEnabled {
		r.recordWorkerMetrics()
	}
	if r.logMetricsEnabled {
		r.logWorkerMetrics()
	}
}
