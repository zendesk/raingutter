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
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"
)

var (
	execCommand     = exec.Command
	version         string
	podName         = os.Getenv("POD_NAME")
	podNameSpace    = os.Getenv("POD_NAMESPACE")
	project         = os.Getenv("PROJECT")
	raindropsActive = promauto.NewSummaryVec(
		prometheus.SummaryOpts{
			Namespace:  "unicorn",
			Subsystem:  "raindrops",
			Name:       "active",
			Objectives: map[float64]float64{0.0: 0.00, 0.1: 0.01, 0.5: 0.05, 0.95: 0.001, 0.99: 0.001, 1: 1},
		},
		[]string{"pod_name", "project", "pod_namespace"})
	raindropsQueued = promauto.NewSummaryVec(
		prometheus.SummaryOpts{
			Namespace:  "unicorn",
			Subsystem:  "raindrops",
			Name:       "queued",
			Objectives: map[float64]float64{0.0: 0.00, 0.1: 0.01, 0.5: 0.05, 0.95: 0.001, 0.99: 0.001, 1: 1},
		},
		[]string{"pod_name", "project", "pod_namespace"})
	raindropsWorkers = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "unicorn",
			Subsystem: "raindrops",
			Name:      "worker",
		},
		[]string{"pod_name", "project", "pod_namespace"})
)

type raindrops struct {
	Calling float64
	Writing float64
	Active  float64
	Queued  float64
}

type status struct {
	Ready bool
}

type workers struct {
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

// GetWorkers return the total number of unicorn workers
func GetWorkers(w *workers) {
	var (
		workers float64
		cmdOut  []byte
		err     error
	)
	unicornWorkers := os.Getenv("UNICORN_WORKERS")
	// If the UNICORN_WORKERS env var is empty
	// get unicorn workers count via pgrep
	if unicornWorkers == "" {
		binary, lookErr := exec.LookPath("pgrep")
		checkFatal(lookErr)
		args := []string{"-fc", "helper.sh"}
		if cmdOut, err = execCommand(binary, args...).CombinedOutput(); err != nil {
			log.Error(binary, " returned: ", err)
			workers = 0
			log.Warn("Unicorn workers count set to 0")
			w.Count = workers
		} else {
			out := string(cmdOut)
			unicorns, err := strconv.ParseFloat(strings.TrimSpace(out), 64)
			checkFatal(err)
			// remove the master from the total running unicorns
			w.Count = unicorns - 1
		}
	} else {
		workers, err := strconv.ParseFloat(unicornWorkers, 64)
		checkFatal(err)
		w.Count = workers
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

// Parse convert a slice to float64
func Parse(l string) float64 {
	// get the value after the last ":"
	splitted := strings.Split(l, ":")[len(strings.Split(l, ":"))-1]
	// trim space and parse the int
	value, err := strconv.ParseFloat(strings.TrimSpace(splitted), 64)
	checkFatal(err)
	return value
}

func (r *raindrops) Scan(response *http.Response) raindrops {
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

func (r *raindrops) ScanSocketStats(s *SocketStats) raindrops {
	// `writing` and `calling` are not yet implemented
	r.Active = s.ActiveWorkers
	r.Queued = s.QueueSize
	return *r
}

// The histogram interface calculate the statistical distribution of any kind of value
// and generates: 95percentile, max, median, avg, count
// according to what's specified in /etc/dd-agent/datadog.conf
//
// https://docs.datadoghq.com/guides/dogstatsd/
func (r *raindrops) SendStats(c *statsd.Client, w *workers) {
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
	// worker.count - total number of available unicorn workers
	err = c.Histogram("worker.count", w.Count, nil, 1)
	checkError(err)
}

func (r *raindrops) recordMetrics(w *workers) {
	raindropsActive.WithLabelValues(podName, project, podNameSpace).Observe(r.Active)
	raindropsQueued.WithLabelValues(podName, project, podNameSpace).Observe(r.Queued)
	raindropsWorkers.WithLabelValues(podName, project, podNameSpace).Set(w.Count)
}

func (r *raindrops) logMetrics(w *workers, raindropsURL string) {
	contextLogger := log.WithFields(log.Fields{
		"active":  r.Active,
		"queued":  r.Queued,
		"writing": r.Writing,
		"calling": r.Calling,
		"workers": w.Count,
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
		go func() {
			for {
				http.Handle("/metrics", promhttp.Handler())
				if err := http.ListenAndServe(":8000", nil); err != nil {
					log.Fatal(err)
				}
			}
		}()
	}

	raindropsURL := os.Getenv("RG_RAINDROPS_URL")
	if raindropsURL == "" {
		log.Warning("RG_RAINDROPS_URL is missing")
	}
	log.Info("RG_RAINDROPS_URL: ", raindropsURL)

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
		statsdNamespace = "unicorn.raingutter.agg."
	}
	log.Info("RG_STATSD_NAMESPACE: ", statsdNamespace)

	statsdExtraTags := os.Getenv("RG_STATSD_EXTRA_TAGS")

	unicorn := os.Getenv("RG_UNICORN")
	if unicorn == "" {
		log.Warning("RG_UNICORN is not defined. Set to true by default")
		unicorn = "true"
	}
	log.Info("RG_UNICORN: ", unicorn)

	usingSocketStats := os.Getenv("RG_USE_SOCKET_STATS")
	if usingSocketStats == "" {
		log.Warning("RG_USE_SOCKET_STATS is not defined. Set to false by default")
		usingSocketStats = "false"
	}
	log.Info("RG_USE_SOCKET_STATS: ", usingSocketStats)

	unicornPort := os.Getenv("RG_UNICORN_PORT")
	if unicornPort == "" {
		unicornPort = "3000"
	}
	log.Info("RG_UNICORN_PORT: ", unicornPort)

	// Milliseconds
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

	r := raindrops{}

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

	w := workers{Count: 0}
	if unicorn == "true" {
		go func() {
			for {
				GetWorkers(&w)
				// sleep for a minute
				<-time.After(60 * time.Second)
			}
		}()
	}

	readiness := status{Ready: false}
	for {
		didScan := false

		time.Sleep(time.Millisecond * time.Duration(freqInt))
		if usingSocketStats == "true" {
			rawStats, err := GetSocketStats()
			if err != nil {
				log.Error(err)
			}

			stats, err := ParseSocketStats(unicornPort, rawStats)
			if err != nil {
				log.Error(err)
			} else {
				r.ScanSocketStats(stats)
				didScan = true
			}
		} else {
			body := Fetch(httpClient, raindropsURL, &readiness)
			if body != nil {
				r.Scan(body)
				didScan = true
			}
		}

		if didScan {
			if statsdEnabled == "true" {
				r.SendStats(statsdClient, &w)
			}
			if prometheusEnabled == "true" {
				r.recordMetrics(&w)
			}
			if logMetricsEnabled == "true" {
				r.logMetrics(&w, raindropsURL)
			}
		}
	}

}
