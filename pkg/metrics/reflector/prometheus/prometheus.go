// Package prometheus sets the cache DefaultMetricsFactory to produce
// prometheus metrics. To use this package, you just have to import it.
package prometheus

import (
	"k8s.io/client-go/tools/cache"

	"github.com/prometheus/client_golang/prometheus"
)

const reflectorSubsystem = "reflector"

var (
	listsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Subsystem: reflectorSubsystem,
		Name:      "lists_total",
		Help:      "Total number of API lists done by the reflectors",
	}, []string{"name"})

	listsDuration = prometheus.NewSummaryVec(prometheus.SummaryOpts{
		Subsystem: reflectorSubsystem,
		Name:      "list_duration_seconds",
		Help:      "How long an API list takes to return and decode for the reflectors",
	}, []string{"name"})

	itemsPerList = prometheus.NewSummaryVec(prometheus.SummaryOpts{
		Subsystem: reflectorSubsystem,
		Name:      "items_per_list",
		Help:      "How many items an API list returns to the reflectors",
	}, []string{"name"})

	watchesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Subsystem: reflectorSubsystem,
		Name:      "watches_total",
		Help:      "Total number of API watches done by the reflectors",
	}, []string{"name"})

	shortWatchesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Subsystem: reflectorSubsystem,
		Name:      "short_watches_total",
		Help:      "Total number of short API watches done by the reflectors",
	}, []string{"name"})

	watchDuration = prometheus.NewSummaryVec(prometheus.SummaryOpts{
		Subsystem: reflectorSubsystem,
		Name:      "watch_duration_seconds",
		Help:      "How long an API watch takes to return and decode for the reflectors",
	}, []string{"name"})

	itemsPerWatch = prometheus.NewSummaryVec(prometheus.SummaryOpts{
		Subsystem: reflectorSubsystem,
		Name:      "items_per_watch",
		Help:      "How many items an API watch returns to the reflectors",
	}, []string{"name"})

	lastResourceVersion = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Subsystem: reflectorSubsystem,
		Name:      "last_resource_version",
		Help:      "Last resource version seen for the reflectors",
	}, []string{"name"})
)

func init() {
	prometheus.MustRegister(listsTotal)
	prometheus.MustRegister(listsDuration)
	prometheus.MustRegister(itemsPerList)
	prometheus.MustRegister(watchesTotal)
	prometheus.MustRegister(shortWatchesTotal)
	prometheus.MustRegister(watchDuration)
	prometheus.MustRegister(itemsPerWatch)
	prometheus.MustRegister(lastResourceVersion)
}

type prometheusMetricsProvider struct{}

func (prometheusMetricsProvider) NewListsMetric(name string) cache.CounterMetric {
	return listsTotal.WithLabelValues(name)
}

// use summary to get averages and percentiles
func (prometheusMetricsProvider) NewListDurationMetric(name string) cache.SummaryMetric {
	return listsDuration.WithLabelValues(name)
}

// use summary to get averages and percentiles
func (prometheusMetricsProvider) NewItemsInListMetric(name string) cache.SummaryMetric {
	return itemsPerList.WithLabelValues(name)
}

func (prometheusMetricsProvider) NewWatchesMetric(name string) cache.CounterMetric {
	return watchesTotal.WithLabelValues(name)
}

func (prometheusMetricsProvider) NewShortWatchesMetric(name string) cache.CounterMetric {
	return shortWatchesTotal.WithLabelValues(name)
}

// use summary to get averages and percentiles
func (prometheusMetricsProvider) NewWatchDurationMetric(name string) cache.SummaryMetric {
	return watchDuration.WithLabelValues(name)
}

// use summary to get averages and percentiles
func (prometheusMetricsProvider) NewItemsInWatchMetric(name string) cache.SummaryMetric {
	return itemsPerWatch.WithLabelValues(name)
}

func (prometheusMetricsProvider) NewLastResourceVersionMetric(name string) cache.GaugeMetric {
	return lastResourceVersion.WithLabelValues(name)
}
