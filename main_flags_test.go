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
	"testing"
	"time"

	"github.com/galexrt/extended-ceph-exporter/collector"
	"github.com/galexrt/extended-ceph-exporter/pkg/config"
	flag "github.com/spf13/pflag"
)

// withFreshFlags swaps the package level flag set and options for empty ones so a
// test can drive applyFlagOverrides without leaking Changed() state into other
// tests.
func withFreshFlags(t *testing.T) {
	t.Helper()

	originalFlags, originalOpts := flags, opts
	t.Cleanup(func() {
		flags, opts = originalFlags, originalOpts
	})

	flags = flag.NewFlagSet("test", flag.ContinueOnError)
	opts = CmdLineOpts{}

	flags.StringSliceVar(&opts.CollectorsEnabled, "collectors-enabled", nil, "")
	flags.StringVar(&opts.ListenAddress, "web.listen-address", "", "")
	flags.DurationVar(&opts.CollectorTimeout, "collector-timeout", 0, "")
	flags.DurationVar(&opts.RefreshInterval, "refresh-interval", 0, "")
}

func TestAliasNormalizeFunc(t *testing.T) {
	tests := map[string]string{
		"multi-realm-config": "realms-config",
		"realms-config":      "realms-config",
		"refresh-interval":   "refresh-interval",
	}

	for in, want := range tests {
		if got := string(aliasNormalizeFunc(nil, in)); got != want {
			t.Errorf("aliasNormalizeFunc(%q) = %q, want %q", in, got, want)
		}
	}
}

// The config file is authoritative for anything left at its default; a flag only
// wins when it was explicitly passed.
func TestApplyFlagOverridesConfigWinsWhenFlagUnset(t *testing.T) {
	withFreshFlags(t)

	enabled := []string{"rbd_images"}
	cfg := &config.Config{
		ListenHost: ":9138",
		Collectors: &enabled,
		Timeouts:   config.Timeouts{Collector: 3 * time.Minute},
		Refresh:    config.Refresh{Interval: 4 * time.Minute},
	}

	applyFlagOverrides(cfg)

	if cfg.ListenHost != ":9138" {
		t.Errorf("listenHost = %q, want the config value \":9138\"", cfg.ListenHost)
	}
	if cfg.Timeouts.Collector != 3*time.Minute {
		t.Errorf("timeouts.collector = %v, want 3m", cfg.Timeouts.Collector)
	}
	if cfg.Refresh.Interval != 4*time.Minute {
		t.Errorf("refresh.interval = %v, want 4m", cfg.Refresh.Interval)
	}
	if len(opts.CollectorsEnabled) != 1 || opts.CollectorsEnabled[0] != "rbd_images" {
		t.Errorf("collectors = %v, want the config list", opts.CollectorsEnabled)
	}
}

func TestApplyFlagOverridesExplicitFlagWins(t *testing.T) {
	withFreshFlags(t)

	enabled := []string{"rbd_images"}
	cfg := &config.Config{
		ListenHost: ":9138",
		Collectors: &enabled,
		Timeouts:   config.Timeouts{Collector: 3 * time.Minute},
		Refresh:    config.Refresh{Interval: 4 * time.Minute},
	}

	for name, value := range map[string]string{
		"web.listen-address": ":9999",
		"collector-timeout":  "90s",
		"refresh-interval":   "45s",
		"collectors-enabled": "rbd_image_usage",
	} {
		if err := flags.Set(name, value); err != nil {
			t.Fatalf("failed to set --%s: %v", name, err)
		}
	}

	applyFlagOverrides(cfg)

	if cfg.ListenHost != ":9999" {
		t.Errorf("listenHost = %q, want the flag value \":9999\"", cfg.ListenHost)
	}
	if cfg.Timeouts.Collector != 90*time.Second {
		t.Errorf("timeouts.collector = %v, want 90s", cfg.Timeouts.Collector)
	}
	if cfg.Refresh.Interval != 45*time.Second {
		t.Errorf("refresh.interval = %v, want 45s", cfg.Refresh.Interval)
	}
	if len(opts.CollectorsEnabled) != 1 || opts.CollectorsEnabled[0] != "rbd_image_usage" {
		t.Errorf("collectors = %v, want the flag value", opts.CollectorsEnabled)
	}
}

func TestLoadCollectors(t *testing.T) {
	const fakeName = "test_only_fake_collector"

	collector.Factories[fakeName] = func() (collector.Collector, error) {
		return &fakeCollector{script: []cycle{{}}}, nil
	}
	t.Cleanup(func() { delete(collector.Factories, fakeName) })

	loaded, err := loadCollectors([]string{fakeName})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded %d collectors, want 1", len(loaded))
	}
	if _, present := loaded[fakeName]; !present {
		t.Errorf("collector %q missing from the result", fakeName)
	}

	// An unknown name has to be a startup error, not a silently skipped collector.
	if _, err := loadCollectors([]string{"no_such_collector"}); err == nil {
		t.Error("expected an error for an unknown collector, got nil")
	}
}

func TestCreateRGWAPIConnection(t *testing.T) {
	cfg := &config.Config{Timeouts: config.Timeouts{HTTP: 55 * time.Second}}

	api, err := CreateRGWAPIConnection(cfg, &config.Realm{
		Name:      "example",
		Host:      "http://rgw.example.com:8080",
		AccessKey: "AK",
		SecretKey: "SK",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if api == nil {
		t.Fatal("expected an API connection, got nil")
	}

	// Missing credentials must fail at startup rather than at the first scrape.
	if _, err := CreateRGWAPIConnection(cfg, &config.Realm{Name: "broken"}); err == nil {
		t.Error("expected an error for a realm with no host or credentials, got nil")
	}
}

// constMetric must return the failure rather than panicking, which is the whole
// reason MustNewConstMetric was dropped.
func TestConstMetricRejectsMismatchedLabels(t *testing.T) {
	n := newTestMetricsCollector(&fakeCollector{script: []cycle{{}}}, time.Minute)

	if _, ok := n.constMetric(scrapeSuccessDesc, 1, "only-one-label-value"); ok {
		t.Error("expected constMetric to reject a label count mismatch")
	}

	if _, ok := n.constMetric(scrapeSuccessDesc, 1, "collector", "client"); !ok {
		t.Error("expected constMetric to accept the correct label count")
	}
}

// StartRefreshers has to prime immediately, keep ticking, and stop when its context
// is canceled. A refresher that ignored cancellation would keep hitting the cluster
// after shutdown.
func TestStartRefreshersTicksAndStopsOnCancel(t *testing.T) {
	fake := &fakeCollector{script: []cycle{{metrics: 1}}}
	n := newTestMetricsCollector(fake, minRefreshInterval)

	ctx, cancel := context.WithCancel(context.Background())
	n.StartRefreshers(ctx)

	// Primed immediately, then at least one tick at the clamped one second interval.
	deadline := time.Now().Add(4 * time.Second)
	for {
		fake.mu.Lock()
		calls := fake.calls
		fake.mu.Unlock()

		if calls >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("refresher produced %d cycles in 4s, want at least 2", calls)
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	time.Sleep(200 * time.Millisecond)

	fake.mu.Lock()
	afterCancel := fake.calls
	fake.mu.Unlock()

	time.Sleep(1500 * time.Millisecond)

	fake.mu.Lock()
	settled := fake.calls
	fake.mu.Unlock()

	if settled != afterCancel {
		t.Errorf("refresher kept running after cancel: %d -> %d cycles", afterCancel, settled)
	}
}
