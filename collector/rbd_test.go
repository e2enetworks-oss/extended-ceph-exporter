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

package collector

import (
	"context"
	"strings"
	"testing"

	"github.com/ceph/go-ceph/rbd"
	"github.com/galexrt/extended-ceph-exporter/pkg/config"
	"github.com/prometheus/client_golang/prometheus"
)

// Everything below eachRBDImage's guard takes concrete go-ceph types
// (*rados.Conn, *rados.IOContext, *rbd.Image), so it cannot be faked without an
// interface seam. The guard itself is reachable and worth covering: a client with
// no rados connection is exactly what happens when an RBD collector is enabled
// without a usable ceph config, and it must report that rather than dereference nil.
func TestEachRBDImageWithoutRadosConnection(t *testing.T) {
	client := &Client{Name: "default", Config: &config.Config{}}

	called := false
	err := eachRBDImage(context.Background(), client, func(string, string, string, *rbd.Image) error {
		called = true

		return nil
	})

	if err == nil {
		t.Fatal("expected an error for a client with no rados connection, got nil")
	}
	if called {
		t.Error("the per image function must not run without a rados connection")
	}
	if !strings.Contains(err.Error(), "no rados connection") {
		t.Errorf("error should explain the missing connection, got: %v", err)
	}
	if !strings.Contains(err.Error(), "default") {
		t.Errorf("error should name the client, got: %v", err)
	}
}

// The same guard has to hold for every RBD collector, since each one is reachable
// from a config that enables it without a working ceph config.
func TestRBDCollectorsReportMissingRadosConnection(t *testing.T) {
	client := &Client{Name: "default", Config: &config.Config{}}

	collectors := map[string]Collector{}
	for _, name := range []string{"rbd_images", "rbd_image_usage", "rbd_volumes"} {
		factory, present := Factories[name]
		if !present {
			t.Fatalf("collector %q is not registered in Factories", name)
		}

		built, err := factory()
		if err != nil {
			t.Fatalf("factory for %q returned an error: %v", name, err)
		}
		collectors[name] = built
	}

	for name, coll := range collectors {
		t.Run(name, func(t *testing.T) {
			ch := make(chan prometheus.Metric, 8)

			if err := coll.Update(context.Background(), client, ch); err == nil {
				t.Error("expected an error without a rados connection, got nil")
			}

			close(ch)
			if len(ch) != 0 {
				t.Errorf("expected no metrics, got %d", len(ch))
			}
		})
	}
}

// A cancelled context must stop the walk before it opens anything. Combined with
// the guard above this covers both of eachRBDImage's early exits.
func TestEachRBDImageRespectsCancelledContext(t *testing.T) {
	client := &Client{Name: "default", Config: &config.Config{}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := eachRBDImage(ctx, client, func(string, string, string, *rbd.Image) error {
		t.Error("the per image function must not run with a cancelled context")

		return nil
	}); err == nil {
		t.Fatal("expected an error, got nil")
	}
}

func TestEmitRejectsMismatchedLabels(t *testing.T) {
	ch := make(chan prometheus.Metric, 1)

	// provisioned_bytes declares three labels; one value must be refused rather
	// than panic the way MustNewConstMetric would.
	if err := Emit(ch, rbdImageProvisionedBytesDesc, 1, "only-pool"); err == nil {
		t.Error("expected an error for a label count mismatch, got nil")
	}

	if err := Emit(ch, rbdImageProvisionedBytesDesc, 1, "pool", "ns", "image"); err != nil {
		t.Errorf("unexpected error for the correct label count: %v", err)
	}
}

// Every RBD collector must be registered under the name the config and flags use,
// otherwise loadCollectors fails at startup with "collector not available".
func TestRBDFactoriesRegistered(t *testing.T) {
	for _, name := range []string{"rbd_images", "rbd_image_usage", "rbd_volumes"} {
		factory, present := Factories[name]
		if !present {
			t.Errorf("collector %q is not registered", name)

			continue
		}

		built, err := factory()
		if err != nil {
			t.Errorf("factory for %q returned an error: %v", name, err)
		}
		if built == nil {
			t.Errorf("factory for %q returned nil", name)
		}
	}
}

// The RGW collectors share the synthetic default client, which has no RGW API. They
// must report that instead of dereferencing nil.
func TestRGWCollectorsReportMissingAPI(t *testing.T) {
	client := &Client{Name: "default", Config: &config.Config{}}

	for _, name := range []string{"rgw_buckets", "rgw_user_quota"} {
		t.Run(name, func(t *testing.T) {
			factory, present := Factories[name]
			if !present {
				t.Fatalf("collector %q is not registered", name)
			}

			coll, err := factory()
			if err != nil {
				t.Fatalf("factory returned an error: %v", err)
			}

			ch := make(chan prometheus.Metric, 8)
			err = coll.Update(context.Background(), client, ch)

			if err == nil {
				t.Fatal("expected an error without an RGW API connection, got nil")
			}
			if !strings.Contains(err.Error(), "no RGW API connection") {
				t.Errorf("error should explain the missing RGW API, got: %v", err)
			}
		})
	}
}
