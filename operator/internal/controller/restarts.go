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
	corev1 "k8s.io/api/core/v1"

	workloadv1alpha1 "github.com/romankopylov/etherealpod-operator/api/v1alpha1"
)

// applyRestartAccounting folds the currently observed pod (nil when absent)
// into the EtherealPod restart counters. It is level-based and convergent:
// banking happens only on a pod-UID transition, so re-running it against an
// unchanged observed state never modifies the status.
//
// Rules (in order):
//   - live restarts = sum of containerStatuses[].restartCount (0 when absent);
//   - no pod tracked yet and a pod exists: adopt it (no banking);
//   - tracked pod still present: refresh the live count;
//   - tracked pod gone or replaced by a different UID: bank the last observed
//     live count plus one for the replacement itself, then adopt the new pod
//     (or clear tracking until one appears);
//   - restarts = banked + live for the current incarnation.
//
// Restarts that happen between the last recorded observation and the pod's
// death are lost; this slippage is bounded by the reconcile cadence and is an
// accepted trade-off (see design notes in the README).
func applyRestartAccounting(status *workloadv1alpha1.EtherealPodStatus, pod *corev1.Pod) {
	live := int32(0)
	if pod != nil {
		for _, cs := range pod.Status.ContainerStatuses {
			live += cs.RestartCount
		}
	}

	switch {
	case status.PodUID == "" && pod == nil:
		// Nothing tracked, nothing observed.
	case status.PodUID == "":
		// First pod, or a replacement whose predecessor was already banked.
		status.PodUID = pod.UID
		status.ObservedPodRestarts = live
	case pod != nil && pod.UID == status.PodUID:
		status.ObservedPodRestarts = live
	default:
		// Tracked pod vanished or was replaced: one banking per transition.
		status.BankedRestarts += status.ObservedPodRestarts + 1
		if pod != nil {
			status.PodUID = pod.UID
			status.ObservedPodRestarts = live
		} else {
			status.PodUID = ""
			status.ObservedPodRestarts = 0
		}
	}

	status.Restarts = status.BankedRestarts + status.ObservedPodRestarts
}
