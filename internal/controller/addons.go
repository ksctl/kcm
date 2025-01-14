package controller

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type AddonManifest struct {
	Name      string
	URL       string
	Namespace *string
}

var addonManifests = map[string]AddonManifest{
	"stack": {
		Name: "stack",
		URL:  "https://raw.githubusercontent.com/argoproj/argo-cd/stable/manifests/install.yaml",
		Namespace: func() *string {
			ns := "argocd"
			return &ns
		}(),
	},
}

func NewAddonData() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kcm-addons",
			Namespace: "ksctl-system",
		},
	}
}

func (r *ClusterAddonReconciler) CreateNamespaceIfNotExists(ctx context.Context, namespace string) error {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
		},
	}
	if err := r.Get(ctx, client.ObjectKey{Name: namespace}, ns); err != nil {
		if errors.IsNotFound(err) {
			return r.Create(ctx, ns)
		}
		return err
	}
	return nil
}

func (r *ClusterAddonReconciler) GetData(ctx context.Context) (cf *corev1.ConfigMap, err error) {
	cf = NewAddonData()
	err = r.Get(ctx, client.ObjectKey{Namespace: cf.Namespace, Name: cf.Name}, cf)
	if !errors.IsNotFound(err) {
		return nil, err
	} else {
		err = nil
	}
	return
}

func (r *ClusterAddonReconciler) UpdateData(ctx context.Context, cf *corev1.ConfigMap) error {
	if err := r.Update(ctx, cf); err != nil {
		if errors.IsNotFound(err) {
			return r.Create(ctx, cf)
		}
		return err
	}
	return nil
}

func (r *ClusterAddonReconciler) HandleAddon(ctx context.Context, addonName string) error {
	manifest, ok := addonManifests[addonName]
	if !ok {
		return fmt.Errorf("addon %s not found in manifest registry", addonName)
	}

	// Get or create ConfigMap to track installations
	cf, err := r.GetOrCreateConfigMap(ctx)
	if err != nil {
		return fmt.Errorf("failed to get/create config map: %w", err)
	}

	if _, installed := cf.Data[addonName]; installed {
		return nil
	}

	if err := r.downloadAndApplyManifests(ctx, manifest.URL); err != nil {
		return fmt.Errorf("failed to install addon %s: %w", addonName, err)
	}

	return r.updateAddonStatus(ctx, cf, addonName)
}

func (r *ClusterAddonReconciler) downloadAndApplyManifests(ctx context.Context, manifestURL string) error {
	resp, err := http.Get(manifestURL)
	if err != nil {
		return fmt.Errorf("failed to download manifest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download manifest, status: %d", resp.StatusCode)
	}

	decoder := yaml.NewYAMLOrJSONDecoder(resp.Body, 4096)
	var rawObj map[string]interface{}
	if err := decoder.Decode(&rawObj); err != nil {
		return fmt.Errorf("failed to decode manifest: %w", err)
	}

	obj := &unstructured.Unstructured{Object: rawObj}

	if err := r.applyResource(ctx, obj); err != nil {
		return fmt.Errorf("failed to apply resource %s/%s: %w",
			obj.GetNamespace(), obj.GetName(), err)
	}

	return nil
}

func (r *ClusterAddonReconciler) applyResource(ctx context.Context, obj *unstructured.Unstructured) error {
	// Ensure namespace exists if specified
	if namespace := obj.GetNamespace(); namespace != "" {
		if err := r.CreateNamespaceIfNotExists(ctx, namespace); err != nil {
			return err
		}
	}

	// Try to create the resource
	err := retry.OnError(retry.DefaultRetry, func(err error) bool {
		return !errors.IsAlreadyExists(err)
	}, func() error {
		return r.Create(ctx, obj)
	})

	// If resource exists, update it
	if errors.IsAlreadyExists(err) {
		existing := &unstructured.Unstructured{}
		existing.SetGroupVersionKind(obj.GroupVersionKind())

		err = r.Get(ctx, client.ObjectKey{
			Namespace: obj.GetNamespace(),
			Name:      obj.GetName(),
		}, existing)
		if err != nil {
			return err
		}

		// Preserve resourceVersion for update
		obj.SetResourceVersion(existing.GetResourceVersion())

		return retry.OnError(retry.DefaultRetry, func(err error) bool {
			return !errors.IsConflict(err)
		}, func() error {
			return r.Update(ctx, obj)
		})
	}

	return err
}

func (r *ClusterAddonReconciler) HandleAddonDelete(ctx context.Context) error {
	cf, err := r.GetData(ctx)
	if err != nil {
		return err
	}

	for addonName := range cf.Data {
		manifest, ok := addonManifests[addonName]
		if !ok {
			continue
		}

		if err := r.deleteAddonResources(ctx, manifest.URL); err != nil {
			return fmt.Errorf("failed to delete addon %s: %w", addonName, err)
		}

		delete(cf.Data, addonName)
	}

	return r.UpdateData(ctx, cf)
}

func (r *ClusterAddonReconciler) deleteAddonResources(ctx context.Context, manifestURL string) error {
	resp, err := http.Get(manifestURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	decoder := yaml.NewYAMLOrJSONDecoder(resp.Body, 4096)
	for {
		var rawObj map[string]interface{}
		if err := decoder.Decode(&rawObj); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		obj := &unstructured.Unstructured{Object: rawObj}

		// Delete the resource
		err := r.Delete(ctx, obj)
		if err != nil && !errors.IsNotFound(err) {
			return err
		}
	}

	return nil
}

func (r *ClusterAddonReconciler) GetOrCreateConfigMap(ctx context.Context) (*corev1.ConfigMap, error) {
	cf := NewAddonData()
	err := r.Get(ctx, client.ObjectKey{Namespace: cf.Namespace, Name: cf.Name}, cf)
	if errors.IsNotFound(err) {
		err = r.Create(ctx, cf)
	}
	if err != nil {
		return nil, err
	}

	if cf.Data == nil {
		cf.Data = make(map[string]string)
	}

	return cf, nil
}

func (r *ClusterAddonReconciler) updateAddonStatus(ctx context.Context, cf *corev1.ConfigMap, addonName string) error {
	return retry.OnError(retry.DefaultRetry, errors.IsConflict, func() error {
		cf.Data[addonName] = fmt.Sprintf("installed@%s", time.Now().Format(time.RFC3339))
		return r.Update(ctx, cf)
	})
}

// Helper function to validate manifest URLs
func validateManifestURL(url string) error {
	if !strings.HasPrefix(url, "https://") {
		return fmt.Errorf("manifest URL must use HTTPS")
	}
	return nil
}
