package discovery

import "github.com/prometheus/client_golang/prometheus"

type metrics struct {
	duration    *prometheus.HistogramVec
	failures    *prometheus.CounterVec
	resources   *prometheus.GaugeVec
	lastSuccess *prometheus.GaugeVec
}

func newMetrics(reg prometheus.Registerer) *metrics {
	m := &metrics{
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "noderings_sd_refresh_duration_seconds",
			Help:    "Time taken to fetch one role's resources from the NodeRings API.",
			Buckets: []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
		}, []string{"role"}),
		failures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "noderings_sd_refresh_failures_total",
			Help: "Number of failed refreshes, by role.",
		}, []string{"role"}),
		resources: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "noderings_sd_resources",
			Help: "Resources in the last successful refresh: VMs, game servers, or DNS record sets.",
		}, []string{"role"}),
		lastSuccess: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "noderings_sd_last_refresh_success_timestamp_seconds",
			Help: "Unix time of the last successful refresh, by role.",
		}, []string{"role"}),
	}
	if reg != nil {
		reg.MustRegister(m.duration, m.failures, m.resources, m.lastSuccess)
	}
	return m
}

// init makes every role's series exist from the start, so a role that has
// never succeeded shows up as 0 failures rather than missing.
func (m *metrics) init(roles []string) {
	for _, r := range roles {
		m.failures.WithLabelValues(r)
		m.duration.WithLabelValues(r)
	}
}
