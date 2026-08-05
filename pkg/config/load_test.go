/*
Copyright 2024 Alexander Trost All rights reserved.

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

package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFile(t *testing.T, name, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write %s: %v", path, err)
	}

	return path
}

// The per collector intervals are a map of durations, which is the one decode path
// that depends on the mapstructure duration hook applying to map values rather than
// only to struct fields.
func TestLoadConfigDecodesRefreshIntervals(t *testing.T) {
	path := writeFile(t, "config.yaml", `
collectors:
  - rbd_images
  - rbd_image_usage
timeouts:
  collector: "150s"
refresh:
  interval: "90s"
  intervals:
    rbd_images: "30s"
    rbd_image_usage: "2m"
rbd:
  opTimeout: "45s"
`)

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if cfg.Refresh.Interval != 90*time.Second {
		t.Errorf("refresh.interval = %v, want 90s", cfg.Refresh.Interval)
	}
	if got := cfg.Refresh.Intervals["rbd_images"]; got != 30*time.Second {
		t.Errorf("refresh.intervals[rbd_images] = %v, want 30s", got)
	}
	if got := cfg.Refresh.Intervals["rbd_image_usage"]; got != 2*time.Minute {
		t.Errorf("refresh.intervals[rbd_image_usage] = %v, want 2m", got)
	}
	if cfg.RBD.OpTimeout != 45*time.Second {
		t.Errorf("rbd.opTimeout = %v, want 45s", cfg.RBD.OpTimeout)
	}
	if cfg.Timeouts.Collector != 150*time.Second {
		t.Errorf("timeouts.collector = %v, want 150s", cfg.Timeouts.Collector)
	}
	if cfg.Collectors == nil || len(*cfg.Collectors) != 2 {
		t.Errorf("collectors = %v, want 2 entries", cfg.Collectors)
	}
}

// The defaults matter operationally: opTimeout being unset would leave librados
// waiting forever, and the refresh interval being unset would leave collectors
// without a schedule.
func TestLoadConfigDefaults(t *testing.T) {
	path := writeFile(t, "config.yaml", "logLevel: \"INFO\"\n")

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if cfg.Refresh.Interval != 4*time.Minute {
		t.Errorf("default refresh.interval = %v, want 4m", cfg.Refresh.Interval)
	}
	if cfg.Timeouts.Collector != 3*time.Minute {
		t.Errorf("default timeouts.collector = %v, want 3m", cfg.Timeouts.Collector)
	}
	if cfg.RBD.OpTimeout != 30*time.Second {
		t.Errorf("default rbd.opTimeout = %v, want 30s", cfg.RBD.OpTimeout)
	}
	if cfg.ListenHost != ":9138" {
		t.Errorf("default listenHost = %q, want \":9138\"", cfg.ListenHost)
	}
}

// An RBD only deployment ships no realms file. Requiring one would be a startup
// failure for a subsystem that is not in use.
func TestLoadRealmsMissingFileIsNotAnError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "realms.yaml")

	realms, err := loadRealms(missing)
	if err != nil {
		t.Fatalf("a missing realms file should not be an error, got: %v", err)
	}
	if realms == nil {
		t.Fatal("expected an empty RGW config, got nil")
	}
	if len(realms.Realms) != 0 {
		t.Errorf("expected no realms, got %d", len(realms.Realms))
	}
}

// Absent is fine; malformed is not. Silently ignoring a broken realms file would
// look identical to having none.
func TestLoadRealmsMalformedFileErrors(t *testing.T) {
	path := writeFile(t, "realms.yaml", "realms: [this is not: valid yaml\n")

	if _, err := loadRealms(path); err == nil {
		t.Fatal("expected a parse error for a malformed realms file, got nil")
	}
}

func TestLoadRealmsParsesRealms(t *testing.T) {
	path := writeFile(t, "realms.yaml", `
realms:
  - name: example
    host: "http://rgw.example.com:8080"
    accessKey: "AK"
    secretKey: "SK"
    skipTLSVerify: true
`)

	realms, err := loadRealms(path)
	if err != nil {
		t.Fatalf("failed to load realms: %v", err)
	}

	if len(realms.Realms) != 1 {
		t.Fatalf("got %d realms, want 1", len(realms.Realms))
	}

	realm := realms.Realms[0]
	if realm.Name != "example" {
		t.Errorf("realm name = %q, want \"example\"", realm.Name)
	}
	if realm.Host != "http://rgw.example.com:8080" {
		t.Errorf("realm host = %q", realm.Host)
	}
	if !realm.SkipTLSVerify {
		t.Error("skipTLSVerify = false, want true")
	}
}

// Every string value goes through os.ExpandEnv, which is how RGW credentials are
// meant to be supplied without being written into the file.
func TestLoadRealmsExpandsEnv(t *testing.T) {
	t.Setenv("TEST_RGW_ACCESS_KEY", "expanded-key")

	path := writeFile(t, "realms.yaml", `
realms:
  - name: example
    host: "http://rgw.example.com:8080"
    accessKey: "${TEST_RGW_ACCESS_KEY}"
    secretKey: "SK"
`)

	realms, err := loadRealms(path)
	if err != nil {
		t.Fatalf("failed to load realms: %v", err)
	}

	if got := realms.Realms[0].AccessKey; got != "expanded-key" {
		t.Errorf("accessKey = %q, want \"expanded-key\"", got)
	}
}

// Load is what main actually calls, so the combination of a present config and an
// absent realms file has to work as a unit, not just each half separately.
func TestLoadWithConfigAndNoRealmsFile(t *testing.T) {
	dir := t.TempDir()

	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("logLevel: \"DEBUG\"\n"), 0o600); err != nil {
		t.Fatalf("failed to write the config: %v", err)
	}

	cfg, realms, err := Load(configPath, filepath.Join(dir, "absent-realms.yaml"))
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.LogLevel != "DEBUG" {
		t.Errorf("logLevel = %q, want \"DEBUG\"", cfg.LogLevel)
	}
	if len(realms.Realms) != 0 {
		t.Errorf("expected no realms, got %d", len(realms.Realms))
	}
}

// A missing config file, unlike a missing realms file, must be fatal: without it
// there is nothing to tell the exporter what to collect.
func TestLoadFailsWithoutConfigFile(t *testing.T) {
	dir := t.TempDir()

	if _, _, err := Load(filepath.Join(dir, "absent-config.yaml"), filepath.Join(dir, "absent-realms.yaml")); err == nil {
		t.Fatal("expected an error for a missing config file, got nil")
	}
}

func TestLoadTestConfigAppliesDefaults(t *testing.T) {
	cfg, realms, err := LoadTestConfig()
	if err != nil {
		t.Fatalf("LoadTestConfig failed: %v", err)
	}

	if cfg.Refresh.Interval != 4*time.Minute {
		t.Errorf("refresh.interval = %v, want the 4m default", cfg.Refresh.Interval)
	}
	if cfg.RBD.OpTimeout != 30*time.Second {
		t.Errorf("rbd.opTimeout = %v, want the 30s default", cfg.RBD.OpTimeout)
	}
	if realms == nil {
		t.Error("expected an empty RGW config, got nil")
	}
}
