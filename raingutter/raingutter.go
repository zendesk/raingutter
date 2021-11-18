package main

import (
	"bufio"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/DataDog/datadog-go/statsd"
	log "github.com/sirupsen/logrus"
)

var (
	execCommand  = exec.Command
	version      string
	podName      = os.Getenv("POD_NAME")
	podNameSpace = os.Getenv("POD_NAMESPACE")
	project      = os.Getenv("PROJECT")
)

type raingutter struct {
	Calling uint64
	Writing uint64
	Active  uint64
	Queued  uint64
}

type status struct {
	Ready bool
}

type totalConnections struct {
	Count uint64
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

func getThreads(tc *totalConnections) {
	maxThreads := os.Getenv("MAX_THREADS")
	if maxThreads == "" {
		log.Fatal("MAX_THREADS is not defined.")
	}
	threads, err := strconv.ParseUint(maxThreads, 10, 64)
	checkFatal(err)
	tc.Count = threads
}

func getWorkers(tc *totalConnections) {
	var (
		workers uint64
		cmdOut  []byte
		err     error
	)
	unicornWorkers := os.Getenv("UNICORN_WORKERS")
	// If the UNICORN_WORKERS env var is empty fallback on pgrep
	// This is meant to be used on VM or bare metal
	if unicornWorkers == "" {
		binary, lookErr := exec.LookPath("pgrep")
		checkFatal(lookErr)
		args := []string{"-fc", "helper.sh"}
		if cmdOut, err = execCommand(binary, args...).CombinedOutput(); err != nil {
			log.Error(binary, " returned: ", err)
			workers = 0
			log.Warn("Unicorn workers count set to 0")
			tc.Count = workers
		} else {
			out := string(cmdOut)
			unicorns, err := strconv.ParseUint(strings.TrimSpace(out), 10, 64)
			checkFatal(err)
			// remove the master from the total running unicorns
			tc.Count = unicorns - 1
		}
	} else {
		workers, err := strconv.ParseUint(unicornWorkers, 10, 64)
		checkFatal(err)
		tc.Count = uint64(workers)
	}
}

// Fetch the raindrops output and convert it to a slice on strings
func Fetch(c http.Client, url string, s *status) *http.Response {
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
func (r *raingutter) sendStats(c *statsd.Client, tc *totalConnections, useThreads string) {
	// calling: int
	// writing: int
	//
	// Middleware response includes extra stats for bound TCP and Unix domain sockets
	// <UNIX or TCP SOCKET> active: int
	// <UNIX or TCP SOCKET> queued: int

	// calling - the number of application dispatchers on your machine
	err := c.Histogram("calling", float64(r.Calling), nil, 1)
	checkError(err)
	// writing - the number of clients being written to on your machine
	err = c.Histogram("writing", float64(r.Writing), nil, 1)
	checkError(err)
	// queued - total number of queued (pre-accept()) clients on that listener
	err = c.Histogram("queued", float64(r.Queued), nil, 1)
	checkError(err)
	// active - total number of active clients on that listener
	err = c.Histogram("active", float64(r.Active), nil, 1)
	checkError(err)
	if useThreads == "true" {
		// threads.count - total number of allowed threads
		err = c.Histogram("threads.count", float64(tc.Count), nil, 1)
		checkError(err)
	} else {
		// worker.count - total number of provisioned workers
		err = c.Histogram("worker.count", float64(tc.Count), nil, 1)
		checkError(err)
	}
}

func (r *raingutter) logMetrics(tc *totalConnections, raindropsURL string) {
	contextLogger := log.WithFields(log.Fields{
		"active":  r.Active,
		"queued":  r.Queued,
		"writing": r.Writing,
		"calling": r.Calling,
		"workers": tc.Count,
	})
	contextLogger.Info(raindropsURL)
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
		socketStatsMode = "netlink"
	}
	if socketStatsMode != "netlink" && socketStatsMode != "proc_net" && socketStatsMode != "raindrops" {
		log.Fatalf("Invalid value for RG_NET_POLL_MODE %s (should be netlink, proc_net, or raindrops)", socketStatsMode)
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
	log.Info("RG_FREQUENCY: ", frequency)
	freqInt, _ := strconv.Atoi(frequency)

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

	// Create an http client
	timeout := time.Duration(3 * time.Second)
	httpClient := http.Client{
		Timeout: timeout,
	}

	// Create a statsd udp client
	statsdURL := statsdHost + ":" + statsdPort
	statsdClient, err := statsd.New(statsdURL)
	if err != nil {
		log.Error(err)
	}

	// Define namespace
	statsdClient.Namespace = statsdNamespace

	// Add k8s tags
	if podName != "" {
		tag := "pod_name:" + podName
		statsdClient.Tags = append(statsdClient.Tags, tag)
	}

	if podNameSpace != "" {
		tag := "pod_namespace:" + podNameSpace
		statsdClient.Tags = append(statsdClient.Tags, tag)
	}

	if project != "" {
		tag := "project:" + project
		statsdClient.Tags = append(statsdClient.Tags, tag)
	}

	// Add extra tags
	if statsdExtraTags != "" {
		tags := strings.Split(statsdExtraTags, ",")
		statsdClient.Tags = append(statsdClient.Tags, tags...)
	}

	// Setup os signals catching
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		s := <-sigs
		log.Fatal("Received signal: ", s)
	}()

	tc := totalConnections{Count: 0}
	if useThreads == "true" {
		getThreads(&tc)

	} else {
		go func() {
			for {
				getWorkers(&tc)
				// sleep for a minute
				<-time.After(60 * time.Second)
			}
		}()
	}

	var rnlc *RaingutterNetlinkConnection
	if socketStatsMode == "netlink" {
		rnlc, err = NewRaingutterNetlinkConnection()
		if err != nil {
			log.Fatal("error creating netlink connection: ", err)
		}
		defer rnlc.Close()
	}

	readiness := status{Ready: false}
	for {
		didScan := false

		time.Sleep(time.Millisecond * time.Duration(freqInt))

		switch socketStatsMode {
		case "proc_net":
			rawStats, err := GetSocketStats(procDir)
			if err != nil {
				log.Error(err)
			}

			stats, err := ParseSocketStats(serverPort, rawStats)
			if err != nil {
				log.Error(err)
			} else {
				r.ScanSocketStats(stats)
				didScan = true
			}
		case "raindrops":
			// if SocketStats is disabled, raingutter will use the raindrops endpoint
			// to retrieve metrics from the unicorn master
			body := Fetch(httpClient, raindropsURL, &readiness)
			if body != nil {
				r.Scan(body)
				didScan = true
			}
		case "netlink":
			stats, err := rnlc.ReadStats(serverPortShort)
			if err != nil {
				log.Error(err)
			} else {
				r.ScanSocketStats(&stats)
				didScan = true
			}
		}

		if didScan {
			if statsdEnabled == "true" {
				r.sendStats(statsdClient, &tc, useThreads)
			}
			if prometheusEnabled == "true" {
				r.recordMetrics(&tc, useThreads)
			}
			if logMetricsEnabled == "true" {
				r.logMetrics(&tc, raindropsURL)
			}
		}
	}

}
