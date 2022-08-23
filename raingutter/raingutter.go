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

	"github.com/DataDog/datadog-go/v5/statsd"
	log "github.com/sirupsen/logrus"
)

var (
	execCommand  = exec.Command
	version      string
	podName      = os.Getenv("POD_NAME")
	podNameSpace = os.Getenv("POD_NAMESPACE")
	project      = os.Getenv("PROJECT")
	statsdTags   []string
)

type raingutter struct {
	Calling float64
	Writing float64
	Active  float64
	Queued  float64
}

type status struct {
	Ready bool
}

type totalConnections struct {
	Count float64
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
	var (
		threads float64
	)
	maxThreads := os.Getenv("MAX_THREADS")
	if maxThreads == "" {
		log.Fatal("MAX_THREADS is not defined.")
	}
	threads, err := strconv.ParseFloat(maxThreads, 64)
	checkFatal(err)
	tc.Count = threads
}

func getWorkers(tc *totalConnections) {
	var (
		workers float64
		err     error
	)
	unicornWorkers := os.Getenv("UNICORN_WORKERS")
	workers, err = strconv.ParseFloat(unicornWorkers, 64)
	checkFatal(err)
	tc.Count = workers
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
func Parse(l string) float64 {
	// get the value after the last ":"
	splitted := strings.Split(l, ":")[len(strings.Split(l, ":"))-1]
	// trim space and parse the int
	value, err := strconv.ParseFloat(strings.TrimSpace(splitted), 64)
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
	err := c.Histogram("calling", r.Calling, nil, 1)
	checkError(err)
	// writing - the number of clients being written to on your machine
	err = c.Histogram("writing", r.Writing, nil, 1)
	checkError(err)
	// queued - total number of queued (pre-accept()) clients on that listener
	err = c.Histogram("queued", r.Queued, nil, 1)
	checkError(err)
	// active - total number of active clients on that listener
	err = c.Histogram("active", r.Active, nil, 1)
	checkError(err)
	if useThreads == "true" {
		// threads.count - total number of allowed threads
		err = c.Histogram("threads.count", tc.Count, nil, 1)
		checkError(err)
	} else {
		// worker.count - total number of provisioned workers
		err = c.Histogram("worker.count", tc.Count, nil, 1)
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

	useSocketStats := os.Getenv("RG_USE_SOCKET_STATS")
	if useSocketStats == "" {
		log.Warning("RG_USE_SOCKET_STATS is not defined. Set to true by default")
		useSocketStats = "true"
	}
	log.Info("RG_USE_SOCKET_STATS: ", useSocketStats)

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
		if useThreads == "false" && useSocketStats == "false" {
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

	// Add k8s tags
	if podName != "" {
		tag := "pod_name:" + podName
		statsdTags = append(statsdTags, tag)
	}

	if podNameSpace != "" {
		tag := "pod_namespace:" + podNameSpace
		statsdTags = append(statsdTags, tag)
	}

	if project != "" {
		tag := "project:" + project
		statsdTags = append(statsdTags, tag)
	}

	// Add extra tags
	if statsdExtraTags != "" {
		tags := strings.Split(statsdExtraTags, ",")
		statsdTags = append(statsdTags, tags...)
	}

	// Create an http client
	timeout := time.Duration(3 * time.Second)
	httpClient := http.Client{
		Timeout: timeout,
	}

	// Create a statsd udp client
	statsdURL := statsdHost + ":" + statsdPort
	statsdClient, err := statsd.New(statsdURL, statsd.WithNamespace(statsdNamespace), statsd.WithTags(statsdTags))
	if err != nil {
		log.Error(err)
	}

	// Setup os signals catching
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		s := <-sigs
		log.Info("Received signal: ", s)
		os.Exit(0)
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

	readiness := status{Ready: false}
	for {
		didScan := false

		time.Sleep(time.Millisecond * time.Duration(freqInt))
		// using SocketStats is the recommended method
		if useSocketStats == "true" {
			rawStats, err := GetSocketStats()
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
		} else {
			// if SocketStats is disabled, raingutter will use the raindrops endpoint
			// to retrieve metrics from the unicorn master
			body := Fetch(httpClient, raindropsURL, &readiness)
			if body != nil {
				r.Scan(body)
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
