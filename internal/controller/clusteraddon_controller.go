/*
Copyright 2025.

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
	"slices"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/dynamic"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	managev1 "github.com/ksctl/kcm/api/v1"
)

// ClusterAddonReconciler reconciles a ClusterAddon object
type ClusterAddonReconciler struct {
	client.Client
	DynamicClient dynamic.Interface
	RESTMapper    meta.RESTMapper
	Scheme        *runtime.Scheme
}

// +kubebuilder:rbac:groups=manage.ksctl.com,resources=clusteraddons,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=manage.ksctl.com,resources=clusteraddons/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=manage.ksctl.com,resources=clusteraddons/finalizers,verbs=update
// +kubebuilder:rbac:groups=*,resources=*,verbs=get;list;watch;create;update;patch;delete

func (r *ClusterAddonReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)
	l.Info("Reconciling ClusterAddon", "name", req.NamespacedName)

	// Get the ClusterAddon instance
	instance := &managev1.ClusterAddon{}
	if err := r.Get(ctx, req.NamespacedName, instance); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return ctrl.Result{}, nil // Object deleted, no requeue
		}
		l.Error(err, "Failed to get ClusterAddon")
		return ctrl.Result{RequeueAfter: time.Second * 10}, err
	}

	if instance.Status.StatusCode == "" {
		instance.Status.StatusCode = managev1.CAddonStatusPending
		if err := r.Status().Update(ctx, instance); err != nil {
			l.Error(err, "Failed to update initial status")
			return ctrl.Result{RequeueAfter: time.Second * 5}, err
		}
	}

	if !instance.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, instance)
	}

	if !slices.Contains(instance.Finalizers, "finalizer.manage.ksctl.com") {
		return r.addFinalizer(ctx, instance)
	}

	// Process addons
	return r.processAddons(ctx, instance)
}

func (r *ClusterAddonReconciler) handleDeletion(ctx context.Context, instance *managev1.ClusterAddon) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	if !slices.Contains(instance.Finalizers, "finalizer.manage.ksctl.com") {
		return ctrl.Result{}, nil
	}

	for _, addon := range instance.Spec.Addons {
		if err := r.validateAndProcessAddon(ctx, addon, r.HandleAddonDelete); err != nil {
			l.Error(err, "Failed to process addon", "name", addon.Name)
			instance.Status.StatusCode = managev1.CAddonStatusFailure
			instance.Status.ReasonOfFailure = fmt.Sprintf("Failed to process addon %s: %v", addon.Name, err)
			if updateErr := r.Status().Update(ctx, instance); updateErr != nil {
				l.Error(updateErr, "Failed to update failure status")
			}
			return ctrl.Result{RequeueAfter: time.Second * 30, Requeue: true}, err
		}
	}

	instance.Finalizers = removeString(instance.Finalizers, "finalizer.manage.ksctl.com")
	if err := r.Update(ctx, instance); err != nil {
		l.Error(err, "Failed to remove finalizer")
		return ctrl.Result{RequeueAfter: time.Second * 5}, err
	}

	return ctrl.Result{}, nil
}

func (r *ClusterAddonReconciler) addFinalizer(ctx context.Context, instance *managev1.ClusterAddon) (ctrl.Result, error) {
	instance.Finalizers = append(instance.Finalizers, "finalizer.manage.ksctl.com")
	if err := r.Update(ctx, instance); err != nil {
		return ctrl.Result{RequeueAfter: time.Second * 5}, err
	}
	return ctrl.Result{Requeue: true}, nil
}

func (r *ClusterAddonReconciler) processAddons(ctx context.Context, instance *managev1.ClusterAddon) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	for _, addon := range instance.Spec.Addons {
		if err := r.validateAndProcessAddon(ctx, addon, r.HandleAddon); err != nil {
			l.Error(err, "Failed to process addon", "name", addon.Name)
			instance.Status.StatusCode = managev1.CAddonStatusFailure
			instance.Status.ReasonOfFailure = fmt.Sprintf("Failed to process addon %s: %v", addon.Name, err)
			if updateErr := r.Status().Update(ctx, instance); updateErr != nil {
				l.Error(updateErr, "Failed to update failure status")
			}
			return ctrl.Result{RequeueAfter: time.Second * 30, Requeue: true}, err
		}
	}

	// Update success status
	instance.Status.StatusCode = managev1.CAddonStatusSuccess
	instance.Status.ReasonOfFailure = ""
	if err := r.Status().Update(ctx, instance); err != nil {
		l.Error(err, "Failed to update success status")
		return ctrl.Result{RequeueAfter: time.Second * 5}, err
	}

	return ctrl.Result{RequeueAfter: time.Minute * 5}, nil // Periodic reconciliation
}

func (r *ClusterAddonReconciler) validateAndProcessAddon(
	ctx context.Context,
	addon managev1.Addon,
	process func(ctx context.Context, addonName string) error,
) error {

	keys := make([]string, 0, len(addonManifests))
	for k := range addonManifests {
		keys = append(keys, k)
	}

	if !slices.Contains(keys, addon.Name) {
		return fmt.Errorf("unsupported addon: %s", addon.Name)
	}

	return process(ctx, addon.Name)
}

func removeString(slice []string, s string) (result []string) {
	for _, item := range slice {
		if item != s {
			result = append(result, item)
		}
	}
	return
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterAddonReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&managev1.ClusterAddon{}).
		Named("clusteraddon").
		Complete(r)
}
