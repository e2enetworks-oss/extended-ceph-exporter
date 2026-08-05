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
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/galexrt/extended-ceph-exporter/collector"
	"github.com/galexrt/extended-ceph-exporter/pkg/config"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
)

const testCollectorName = "fake"

var testDesc = prometheus.NewDesc("custom_test_image_bytes", "Test metric.", []string{"image"}, nil)

// cycle describes how the fake collector behaves on one Update call.
type cycle struct {
	metrics int
	delay   time.Duration
	err     error
	panics  bool
}

// fakeCollector runs through a script of cycles, repeating the last one once the
// script is exhausted. It exists because collector.Collector is an interface, which
// is what makes the refresher's caching semantics testable without a Ceph cluster.
type fakeCollector struct {
	mu     sync.Mutex
	calls  int
	script []cycle
}

func (f *fakeCollector) Update(ctx context.Context, _ *collector.Client, ch chan<- prometheus.Metric) error {
	f.mu.Lock()
	f.calls++
	current := f.script[min(f.calls-1, len(f.script)-1)]
	f.mu.Unlock()

	if current.panics {
		panic("collector exploded")
	}

	if current.delay > 0 {
		select {
		case <-time.After(current.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	for i := 0; i < current.metrics; i++ {
		if err := collector.Emit(ch, testDesc, float64(i), fmt.Sprintf("img-%d", i)); err != nil {
			return err
		}
	}

	return current.err
}

func newTestMetricsCollector(coll collector.Collector, interval time.Duration) *ExtendedCephMetricsCollector {
	clients := map[string]*collector.Client{
		"default": {Name: "default", Config: &config.Config{}},
	}
	collectors := map[string]collector.Collector{testCollectorName: coll}

	return NewExtendedCephMetricsCollector(context.Background(), zap.NewNop(), clients, collectors,
		time.Minute, interval, nil)
}

func cachedDataCount(n *ExtendedCephMetricsCollector) int {
	n.cacheMutex.RLock()
	defer n.cacheMutex.RUnlock()

	return len(n.cache[testCollectorName])
}

func lastRefreshOf(n *ExtendedCephMetricsCollector) time.Time {
	n.cacheMutex.RLock()
	defer n.cacheMutex.RUnlock()

	return n.lastRefresh[testCollectorName]
}

const successMetric = "custom_scrape_collector_success"

// gatherAndCompare compares selected metrics through a plain registry.
//
// testutil.CollectAndCompare would use a pedantic registry, which rejects any
// metric whose Desc was not announced in Describe. This collector cannot announce
// its per image descriptors up front, because they come from whichever sub
// collectors are enabled, so the pedantic check is the wrong tool here.
func gatherAndCompare(t *testing.T, n *ExtendedCephMetricsCollector, expected string, names ...string) {
	t.Helper()

	registry := prometheus.NewRegistry()
	if err := registry.Register(n); err != nil {
		t.Fatalf("failed to register the collector: %v", err)
	}

	if err := testutil.GatherAndCompare(registry, strings.NewReader(expected), names...); err != nil {
		t.Fatal(err)
	}
}

func assertSuccess(t *testing.T, n *ExtendedCephMetricsCollector, want float64) {
	t.Helper()

	expected := fmt.Sprintf(`
# HELP %s Whether a collector succeeded.
# TYPE %s gauge
%s{client="default",collector="%s"} %g
`, successMetric, successMetric, successMetric, testCollectorName, want)

	gatherAndCompare(t, n, expected, successMetric)
}

// A collector accumulates errors and keeps walking, so a failed cycle can return a
// partial result. Publishing that would silently shrink the exported series set, so
// the previous values have to survive.
func TestRefreshKeepsLastGoodDataWhenCycleFails(t *testing.T) {
	fake := &fakeCollector{script: []cycle{
		{metrics: 3},
		{metrics: 1, err: errors.New("one image could not be opened")},
	}}
	n := newTestMetricsCollector(fake, time.Minute)

	n.refresh(testCollectorName, time.Minute)

	if got := cachedDataCount(n); got != 3 {
		t.Fatalf("after a good cycle: cached %d data metrics, want 3", got)
	}
	firstRefresh := lastRefreshOf(n)
	assertSuccess(t, n, 1)

	n.refresh(testCollectorName, time.Minute)

	if got := cachedDataCount(n); got != 3 {
		t.Errorf("after a failed cycle: cached %d data metrics, want the previous 3", got)
	}
	if got := lastRefreshOf(n); !got.Equal(firstRefresh) {
		t.Errorf("lastRefresh advanced on a failed cycle: %v -> %v", firstRefresh, got)
	}

	// The failure still has to be visible. Discarding the whole result set would
	// have thrown away the one metric that announces it.
	assertSuccess(t, n, 0)
}

// The watchdog stops waiting for a slow cycle but cannot stop the cycle itself, so
// a cycle that is abandoned and later finishes must not overwrite a newer result or
// stamp its own data as freshly refreshed.
func TestRefreshDiscardsAbandonedCycle(t *testing.T) {
	fake := &fakeCollector{script: []cycle{
		{metrics: 1, delay: 300 * time.Millisecond},
		{metrics: 7},
	}}
	n := newTestMetricsCollector(fake, time.Minute)

	// Abandoned: the bound elapses long before the 300ms cycle finishes.
	n.refresh(testCollectorName, 20*time.Millisecond)

	// A later cycle publishes while the first is still running.
	n.refresh(testCollectorName, time.Minute)

	if got := cachedDataCount(n); got != 7 {
		t.Fatalf("second cycle cached %d data metrics, want 7", got)
	}
	published := lastRefreshOf(n)

	// Give the abandoned cycle time to finish and attempt its write.
	time.Sleep(500 * time.Millisecond)

	if got := cachedDataCount(n); got != 7 {
		t.Errorf("abandoned cycle overwrote the cache: %d data metrics, want 7", got)
	}
	if got := lastRefreshOf(n); !got.Equal(published) {
		t.Errorf("abandoned cycle restamped lastRefresh: %v -> %v", published, got)
	}
}

// A panic inside a collector runs on a background goroutine, where leaving it
// unrecovered terminates the whole exporter rather than just this cycle.
func TestPanicInCollectorIsContained(t *testing.T) {
	fake := &fakeCollector{script: []cycle{{panics: true}}}
	n := newTestMetricsCollector(fake, time.Minute)

	n.refresh(testCollectorName, time.Minute)

	if got := cachedDataCount(n); got != 0 {
		t.Errorf("a panicking cycle published %d data metrics, want 0", got)
	}
	if got := lastRefreshOf(n); !got.IsZero() {
		t.Errorf("a panicking cycle advanced lastRefresh to %v", got)
	}
	assertSuccess(t, n, 0)
}

// Before the first cycle completes there is nothing to replay, but the refresh
// timestamp must still be emitted as 0 so a collector that never ran stays
// alertable instead of being absent entirely.
func TestSnapshotBeforeFirstCycle(t *testing.T) {
	n := newTestMetricsCollector(&fakeCollector{script: []cycle{{metrics: 1}}}, time.Minute)

	expected := `
# HELP custom_rbd_last_refresh_timestamp_seconds Unix timestamp of the last completed collection cycle, per collector.
# TYPE custom_rbd_last_refresh_timestamp_seconds gauge
custom_rbd_last_refresh_timestamp_seconds{collector="fake"} 0
`

	gatherAndCompare(t, n, expected, "custom_rbd_last_refresh_timestamp_seconds")
}

func TestIntervalFor(t *testing.T) {
	tests := []struct {
		name      string
		defaultIn time.Duration
		overrides map[string]time.Duration
		want      time.Duration
	}{
		{
			name:      "falls back to the default",
			defaultIn: 4 * time.Minute,
			want:      4 * time.Minute,
		},
		{
			name:      "explicit override wins",
			defaultIn: 4 * time.Minute,
			overrides: map[string]time.Duration{testCollectorName: 30 * time.Second},
			want:      30 * time.Second,
		},
		{
			name:      "a zero override falls back to the default",
			defaultIn: 4 * time.Minute,
			overrides: map[string]time.Duration{testCollectorName: 0},
			want:      4 * time.Minute,
		},
		{
			name:      "an override for another collector is ignored",
			defaultIn: 4 * time.Minute,
			overrides: map[string]time.Duration{"someone_else": time.Second * 5},
			want:      4 * time.Minute,
		},
		{
			// Guards against a misconfigured interval turning the refresher into a
			// hot loop against the cluster.
			name:      "clamped to the minimum",
			defaultIn: time.Millisecond,
			want:      minRefreshInterval,
		},
		{
			name:      "an override below the minimum is clamped",
			defaultIn: 4 * time.Minute,
			overrides: map[string]time.Duration{testCollectorName: time.Millisecond},
			want:      minRefreshInterval,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			n := newTestMetricsCollector(&fakeCollector{script: []cycle{{}}}, test.defaultIn)
			n.intervals = test.overrides

			if got := n.intervalFor(testCollectorName); got != test.want {
				t.Fatalf("intervalFor() = %v, want %v", got, test.want)
			}
		})
	}
}

// Scrapes read while a refresher writes. Run with -race.
func TestConcurrentScrapeAndRefresh(t *testing.T) {
	fake := &fakeCollector{script: []cycle{{metrics: 5}}}
	n := newTestMetricsCollector(fake, time.Minute)

	var wg sync.WaitGroup

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				n.refresh(testCollectorName, time.Minute)
			}
		}()
	}

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				if got := len(n.snapshot()); got == 0 {
					t.Error("snapshot returned no metrics")

					return
				}
			}
		}()
	}

	wg.Wait()
}
