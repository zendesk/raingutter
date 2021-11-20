package main

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"
)

var (
	raingutterActive = promauto.NewSummaryVec(
		prometheus.SummaryOpts{
			Namespace:  "raingutter",
			Name:       "active",
			Objectives: map[float64]float64{0.0: 0.00, 0.1: 0.01, 0.5: 0.05, 0.95: 0.001, 0.99: 0.001, 1: 1},
		},
		[]string{"pod_name", "project", "pod_namespace"})
	raingutterQueued = promauto.NewSummaryVec(
		prometheus.SummaryOpts{
			Namespace:  "raingutter",
			Name:       "queued",
			Objectives: map[float64]float64{0.0: 0.00, 0.1: 0.01, 0.5: 0.05, 0.95: 0.001, 0.99: 0.001, 1: 1},
		},
		[]string{"pod_name", "project", "pod_namespace"})
	raingutterWorkers = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "raingutter",
			Name:      "worker",
		},
		[]string{"pod_name", "project", "pod_namespace"})
	raingutterThreads = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "raingutter",
			Name:      "threads",
		},
		[]string{"pod_name", "project", "pod_namespace"})
)

func (r *raingutter) recordMetrics() {
	raingutterActive.WithLabelValues(podName, project, podNameSpace).Observe(float64(r.Active))
	raingutterQueued.WithLabelValues(podName, project, podNameSpace).Observe(float64(r.Queued))
	if r.useThreads {
		raingutterThreads.WithLabelValues(podName, project, podNameSpace).Set(float64(r.workerCount))
	} else {
		raingutterWorkers.WithLabelValues(podName, project, podNameSpace).Set(float64(r.workerCount))
	}
}

func setupPrometheus() {
	go func() {
		for {
			http.Handle("/metrics", promhttp.Handler())
			if err := http.ListenAndServe(":8000", nil); err != nil {
				log.Fatal(err)
			}
		}
	}()
}
