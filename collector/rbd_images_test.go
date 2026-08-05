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
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// emitFunc adapts one of the per image emit helpers to prometheus.Collector so
// that testutil can compare the exact metric text, label values included.
type emitFunc func(ch chan<- prometheus.Metric) error

func (f emitFunc) Describe(chan<- *prometheus.Desc) {}

func (f emitFunc) Collect(ch chan<- prometheus.Metric) { _ = f(ch) }

// fullyTagged is the schema the provisioning pipeline writes on a current image.
func fullyTagged() map[string]string {
	return map[string]string{
		"e2e.resource_type":       "compute_vm",
		"e2e.vm_id":               "100054",
		"e2e.project":             "p-532",
		"e2e.customer_id":         "1564",
		"e2e.customer_email":      "jatin.jeena@e2enetworks.com",
		"e2e.billed_customer_crn": "4236",
	}
}

func TestEmitRBDImageOwnerFullyTagged(t *testing.T) {
	expected := `
# HELP custom_rbd_image_owner Ownership mapping for the RBD image; value is always 1
# TYPE custom_rbd_image_owner gauge
custom_rbd_image_owner{billed_customer_crn="4236",customer_email="jatin.jeena@e2enetworks.com",customer_id="1564",image="csi-vol-aaaac365",namespace="",pool="vm-storage-pool",project="p-532",resource_type="compute_vm",vm_id="100054"} 1
`

	collector := emitFunc(func(ch chan<- prometheus.Metric) error {
		return emitRBDImageOwner(ch, "vm-storage-pool", "", "csi-vol-aaaac365", fullyTagged())
	})

	if err := testutil.CollectAndCompare(collector, strings.NewReader(expected), "custom_rbd_image_owner"); err != nil {
		t.Fatal(err)
	}
}

// A partially tagged image must still be reported, with the absent keys rendered
// as empty labels, so it does not silently drop out of tenant queries. Four such
// images exist on the current cluster.
func TestEmitRBDImageOwnerPartiallyTagged(t *testing.T) {
	expected := `
# HELP custom_rbd_image_owner Ownership mapping for the RBD image; value is always 1
# TYPE custom_rbd_image_owner gauge
custom_rbd_image_owner{billed_customer_crn="",customer_email="admin@ceph.com",customer_id="1",image="csi-vol-922d93b4",namespace="",pool="vm-storage-pool",project="",resource_type="",vm_id=""} 1
`

	meta := map[string]string{
		"e2e.customer_email": "admin@ceph.com",
		"e2e.customer_id":    "1",
	}

	collector := emitFunc(func(ch chan<- prometheus.Metric) error {
		return emitRBDImageOwner(ch, "vm-storage-pool", "", "csi-vol-922d93b4", meta)
	})

	if err := testutil.CollectAndCompare(collector, strings.NewReader(expected), "custom_rbd_image_owner"); err != nil {
		t.Fatal(err)
	}
}

// An image with no ownership keys must produce no series at all, which is what
// makes untagged images countable against provisioned_bytes.
func TestEmitRBDImageOwnerUntaggedEmitsNothing(t *testing.T) {
	meta := map[string]string{
		"csi.storage.k8s.io/pvc/namespace": "p-123",
	}

	collector := emitFunc(func(ch chan<- prometheus.Metric) error {
		return emitRBDImageOwner(ch, "vm-storage-pool", "", "csi-vol-08bb9135", meta)
	})

	if got := testutil.CollectAndCount(collector, "custom_rbd_image_owner"); got != 0 {
		t.Fatalf("expected no owner series for an untagged image, got %d", got)
	}
}

// The owner Desc's label names are derived from ownershipLabels, and the values
// are appended in that same order. If those ever drift, NewConstMetric returns an
// error rather than panicking, and this test is what catches the drift.
func TestOwnerLabelCountMatchesDesc(t *testing.T) {
	if err := emitRBDImageOwner(make(chan prometheus.Metric, 1), "p", "ns", "img", fullyTagged()); err != nil {
		t.Fatalf("owner label values do not match the Desc: %v", err)
	}

	wantLabels := len(rbdImageLabels) + len(ownershipLabels)
	if got := len(ownerLabelNames()); got != wantLabels {
		t.Fatalf("ownerLabelNames() returned %d labels, want %d", got, wantLabels)
	}
}

func TestEmitRBDImageQoS(t *testing.T) {
	tests := []struct {
		name      string
		meta      map[string]string
		expected  string
		wantErr   bool
		wantCount int
	}{
		{
			// Absent must mean no series. Ceph reads a configured 0 as unlimited, so
			// emitting 0 for an image with no override would invent a limit.
			name:      "absent key emits nothing",
			meta:      map[string]string{},
			wantCount: 0,
		},
		{
			// Two images on the cluster carry an explicit 0. That is a real state and
			// has to be reported, distinct from absent.
			name: "explicit zero is reported",
			meta: map[string]string{metaKeyQoSReadIOPS: "0"},
			expected: `
# HELP custom_rbd_image_qos_read_iops_limit Configured read IOPS limit per RBD image
# TYPE custom_rbd_image_qos_read_iops_limit gauge
custom_rbd_image_qos_read_iops_limit{image="img",namespace="",pool="p"} 0
`,
			wantCount: 1,
		},
		{
			name: "both limits reported",
			meta: map[string]string{metaKeyQoSReadIOPS: "800", metaKeyQoSWriteIOPS: "400"},
			expected: `
# HELP custom_rbd_image_qos_read_iops_limit Configured read IOPS limit per RBD image
# TYPE custom_rbd_image_qos_read_iops_limit gauge
custom_rbd_image_qos_read_iops_limit{image="img",namespace="",pool="p"} 800
# HELP custom_rbd_image_qos_write_iops_limit Configured write IOPS limit per RBD image
# TYPE custom_rbd_image_qos_write_iops_limit gauge
custom_rbd_image_qos_write_iops_limit{image="img",namespace="",pool="p"} 400
`,
			wantCount: 2,
		},
		{
			// Metadata is operator-writable, so a non-numeric value is reachable. It
			// must surface as an error rather than a panic.
			name:      "unparseable value errors without panicking",
			meta:      map[string]string{metaKeyQoSReadIOPS: "not-a-number"},
			wantErr:   true,
			wantCount: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var emitErr error
			collector := emitFunc(func(ch chan<- prometheus.Metric) error {
				emitErr = emitRBDImageQoS(ch, "p", "", "img", test.meta)

				return emitErr
			})

			names := []string{"custom_rbd_image_qos_read_iops_limit", "custom_rbd_image_qos_write_iops_limit"}
			if got := testutil.CollectAndCount(collector, names...); got != test.wantCount {
				t.Fatalf("collected %d series, want %d", got, test.wantCount)
			}

			if test.wantErr && emitErr == nil {
				t.Fatal("expected an error, got nil")
			}
			if !test.wantErr && emitErr != nil {
				t.Fatalf("unexpected error: %v", emitErr)
			}

			if test.expected != "" {
				if err := testutil.CollectAndCompare(collector, strings.NewReader(test.expected), names...); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// The LUKS2 wrapped data encryption key lives in image metadata alongside the keys
// this collector consumes. Narrowing the map at the source is what keeps it from
// reaching a log line or a label later.
func TestRetainConsumedMetadataDropsSecrets(t *testing.T) {
	meta := fullyTagged()
	meta[metaKeyQoSReadIOPS] = "800"
	meta["rbd.csi.ceph.com/dek"] = `{"dek":"c2VjcmV0","nonce":"abcd"}`
	meta["rbd.csi.ceph.com/encrypted"] = "encrypted"
	meta["csi.storage.k8s.io/pvc/namespace"] = "p-532"

	consumed := retainConsumedMetadata(meta)

	for _, dropped := range []string{"rbd.csi.ceph.com/dek", "rbd.csi.ceph.com/encrypted", "csi.storage.k8s.io/pvc/namespace"} {
		if _, present := consumed[dropped]; present {
			t.Errorf("key %q survived the filter", dropped)
		}
	}

	for key := range consumedMetadataKeys {
		want, inInput := meta[key]
		got, inOutput := consumed[key]
		if inInput != inOutput || want != got {
			t.Errorf("consumed key %q: got %q (present %t), want %q (present %t)", key, got, inOutput, want, inInput)
		}
	}

	if len(consumed) != len(ownershipLabels)+1 {
		t.Errorf("filtered map has %d keys, want %d", len(consumed), len(ownershipLabels)+1)
	}
}
