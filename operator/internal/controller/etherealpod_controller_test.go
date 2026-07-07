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
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	workloadv1alpha1 "github.com/romankopylov/etherealpod-operator/api/v1alpha1"
)

const (
	testNamespace     = "default"
	testContainerName = "main"
	testImage         = "alpine:3.20"
)

func newTestEP(name string) *workloadv1alpha1.EtherealPod {
	return &workloadv1alpha1.EtherealPod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: workloadv1alpha1.EtherealPodSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					// A policy the controller must override with Always.
					RestartPolicy: corev1.RestartPolicyOnFailure,
					Containers: []corev1.Container{{
						Name:  testContainerName,
						Image: testImage,
					}},
				},
			},
		},
	}
}

var _ = Describe("EtherealPod Controller", func() {
	var reconciler *EtherealPodReconciler
	ctx := context.Background()

	BeforeEach(func() {
		reconciler = &EtherealPodReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: events.NewFakeRecorder(64),
		}
	})

	reconcileOnce := func(name string) reconcile.Result {
		GinkgoHelper()
		result, err := reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: name, Namespace: testNamespace},
		})
		Expect(err).NotTo(HaveOccurred())
		return result
	}

	getPod := func(name string) *corev1.Pod {
		GinkgoHelper()
		pod := &corev1.Pod{}
		Expect(k8sClient.Get(ctx,
			types.NamespacedName{Name: name, Namespace: testNamespace}, pod)).To(Succeed())
		return pod
	}

	getEP := func(name string) *workloadv1alpha1.EtherealPod {
		GinkgoHelper()
		ep := &workloadv1alpha1.EtherealPod{}
		Expect(k8sClient.Get(ctx,
			types.NamespacedName{Name: name, Namespace: testNamespace}, ep)).To(Succeed())
		return ep
	}

	readyCondition := func(ep *workloadv1alpha1.EtherealPod) *metav1.Condition {
		return meta.FindStatusCondition(ep.Status.Conditions, conditionTypeReady)
	}

	It("creates the managed pod with deterministic name, ownership and forced restart policy", func() {
		ep := newTestEP("ep-create")
		Expect(k8sClient.Create(ctx, ep)).To(Succeed())

		reconcileOnce(ep.Name)

		pod := getPod(ep.Name)
		Expect(pod.Spec.RestartPolicy).To(Equal(corev1.RestartPolicyAlways))
		controllerRef := metav1.GetControllerOf(pod)
		Expect(controllerRef).NotTo(BeNil())
		Expect(controllerRef.Kind).To(Equal("EtherealPod"))
		Expect(controllerRef.Name).To(Equal(ep.Name))
		Expect(pod.Annotations).To(HaveKey(templateHashAnnotation))

		updated := getEP(ep.Name)
		Expect(updated.Status.PodName).To(Equal(ep.Name))
		Expect(updated.Status.PodUID).To(Equal(pod.UID))
		Expect(updated.Status.Restarts).To(BeZero())
	})

	It("tracks live container restarts of the same pod incarnation", func() {
		ep := newTestEP("ep-live")
		Expect(k8sClient.Create(ctx, ep)).To(Succeed())
		reconcileOnce(ep.Name)

		pod := getPod(ep.Name)
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name: testContainerName, Image: testImage, RestartCount: 2,
		}}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

		reconcileOnce(ep.Name)

		Expect(getEP(ep.Name).Status.Restarts).To(Equal(int32(2)))
	})

	It("recreates a fully deleted pod and banks the replacement", func() {
		ep := newTestEP("ep-recreate")
		Expect(k8sClient.Create(ctx, ep)).To(Succeed())
		reconcileOnce(ep.Name)

		pod := getPod(ep.Name)
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name: testContainerName, Image: testImage, RestartCount: 2,
		}}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())
		reconcileOnce(ep.Name)

		oldUID := pod.UID
		Expect(k8sClient.Delete(ctx, pod,
			client.GracePeriodSeconds(0))).To(Succeed())

		reconcileOnce(ep.Name)

		newPod := getPod(ep.Name)
		Expect(newPod.UID).NotTo(Equal(oldUID))

		updated := getEP(ep.Name)
		// 2 live restarts of the dead pod + 1 for the replacement itself.
		Expect(updated.Status.Restarts).To(Equal(int32(3)))
		Expect(updated.Status.BankedRestarts).To(Equal(int32(3)))
		Expect(updated.Status.PodUID).To(Equal(newPod.UID))
	})

	It("waits for a terminating pod instead of creating or force-deleting", func() {
		ep := newTestEP("ep-terminating")
		Expect(k8sClient.Create(ctx, ep)).To(Succeed())
		reconcileOnce(ep.Name)

		pod := getPod(ep.Name)
		// Unscheduled pods are deleted immediately by the API server (graceful
		// deletion applies only to pods bound to a node), so hold the pod in
		// the Terminating state with a finalizer to observe that window.
		const testFinalizer = "workload.sunday.io/test-hold"
		pod.Finalizers = append(pod.Finalizers, testFinalizer)
		Expect(k8sClient.Update(ctx, pod)).To(Succeed())
		DeferCleanup(func() {
			held := getPod(ep.Name)
			held.Finalizers = nil
			Expect(k8sClient.Update(ctx, held)).To(Succeed())
		})
		Expect(k8sClient.Delete(ctx, pod)).To(Succeed())
		Expect(getPod(ep.Name).DeletionTimestamp).NotTo(BeNil())

		result := reconcileOnce(ep.Name)
		Expect(result.RequeueAfter).To(BeNumerically(">", 0))

		updated := getEP(ep.Name)
		cond := readyCondition(updated)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal(reasonRecovering))
	})

	It("deletes a pod in a terminal phase so it can be replaced", func() {
		ep := newTestEP("ep-terminal")
		Expect(k8sClient.Create(ctx, ep)).To(Succeed())
		reconcileOnce(ep.Name)

		pod := getPod(ep.Name)
		pod.Status.Phase = corev1.PodFailed
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

		reconcileOnce(ep.Name)

		afterPod := &corev1.Pod{}
		err := k8sClient.Get(ctx,
			types.NamespacedName{Name: ep.Name, Namespace: testNamespace}, afterPod)
		if err == nil {
			Expect(afterPod.DeletionTimestamp).NotTo(BeNil())
		} else {
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		}
	})

	It("deletes the pod when the pod template drifts from the created pod", func() {
		ep := newTestEP("ep-drift")
		Expect(k8sClient.Create(ctx, ep)).To(Succeed())
		reconcileOnce(ep.Name)
		Expect(getPod(ep.Name).DeletionTimestamp).To(BeNil())

		updatedEP := getEP(ep.Name)
		updatedEP.Spec.Template.Spec.Containers[0].Image = "alpine:3.21"
		Expect(k8sClient.Update(ctx, updatedEP)).To(Succeed())

		reconcileOnce(ep.Name)

		afterPod := &corev1.Pod{}
		err := k8sClient.Get(ctx,
			types.NamespacedName{Name: ep.Name, Namespace: testNamespace}, afterPod)
		if err == nil {
			Expect(afterPod.DeletionTimestamp).NotTo(BeNil())
		} else {
			Expect(apierrors.IsNotFound(err)).To(BeTrue())
		}
	})

	It("reports ImagePullError when the container image cannot be pulled", func() {
		ep := newTestEP("ep-imagepull")
		Expect(k8sClient.Create(ctx, ep)).To(Succeed())
		reconcileOnce(ep.Name)

		pod := getPod(ep.Name)
		pod.Status.Phase = corev1.PodPending
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name: testContainerName, Image: testImage,
			State: corev1.ContainerState{
				Waiting: &corev1.ContainerStateWaiting{Reason: "ErrImagePull"},
			},
		}}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

		reconcileOnce(ep.Name)

		cond := readyCondition(getEP(ep.Name))
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal(reasonImagePullError))
	})

	It("does not adopt or delete a foreign pod squatting the name", func() {
		foreign := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "ep-conflict", Namespace: testNamespace},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: testContainerName, Image: testImage}},
			},
		}
		Expect(k8sClient.Create(ctx, foreign)).To(Succeed())

		ep := newTestEP("ep-conflict")
		Expect(k8sClient.Create(ctx, ep)).To(Succeed())

		result := reconcileOnce(ep.Name)
		Expect(result.RequeueAfter).To(BeNumerically(">", 0))

		survivor := getPod(ep.Name)
		Expect(survivor.UID).To(Equal(foreign.UID))
		Expect(survivor.DeletionTimestamp).To(BeNil())
		Expect(metav1.GetControllerOf(survivor)).To(BeNil())

		cond := readyCondition(getEP(ep.Name))
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		Expect(cond.Reason).To(Equal(reasonNameConflict))
	})

	It("marks the EtherealPod Ready when the pod is running", func() {
		ep := newTestEP("ep-running")
		Expect(k8sClient.Create(ctx, ep)).To(Succeed())
		reconcileOnce(ep.Name)

		pod := getPod(ep.Name)
		pod.Status.Phase = corev1.PodRunning
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

		reconcileOnce(ep.Name)

		cond := readyCondition(getEP(ep.Name))
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		Expect(cond.Reason).To(Equal(reasonRunning))
	})
})
