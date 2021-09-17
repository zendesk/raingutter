package main

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"
)

var (
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

func (r *raindrops) recordMetrics(w *workers) {
	// TODO add a conditional here, and provide different metrics if we use puma or unicorn
	raindropsActive.WithLabelValues(podName, project, podNameSpace).Observe(r.Active)
	raindropsQueued.WithLabelValues(podName, project, podNameSpace).Observe(r.Queued)
	raindropsWorkers.WithLabelValues(podName, project, podNameSpace).Set(w.Count)
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
