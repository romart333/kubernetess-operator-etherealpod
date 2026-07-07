/*
Copyright 2026.

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

package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	workloadv1alpha1 "github.com/romankopylov/etherealpod-operator/api/v1alpha1"
)

// UIDs of two consecutive pod incarnations in the accounting scenarios.
const (
	uidFirst  types.UID = "uid-1"
	uidSecond types.UID = "uid-2"
)

func podWithRestarts(uid types.UID, restartCounts ...int32) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "example-ep", UID: uid},
	}
	for _, c := range restartCounts {
		pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses,
			corev1.ContainerStatus{RestartCount: c})
	}
	return pod
}

func TestApplyRestartAccounting(t *testing.T) {
	tests := []struct {
		name   string
		status workloadv1alpha1.EtherealPodStatus
		pod    *corev1.Pod
		want   workloadv1alpha1.EtherealPodStatus
	}{
		{
			name:   "no pod ever seen is a no-op",
			status: workloadv1alpha1.EtherealPodStatus{},
			pod:    nil,
			want:   workloadv1alpha1.EtherealPodStatus{},
		},
		{
			name:   "adoption of the first pod",
			status: workloadv1alpha1.EtherealPodStatus{},
			pod:    podWithRestarts(uidFirst, 0),
			want: workloadv1alpha1.EtherealPodStatus{
				PodUID: uidFirst,
			},
		},
		{
			name:   "adoption of a pod already reporting restarts",
			status: workloadv1alpha1.EtherealPodStatus{},
			pod:    podWithRestarts(uidFirst, 2),
			want: workloadv1alpha1.EtherealPodStatus{
				Restarts:            2,
				PodUID:              uidFirst,
				ObservedPodRestarts: 2,
			},
		},
		{
			name: "same incarnation updates live count",
			status: workloadv1alpha1.EtherealPodStatus{
				Restarts:            1,
				PodUID:              uidFirst,
				ObservedPodRestarts: 1,
			},
			pod: podWithRestarts(uidFirst, 3),
			want: workloadv1alpha1.EtherealPodStatus{
				Restarts:            3,
				PodUID:              uidFirst,
				ObservedPodRestarts: 3,
			},
		},
		{
			name: "multi-container restarts are summed",
			status: workloadv1alpha1.EtherealPodStatus{
				PodUID: uidFirst,
			},
			pod: podWithRestarts(uidFirst, 2, 3),
			want: workloadv1alpha1.EtherealPodStatus{
				Restarts:            5,
				PodUID:              uidFirst,
				ObservedPodRestarts: 5,
			},
		},
		{
			name: "pod without container statuses yet reports zero live",
			status: workloadv1alpha1.EtherealPodStatus{
				PodUID: uidFirst,
			},
			pod: podWithRestarts(uidFirst),
			want: workloadv1alpha1.EtherealPodStatus{
				PodUID: uidFirst,
			},
		},
		{
			name: "pod disappearance banks observed count plus one",
			status: workloadv1alpha1.EtherealPodStatus{
				Restarts:            2,
				PodUID:              uidFirst,
				ObservedPodRestarts: 2,
			},
			pod: nil,
			want: workloadv1alpha1.EtherealPodStatus{
				Restarts:       3,
				BankedRestarts: 3,
			},
		},
		{
			name: "replacement pod after banked absence is adopted without extra banking",
			status: workloadv1alpha1.EtherealPodStatus{
				Restarts:       3,
				BankedRestarts: 3,
			},
			pod: podWithRestarts(uidSecond, 0),
			want: workloadv1alpha1.EtherealPodStatus{
				Restarts:       3,
				BankedRestarts: 3,
				PodUID:         uidSecond,
			},
		},
		{
			name: "direct UID swap banks and adopts in one pass",
			status: workloadv1alpha1.EtherealPodStatus{
				Restarts:            2,
				PodUID:              uidFirst,
				ObservedPodRestarts: 2,
			},
			pod: podWithRestarts(uidSecond, 0),
			want: workloadv1alpha1.EtherealPodStatus{
				Restarts:       3,
				BankedRestarts: 3,
				PodUID:         uidSecond,
			},
		},
		{
			name: "restarts accumulate across banking and new live restarts",
			status: workloadv1alpha1.EtherealPodStatus{
				Restarts:       3,
				BankedRestarts: 3,
				PodUID:         uidSecond,
			},
			pod: podWithRestarts(uidSecond, 4),
			want: workloadv1alpha1.EtherealPodStatus{
				Restarts:            7,
				BankedRestarts:      3,
				PodUID:              uidSecond,
				ObservedPodRestarts: 4,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.status
			applyRestartAccounting(&got, tt.pod)
			assert.Equal(t, tt.want, got)

			// Re-running with identical observed state must be a no-op
			// (level-based accounting is convergent).
			rerun := got
			applyRestartAccounting(&rerun, tt.pod)
			assert.Equal(t, got, rerun, "second identical pass must not change status")
		})
	}
}
