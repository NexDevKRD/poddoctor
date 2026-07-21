// Package metrics registers PodDoctor's Prometheus counters so diagnosis
// trends survive PodDiagnosis CRs being garbage-collected with their pod.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// DiagnosesTotal counts every root-cause determination PodDoctor makes,
// labeled so `sum by (root_cause)` gives a durable failure-type trend even
// after the underlying PodDiagnosis CR is deleted along with its pod.
var DiagnosesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "poddoctor_diagnoses_total",
	Help: "Total number of PodDiagnosis root-cause determinations made, by root cause and confidence.",
}, []string{"root_cause", "confidence"})

func init() {
	metrics.Registry.MustRegister(DiagnosesTotal)
}
