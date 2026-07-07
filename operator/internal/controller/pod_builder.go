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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"

	workloadv1alpha1 "github.com/romankopylov/etherealpod-operator/api/v1alpha1"
)

// templateHashAnnotation stores the hash of the EtherealPod template the pod
// was built from; a mismatch means the spec drifted and the pod is replaced.
const templateHashAnnotation = "workload.sunday.io/template-hash"

// templateHash returns a stable hash of the pod template. json.Marshal is
// deterministic (struct order is fixed, map keys are sorted), so equal
// templates always produce equal hashes.
func templateHash(template *corev1.PodTemplateSpec) (string, error) {
	raw, err := json.Marshal(template)
	if err != nil {
		return "", fmt.Errorf("marshal pod template: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8]), nil
}

// buildPod renders the managed pod for an EtherealPod: deterministic name
// equal to the CR name (the API server's uniqueness constraint is what
// guarantees "exactly one"), restartPolicy forced to Always so the kubelet
// restarts crashed containers in place, and a controller owner reference so
// deletion of the EtherealPod cascades to the pod.
func buildPod(ep *workloadv1alpha1.EtherealPod, hash string, scheme *runtime.Scheme) (*corev1.Pod, error) {
	tmpl := ep.Spec.Template.DeepCopy()

	annotations := make(map[string]string, len(tmpl.Annotations)+1)
	maps.Copy(annotations, tmpl.Annotations)
	annotations[templateHashAnnotation] = hash

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        ep.Name,
			Namespace:   ep.Namespace,
			Labels:      tmpl.Labels,
			Annotations: annotations,
		},
		Spec: tmpl.Spec,
	}
	pod.Spec.RestartPolicy = corev1.RestartPolicyAlways

	if err := ctrl.SetControllerReference(ep, pod, scheme); err != nil {
		return nil, fmt.Errorf("set controller reference: %w", err)
	}
	return pod, nil
}
