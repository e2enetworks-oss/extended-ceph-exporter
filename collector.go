/*
Copyright 2022 Koor Technologies, Inc. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/galexrt/extended-ceph-exporter/collector"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

const (
	// minRefreshInterval keeps a misconfigured interval from turning a refresher
	// into a hot loop against the cluster.
	minRefreshInterval = time.Second

	// dutyCycleWarnRatio is the share of its interval a cycle may occupy before it
	// is worth warning about. A collector sitting above this is on its way to
	// overrunning the interval entirely, at which point every cycle gets abandoned
	// and the metrics stop advancing; the warning is the early notice.
	dutyCycleWarnRatio = 0.5
)

var (
	scrapeDurationDesc = prometheus.NewDesc(
		prometheus.BuildFQName(collector.MetricsNamespace, "scrape", "collector_duration_seconds"),
		"Duration of a collector scrape.",
		[]string{"collector", "client"},
		nil,
	)
	scrapeSuccessDesc = prometheus.NewDesc(
		prometheus.BuildFQName(collector.MetricsNamespace, "scrape", "collector_success"),
		"Whether a collector succeeded.",
		[]string{"collector", "client"},
		nil,
	)
	lastRefreshDesc = prometheus.NewDesc(
		prometheus.BuildFQName(collector.MetricsNamespace, "rbd", "last_refresh_timestamp_seconds"),
		"Unix timestamp of the last completed collection cycle, per collector.",
		[]string{"collector"},
		nil,
	)
)

// ExtendedCephMetricsCollector contains the collectors to be used
//
// Every enabled collector is refreshed by its own background goroutine on its
// own interval, and scrapes only replay what those refreshers stored. A cheap
// collector can therefore be kept fresh without an expensive one delaying, or
// timing out, a Prometheus scrape.
type ExtendedCephMetricsCollector struct {
	ctx        context.Context
	ctxTimeout time.Duration
	logger     *zap.Logger
	clients    map[string]*collector.Client
	collectors map[string]collector.Collector

	// Refresh related
	defaultInterval time.Duration
	intervals       map[string]time.Duration

	// cache, self, lastRefresh and generation are keyed by collector name and
	// guarded by cacheMutex. It is an RWMutex because scrapes only read; the
	// refreshers are the sole writers.
	//
	// cache and self are separate because they have different publish rules. A
	// failed cycle must not replace the data metrics, but it must still publish its
	// own scrape_collector_success of 0 — otherwise the one metric that announces
	// the failure would be dropped along with the partial data, and the cache would
	// keep serving the previous success of 1.
	cacheMutex  sync.RWMutex
	cache       map[string][]prometheus.Metric
	self        map[string][]prometheus.Metric
	lastRefresh map[string]time.Time
	generation  map[string]uint64
}

func NewExtendedCephMetricsCollector(ctx context.Context, logger *zap.Logger, clients map[string]*collector.Client, collectors map[string]collector.Collector, ctxTimeout time.Duration, defaultInterval time.Duration, intervals map[string]time.Duration) *ExtendedCephMetricsCollector {
	return &ExtendedCephMetricsCollector{
		ctx:             ctx,
		ctxTimeout:      ctxTimeout,
		logger:          logger,
		clients:         clients,
		collectors:      collectors,
		defaultInterval: defaultInterval,
		intervals:       intervals,
		cache:           make(map[string][]prometheus.Metric, len(collectors)),
		self:            make(map[string][]prometheus.Metric, len(collectors)),
		lastRefresh:     make(map[string]time.Time, len(collectors)),
		generation:      make(map[string]uint64, len(collectors)),
	}
}

// Describe implements the prometheus.Collector interface.
func (n *ExtendedCephMetricsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- scrapeDurationDesc
	ch <- scrapeSuccessDesc
	ch <- lastRefreshDesc
}

// Collect implements the prometheus.Collector interface.
//
// It never collects anything itself; it only replays what the background
// refreshers produced, which is what keeps a scrape fast regardless of how
// expensive the underlying collection is.
func (n *ExtendedCephMetricsCollector) Collect(outgoingCh chan<- prometheus.Metric) {
	for _, metric := range n.snapshot() {
		outgoingCh <- metric
	}
}

// snapshot copies the cached metrics of every collector and adds a refresh
// timestamp for each.
//
// The copy is taken under the read lock so that sending on the outgoing channel,
// which is paced by whoever is scraping, cannot stall the refreshers.
func (n *ExtendedCephMetricsCollector) snapshot() []prometheus.Metric {
	n.cacheMutex.RLock()
	defer n.cacheMutex.RUnlock()

	// One refresh timestamp per collector, plus whatever each has cached.
	total := len(n.collectors)
	for _, cached := range n.cache {
		total += len(cached)
	}
	for _, cached := range n.self {
		total += len(cached)
	}

	metrics := make([]prometheus.Metric, 0, total)
	for name := range n.collectors {
		metrics = append(metrics, n.cache[name]...)
		metrics = append(metrics, n.self[name]...)

		// Always emitted, so a collector that has never completed a cycle shows
		// up as 0 and stays alertable rather than being absent entirely.
		var timestamp float64
		if refreshed, ok := n.lastRefresh[name]; ok {
			timestamp = float64(refreshed.Unix())
		}

		if metric, ok := n.constMetric(lastRefreshDesc, timestamp, name); ok {
			metrics = append(metrics, metric)
		}
	}

	return metrics
}

// constMetric builds a const gauge, logging rather than panicking when the label
// values do not match the Desc. See collector.Emit for why Must is avoided.
func (n *ExtendedCephMetricsCollector) constMetric(desc *prometheus.Desc, value float64, labelValues ...string) (prometheus.Metric, bool) {
	metric, err := prometheus.NewConstMetric(desc, prometheus.GaugeValue, value, labelValues...)
	if err != nil {
		n.logger.Error("failed to build metric", zap.String("desc", desc.String()), zap.Error(err))

		return nil, false
	}

	return metric, true
}

// StartRefreshers launches one background refresher per enabled collector.
func (n *ExtendedCephMetricsCollector) StartRefreshers(ctx context.Context) {
	for name := range n.collectors {
		interval := n.intervalFor(name)
		n.logger.Info("starting collector refresher", zap.String("collector", name), zap.Duration("interval", interval))

		go n.refreshLoop(ctx, name, interval)
	}
}

// intervalFor resolves a collector's refresh interval, preferring its explicit
// override over the shared default.
func (n *ExtendedCephMetricsCollector) intervalFor(name string) time.Duration {
	interval := n.defaultInterval
	if override, ok := n.intervals[name]; ok && override > 0 {
		interval = override
	}

	if interval < minRefreshInterval {
		interval = minRefreshInterval
	}

	return interval
}

// refreshLoop primes the cache once and then refreshes at a fixed rate, so that
// the cycle to cycle period does not drift by however long a collection took.
func (n *ExtendedCephMetricsCollector) refreshLoop(ctx context.Context, name string, interval time.Duration) {
	n.refresh(name, interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n.refresh(name, interval)
		}
	}
}

// refresh runs one collection cycle for a collector, but stops waiting for it
// once bound has elapsed.
//
// A librados call can block indefinitely and Go cannot interrupt a blocked cgo
// call, so an abandoned cycle keeps running and leaks its goroutine. Abandoning
// it regardless is what keeps the loop alive and the refresh timestamp moving,
// which is what makes a wedged collector visible instead of letting the exporter
// serve stale metrics forever without ever reporting an error.
//
// Two rules govern what a finished cycle is allowed to publish:
//
//   - It must still be the current generation. A cycle the watchdog already
//     abandoned may finish after a later one, and letting it write would replace
//     newer data with older data while stamping it as freshly refreshed.
//   - It must have succeeded. Collectors accumulate errors and keep walking, so a
//     failed cycle can carry a partial result; publishing that would silently
//     shrink the exported series set. Keeping the previous values and leaving
//     lastRefresh alone makes the staleness signal do the talking instead.
func (n *ExtendedCephMetricsCollector) refresh(name string, bound time.Duration) {
	begin := time.Now()

	n.cacheMutex.Lock()
	n.generation[name]++
	generation := n.generation[name]
	n.cacheMutex.Unlock()

	done := make(chan struct{})

	go func() {
		defer close(done)

		data, self, ok := n.runCollector(name)

		n.cacheMutex.Lock()
		defer n.cacheMutex.Unlock()

		if n.generation[name] != generation {
			n.logger.Warn("discarding the result of an abandoned refresh cycle, a later cycle already published",
				zap.String("collector", name))

			return
		}

		// Published even on failure: this carries scrape_collector_success, which is
		// how a failed cycle becomes visible at all.
		n.self[name] = self

		if !ok {
			n.logger.Warn("keeping the previously cached metrics because this cycle failed, the refresh timestamp deliberately does not advance",
				zap.String("collector", name))

			return
		}

		n.cache[name] = data
		n.lastRefresh[name] = time.Now()
	}()

	select {
	case <-done:
		took := time.Since(begin)
		n.logger.Debug("refresh cycle complete", zap.String("collector", name), zap.Float64("took", took.Seconds()))

		if took > time.Duration(float64(bound)*dutyCycleWarnRatio) {
			n.logger.Warn("refresh cycle is using a large share of its interval, it will start overrunning if this grows",
				zap.String("collector", name), zap.Float64("took", took.Seconds()), zap.Duration("interval", bound))
		}
	case <-time.After(bound):
		n.logger.Error("refresh cycle exceeded its interval and was abandoned, it may be blocked in librados and its goroutine will leak until the exporter is restarted",
			zap.String("collector", name), zap.Duration("interval", bound))
	}
}

// runCollector runs a single collector against every client.
//
// It returns the collector's own metrics, this exporter's per client scrape
// metrics, and whether every client succeeded. The two metric sets are kept apart
// because a failed cycle must still publish its scrape metrics while leaving the
// previously cached data alone.
func (n *ExtendedCephMetricsCollector) runCollector(name string) (data []prometheus.Metric, self []prometheus.Metric, ok bool) {
	coll := n.collectors[name]
	metricsCh := make(chan prometheus.Metric)

	data = []prometheus.Metric{}

	// Wait to ensure metricsCh is fully drained before the collected metrics
	// are handed back
	drained := make(chan struct{})
	go func() {
		defer close(drained)

		for metric := range metricsCh {
			data = append(data, metric)
		}
	}()

	var failed atomic.Bool
	var selfMu sync.Mutex
	var wgCollection sync.WaitGroup

	for clientName, client := range n.clients {
		wgCollection.Add(1)
		go func(clientName string, client *collector.Client) {
			defer wgCollection.Done()

			begin := time.Now()
			err := n.updateCollector(coll, name, clientName, client, metricsCh)
			duration := time.Since(begin)
			var success float64

			if err != nil {
				n.logger.Error(fmt.Sprintf("%s collector failed after %fs", name, duration.Seconds()), zap.Error(err))
				failed.Store(true)
				success = 0
			} else {
				n.logger.Debug(fmt.Sprintf("%s collector succeeded after %fs.", name, duration.Seconds()))
				success = 1
			}

			// Built here rather than sent through metricsCh so they stay separable
			// from the collector's own output.
			selfMu.Lock()
			defer selfMu.Unlock()

			if metric, built := n.constMetric(scrapeDurationDesc, duration.Seconds(), name, clientName); built {
				self = append(self, metric)
			}
			if metric, built := n.constMetric(scrapeSuccessDesc, success, name, clientName); built {
				self = append(self, metric)
			}
		}(clientName, client)
	}

	wgCollection.Wait()
	close(metricsCh)
	<-drained

	return data, self, !failed.Load()
}

// updateCollector runs one collector against one client, converting a panic into
// an error.
//
// This runs on a background goroutine, where an unrecovered panic terminates the
// whole exporter rather than just this cycle. Containing it means a defect in a
// collector, or in the cgo layer beneath it, degrades to a failed collector that
// scrape_collector_success reports.
func (n *ExtendedCephMetricsCollector) updateCollector(coll collector.Collector, name, clientName string, client *collector.Client, ch chan<- prometheus.Metric) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("collector panicked: %v", recovered)
			n.logger.Error("recovered a panic in a collector",
				zap.String("collector", name),
				zap.String("client", clientName),
				zap.Any("panic", recovered),
				zap.ByteString("stack", debug.Stack()))
		}
	}()

	ctx, cancel := context.WithTimeout(n.ctx, n.ctxTimeout)
	defer cancel()

	return coll.Update(ctx, client, ch)
}
