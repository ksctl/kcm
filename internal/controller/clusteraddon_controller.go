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
	"errors"
	"slices"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	managev1 "github.com/ksctl/kcm/api/v1"
)

// ClusterAddonReconciler reconciles a ClusterAddon object
type ClusterAddonReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=manage.ksctl.com,resources=clusteraddons,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=manage.ksctl.com,resources=clusteraddons/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=manage.ksctl.com,resources=clusteraddons/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the ClusterAddon object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.4/pkg/reconcile
func (r *ClusterAddonReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := log.FromContext(ctx)

	l.Info("Reconciling ClusterAddon", "name", req.NamespacedName, "namespace", req.Namespace) // TODO: need it non-namespace based resource

	// Fetch the ClusterAddon instance
	instance := &managev1.ClusterAddon{}
	err := r.Get(ctx, req.NamespacedName, instance)
	if err != nil {
		l.Error(err, "Failed to get ClusterAddon")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if instance.Status.StatusCode == "" {
		instance.Status.StatusCode = managev1.CAddonStatusPending
		_ = r.Status().Update(ctx, instance)
	}

	if instance.DeletionTimestamp.IsZero() { // no deletion
		if !slices.Contains(instance.Finalizers, "finalizer.manage.ksctl.com") {
			instance.Finalizers = append(instance.Finalizers, "finalizer.manage.ksctl.com")
			if err := r.Update(ctx, instance); err != nil {
				return ctrl.Result{}, err
			}
		} else {
			for _, addon := range instance.Spec.Addons {

				switch addon.Name {
				case string(AddonSKUStack):
					l.Info("Processing addon", "name", addon.Name)
					if errAddon := r.HandleAddon(ctx, addon.Name); errAddon != nil {
						l.Error(errAddon, "Failed to handle addon", "name", addon.Name)
						instance.Status.StatusCode = managev1.CAddonStatusFailure
						instance.Status.ReasonOfFailure = "Failed to handle addon " + addon.Name
						_ = r.Status().Update(ctx, instance)
						return ctrl.Result{}, nil
					}

				default:
					l.Error(errors.New("NOT_FOUND"), "Addon not found", "name", addon.Name)
					instance.Status.StatusCode = managev1.CAddonStatusFailure
					instance.Status.ReasonOfFailure = "Addon not found but got " + addon.Name
					_ = r.Status().Update(ctx, instance)
					return ctrl.Result{}, nil
				}
			}
			instance.Status.StatusCode = managev1.CAddonStatusSuccess
			_ = r.Status().Update(ctx, instance)
		}

	} else {
		if slices.Contains(instance.Finalizers, "finalizer.manage.ksctl.com") {
			if err := r.HandleAddonDelete(ctx); err != nil {
				l.Error(err, "Failed to handle finalizer")
				return ctrl.Result{}, err
			}
		}

		l.Info("Uninstall Addons was successful")

		instance.ObjectMeta.Finalizers = removeString(instance.ObjectMeta.Finalizers, "finalizer.manage.ksctl.com")
		if err := r.Update(ctx, instance); err != nil { // Use the correct context
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
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
