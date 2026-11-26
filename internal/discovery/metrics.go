package discovery

import "github.com/prometheus/client_golang/prometheus"

type metrics struct {
	duration    prometheus.Histogram
	failures    prometheus.Counter
	vms         prometheus.Gauge
	lastSuccess prometheus.Gauge
}

func newMetrics(reg prometheus.Registerer) *metrics {
	m := &metrics{
		duration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "noderings_sd_refresh_duration_seconds",
			Help:    "Time taken to fetch the virtual machine list from the NodeRings API.",
			Buckets: []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
		}),
		failures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "noderings_sd_refresh_failures_total",
			Help: "Number of failed refreshes of the virtual machine list.",
		}),
		vms: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "noderings_sd_virtual_machines",
			Help: "Number of virtual machines in the last successful refresh.",
		}),
		lastSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "noderings_sd_last_refresh_success_timestamp_seconds",
			Help: "Unix time of the last successful refresh.",
		}),
	}
	if reg != nil {
		reg.MustRegister(m.duration, m.failures, m.vms, m.lastSuccess)
	}
	return m
}
