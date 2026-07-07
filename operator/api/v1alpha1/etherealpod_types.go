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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

// EtherealPodSpec defines the desired state of EtherealPod
type EtherealPodSpec struct {
	// template describes the pod that will be created and kept running.
	// The pod is created with a deterministic name equal to the EtherealPod
	// name; spec.restartPolicy is always forced to Always so containers are
	// restarted in place by the kubelet.
	// +required
	Template corev1.PodTemplateSpec `json:"template"`
}

// EtherealPodStatus defines the observed state of EtherealPod.
type EtherealPodStatus struct {
	// restarts is the cumulative restart count of the managed pod: container
	// restarts observed by the kubelet plus one for every pod replacement
	// performed by the controller. It never resets while the EtherealPod exists.
	// +optional
	Restarts int32 `json:"restarts"`

	// bankedRestarts accumulates restarts of pod incarnations that no longer
	// exist (each replacement banks the last observed live count plus one).
	// +optional
	BankedRestarts int32 `json:"bankedRestarts,omitempty"`

	// podName is the name of the managed pod (always equal to the
	// EtherealPod name).
	// +optional
	PodName string `json:"podName,omitempty"`

	// podUID is the UID of the currently tracked pod incarnation; a UID
	// change is how pod replacement is detected.
	// +optional
	PodUID types.UID `json:"podUID,omitempty"`

	// observedPodRestarts is the last live sum of
	// containerStatuses[].restartCount seen for podUID.
	// +optional
	ObservedPodRestarts int32 `json:"observedPodRestarts,omitempty"`

	// conditions represent the current state of the EtherealPod resource.
	// A single Ready condition is maintained (reasons: Running, Recovering,
	// ImagePullError, NameConflict).
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=eps
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="Restarts",type="integer",JSONPath=".status.restarts"

// EtherealPod is the Schema for the etherealpods API
type EtherealPod struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of EtherealPod
	// +required
	Spec EtherealPodSpec `json:"spec"`

	// status defines the observed state of EtherealPod
	// +optional
	Status EtherealPodStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// EtherealPodList contains a list of EtherealPod
type EtherealPodList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []EtherealPod `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &EtherealPod{}, &EtherealPodList{})
		return nil
	})
}
