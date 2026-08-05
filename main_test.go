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
	"testing"
	"time"
)

func TestResolveRefreshIntervals(t *testing.T) {
	tests := []struct {
		name       string
		fromConfig map[string]time.Duration
		fromFlags  map[string]string
		want       map[string]time.Duration
		wantErr    bool
	}{
		{
			name: "no intervals anywhere",
			want: map[string]time.Duration{},
		},
		{
			name:       "config only",
			fromConfig: map[string]time.Duration{"rbd_images": 30 * time.Second},
			want:       map[string]time.Duration{"rbd_images": 30 * time.Second},
		},
		{
			name:      "flags only",
			fromFlags: map[string]string{"rbd_image_usage": "2m"},
			want:      map[string]time.Duration{"rbd_image_usage": 2 * time.Minute},
		},
		{
			// The merge is per collector, not a whole-map replacement: a flag for one
			// collector must not discard config entries for the others.
			name: "flag wins per collector and leaves the rest alone",
			fromConfig: map[string]time.Duration{
				"rbd_images":      30 * time.Second,
				"rbd_image_usage": 2 * time.Minute,
			},
			fromFlags: map[string]string{"rbd_images": "90s"},
			want: map[string]time.Duration{
				"rbd_images":      90 * time.Second,
				"rbd_image_usage": 2 * time.Minute,
			},
		},
		{
			name:      "an unparseable duration is an error, not a silent zero",
			fromFlags: map[string]string{"rbd_images": "half an hour"},
			wantErr:   true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveRefreshIntervals(test.fromConfig, test.fromFlags)

			if test.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}

				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(got) != len(test.want) {
				t.Fatalf("got %d intervals, want %d: %v", len(got), len(test.want), got)
			}
			for name, want := range test.want {
				if got[name] != want {
					t.Errorf("interval for %s = %v, want %v", name, got[name], want)
				}
			}
		})
	}
}

// resolveRefreshIntervals must not mutate the config map it is handed, since the
// caller keeps using cfg afterwards.
func TestResolveRefreshIntervalsDoesNotMutateInput(t *testing.T) {
	fromConfig := map[string]time.Duration{"rbd_images": 30 * time.Second}

	if _, err := resolveRefreshIntervals(fromConfig, map[string]string{"rbd_images": "90s"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := fromConfig["rbd_images"]; got != 30*time.Second {
		t.Fatalf("config map was mutated: rbd_images = %v, want 30s", got)
	}
}
