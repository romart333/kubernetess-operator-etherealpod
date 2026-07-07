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
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	workloadv1alpha1 "github.com/romankopylov/etherealpod-operator/api/v1alpha1"
)

// Ready condition vocabulary of the EtherealPod status.
const (
	conditionTypeReady = "Ready"

	reasonRunning        = "Running"
	reasonRecovering     = "Recovering"
	reasonImagePullError = "ImagePullError"
	reasonNameConflict   = "NameConflict"
)

// Event vocabulary: actions describe what the controller did (or refused to
// do) with the managed pod; event reasons describe why, separately from the
// Ready condition reasons above.
const (
	actionCreatePod    = "CreatePod"
	actionDeletePod    = "DeletePod"
	actionReconcilePod = "ReconcilePod"

	eventReasonCreated         = "Created"
	eventReasonTerminalPhase   = "TerminalPodReplaced"
	eventReasonTemplateDrifted = "PodTemplateDrifted"
)

const (
	// requeueWhileRecovering re-checks a pod that is terminating or being
	// replaced; kept short so recreation lands within the (small) grace
	// period of the managed pod.
	requeueWhileRecovering = 3 * time.Second
	// requeueOnNameConflict re-checks a name squatted by a foreign pod; the
	// foreign pod is not owned, so no watch event will wake us up.
	requeueOnNameConflict = 30 * time.Second
)

// imagePullWaitingReasons are the kubelet waiting reasons that mean the
// container image cannot be pulled; waiting is not a restart, so the pod is
// left in place and only the Ready condition reports the problem.
var imagePullWaitingReasons = map[string]bool{
	"ErrImagePull":     true,
	"ImagePullBackOff": true,
	"InvalidImageName": true,
}

// EtherealPodReconciler reconciles a EtherealPod object
type EtherealPodReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
}

// +kubebuilder:rbac:groups=workload.sunday.io,resources=etherealpods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=workload.sunday.io,resources=etherealpods/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=workload.sunday.io,resources=etherealpods/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile drives the observed state towards "exactly one running pod with
// the spec'd template per EtherealPod". It is level-based: every pass
// re-derives everything from the EtherealPod and the observed pod, so any
// individual pass may be repeated or skipped safely.
func (r *EtherealPodReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	ep := &workloadv1alpha1.EtherealPod{}
	if err := r.Get(ctx, req.NamespacedName, ep); err != nil {
		// Not found: the EtherealPod is gone and garbage collection removes
		// the pod via its owner reference; nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	base := ep.DeepCopy()

	// The managed pod has a deterministic name equal to the CR name.
	pod := &corev1.Pod{}
	if err := r.Get(ctx, req.NamespacedName, pod); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("get managed pod: %w", err)
		}
		pod = nil
	}

	// A pod with our name that we do not control must not be adopted or
	// deleted; report the conflict and check back periodically.
	if pod != nil && !metav1.IsControlledBy(pod, ep) {
		log.Info("managed pod name is taken by a foreign pod", "pod", pod.Name)
		r.Recorder.Eventf(ep, nil, corev1.EventTypeWarning, reasonNameConflict, actionReconcilePod,
			"pod %q exists but is not controlled by this EtherealPod; refusing to adopt or delete it", pod.Name)
		applyRestartAccounting(&ep.Status, nil)
		ep.Status.PodName = ""
		r.setReadyCondition(ep, metav1.ConditionFalse, reasonNameConflict,
			fmt.Sprintf("pod %q exists but is not controlled by this EtherealPod", pod.Name))
		if err := r.patchStatus(ctx, ep, base); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: requeueOnNameConflict}, nil
	}

	hash, err := templateHash(&ep.Spec.Template)
	if err != nil {
		return ctrl.Result{}, err
	}

	var result ctrl.Result
	skipAccounting := false

	switch {
	case pod == nil:
		newPod, err := buildPod(ep, hash, r.Scheme)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("build pod: %w", err)
		}
		if err := r.Create(ctx, newPod); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return ctrl.Result{}, fmt.Errorf("create pod: %w", err)
			}
			// Stale cache: the pod exists on the API server but is not in
			// our cache yet. The API server is the arbiter of uniqueness —
			// treat as success, skip accounting (we cannot see the live
			// pod), and let the next pass observe it.
			log.V(1).Info("pod already exists (stale cache), retrying shortly")
			skipAccounting = true
			result = ctrl.Result{RequeueAfter: requeueWhileRecovering}
		} else {
			log.Info("created managed pod", "pod", newPod.Name)
			r.Recorder.Eventf(ep, newPod, corev1.EventTypeNormal, eventReasonCreated, actionCreatePod,
				"created managed pod %q", newPod.Name)
			pod = newPod
		}
		r.setReadyCondition(ep, metav1.ConditionFalse, reasonRecovering,
			"managed pod created; waiting for it to start")

	case pod.DeletionTimestamp != nil:
		// The name is still taken by the terminating pod; never force-delete,
		// just wait for it to disappear and recreate then.
		r.setReadyCondition(ep, metav1.ConditionFalse, reasonRecovering,
			"waiting for the terminating pod to disappear")
		result = ctrl.Result{RequeueAfter: requeueWhileRecovering}

	case pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded:
		// Terminal phases are never restarted by the kubelet (node loss,
		// eviction, completed): delete and let the next pass recreate.
		log.Info("deleting pod in terminal phase", "pod", pod.Name, "phase", pod.Status.Phase)
		r.Recorder.Eventf(ep, pod, corev1.EventTypeNormal, eventReasonTerminalPhase, actionDeletePod,
			"deleting managed pod %q in terminal phase %s", pod.Name, pod.Status.Phase)
		if err := r.deletePod(ctx, pod); err != nil {
			return ctrl.Result{}, err
		}
		r.setReadyCondition(ep, metav1.ConditionFalse, reasonRecovering,
			fmt.Sprintf("replacing pod in terminal phase %s", pod.Status.Phase))
		result = ctrl.Result{RequeueAfter: requeueWhileRecovering}

	case pod.Annotations[templateHashAnnotation] != hash:
		// The EtherealPod template changed after the pod was created; pods
		// are (mostly) immutable, so replace the pod.
		log.Info("pod template drifted, deleting pod", "pod", pod.Name)
		r.Recorder.Eventf(ep, pod, corev1.EventTypeNormal, eventReasonTemplateDrifted, actionDeletePod,
			"pod template changed; deleting managed pod %q for replacement", pod.Name)
		if err := r.deletePod(ctx, pod); err != nil {
			return ctrl.Result{}, err
		}
		r.setReadyCondition(ep, metav1.ConditionFalse, reasonRecovering,
			"pod template changed; replacing the managed pod")
		result = ctrl.Result{RequeueAfter: requeueWhileRecovering}

	default:
		r.setConditionFromPodStatus(ep, pod)
	}

	if !skipAccounting {
		applyRestartAccounting(&ep.Status, pod)
		if pod != nil {
			ep.Status.PodName = pod.Name
		} else {
			ep.Status.PodName = ""
		}
	}

	if err := r.patchStatus(ctx, ep, base); err != nil {
		return ctrl.Result{}, err
	}
	return result, nil
}

// deletePod deletes the managed pod with a UID precondition so a replacement
// that reused the name is never deleted by a stale pass. NotFound and
// precondition conflicts mean the work is already done.
func (r *EtherealPodReconciler) deletePod(ctx context.Context, pod *corev1.Pod) error {
	err := r.Delete(ctx, pod, client.Preconditions{UID: &pod.UID})
	if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
		return fmt.Errorf("delete pod %q: %w", pod.Name, err)
	}
	return nil
}

// setConditionFromPodStatus derives the Ready condition for a live,
// non-terminating owned pod.
func (r *EtherealPodReconciler) setConditionFromPodStatus(ep *workloadv1alpha1.EtherealPod, pod *corev1.Pod) {
	for _, cs := range append(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses...) {
		if cs.State.Waiting != nil && imagePullWaitingReasons[cs.State.Waiting.Reason] {
			r.setReadyCondition(ep, metav1.ConditionFalse, reasonImagePullError,
				fmt.Sprintf("container %q cannot pull its image: %s", cs.Name, cs.State.Waiting.Reason))
			return
		}
	}
	if pod.Status.Phase == corev1.PodRunning {
		r.setReadyCondition(ep, metav1.ConditionTrue, reasonRunning, "managed pod is running")
		return
	}
	r.setReadyCondition(ep, metav1.ConditionFalse, reasonRecovering,
		fmt.Sprintf("managed pod is in phase %s", pod.Status.Phase))
}

func (r *EtherealPodReconciler) setReadyCondition(ep *workloadv1alpha1.EtherealPod, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&ep.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: ep.Generation,
	})
}

// patchStatus writes only the status subresource with an optimistic lock; a
// conflict is returned to the workqueue for a backed-off retry rather than
// resolved in place (plain MergeFrom would silently drop the lost update).
func (r *EtherealPodReconciler) patchStatus(ctx context.Context, ep, base *workloadv1alpha1.EtherealPod) error {
	patch := client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})
	if err := r.Status().Patch(ctx, ep, patch); err != nil {
		return fmt.Errorf("patch status: %w", err)
	}
	return nil
}

// SetupWithManager sets up the controller with the Manager. Owns(Pod) makes
// every status change of the managed pod (including restartCount bumps)
// trigger a reconcile, so no periodic resync is needed.
func (r *EtherealPodReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&workloadv1alpha1.EtherealPod{}).
		Owns(&corev1.Pod{}).
		Named("etherealpod").
		Complete(r)
}
