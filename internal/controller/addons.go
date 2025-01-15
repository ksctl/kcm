package controller

import (
	"context"
	"fmt"
	"github.com/gookit/goutil/dump"
	"io"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/retry"
	"net/http"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"time"
)

type AddonManifest struct {
	URL       string
	Namespace *string
}

var addonManifests = map[string]AddonManifest{
	"stack": {
		URL: "https://raw.githubusercontent.com/argoproj/argo-cd/stable/manifests/install.yaml",
		Namespace: func() *string {
			x := "argocd"
			return &x
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

func (r *ClusterAddonReconciler) GetData(ctx context.Context) (*corev1.ConfigMap, error) {
	cf := NewAddonData()
	err := r.Get(ctx, client.ObjectKey{Namespace: cf.Namespace, Name: cf.Name}, cf)
	if err != nil {
		if errors.IsNotFound(err) {
			err = r.Create(ctx, cf)
		}
		return nil, err
	}

	if cf.Data == nil {
		cf.Data = make(map[string]string)
	}
	return cf, nil
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

	cf, err := r.GetData(ctx)
	if err != nil {
		return fmt.Errorf("failed to get/create config map: %w", err)
	}

	if _, installed := cf.Data[addonName]; installed {
		return nil
	}

	fmt.Println("##########", cf.Data, addonName, manifest)

	if manifest.Namespace != nil {
		if err := r.CreateNamespaceIfNotExists(ctx, *manifest.Namespace); err != nil {
			return fmt.Errorf("failed to create namespace for ADDON %s: %w", *manifest.Namespace, err)
		}
	}

	if err := r.downloadAndApplyManifests(ctx, manifest); err != nil {
		return fmt.Errorf("failed to install addon %s: %w", addonName, err)
	}

	return r.updateAddonStatus(ctx, cf, addonName)
}

func (r *ClusterAddonReconciler) HandleAddonDelete(ctx context.Context) error {
	return nil
}

func (r *ClusterAddonReconciler) downloadAndApplyManifests(ctx context.Context, manifest AddonManifest) error {
	resp, err := http.Get(manifest.URL)
	if err != nil {
		return fmt.Errorf("failed to download manifest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download manifest, status: %d", resp.StatusCode)
	}

	decoder := yaml.NewYAMLOrJSONDecoder(resp.Body, 4096)
	for {
		var rawObj map[string]interface{}
		if err := decoder.Decode(&rawObj); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("failed to decode manifest: %w", err)
		}

		if len(rawObj) == 0 {
			continue // Skip empty documents
		}

		obj := &unstructured.Unstructured{Object: rawObj}

		// Set namespace for namespaced resources if not specified
		if obj.GetNamespace() == "" && manifest.Namespace != nil {
			obj.SetNamespace(*manifest.Namespace)
		}

		// Validate required fields
		if obj.GetAPIVersion() == "" || obj.GetKind() == "" {
			return fmt.Errorf("manifest missing apiVersion or kind")
		}

		if err := r.applyResource(ctx, obj); err != nil {
			return fmt.Errorf("failed to apply resource %s/%s: %w",
				obj.GetNamespace(), obj.GetName(), err)
		}
	}

	return nil
}

func (r *ClusterAddonReconciler) applyResource(ctx context.Context, obj *unstructured.Unstructured) error {
	// Get the GVK for the resource
	gvk := obj.GroupVersionKind()

	// Get the corresponding REST mapping
	mapping, err := r.RESTMapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return fmt.Errorf("failed to get REST mapping: %w", err)
	}

	// Create dynamic resource interface
	var dr dynamic.ResourceInterface
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		// Namespaced resources
		dr = r.DynamicClient.Resource(mapping.Resource).Namespace(obj.GetNamespace())
	} else {
		// Cluster-scoped resources
		dr = r.DynamicClient.Resource(mapping.Resource)
	}

	// Apply the resource using server-side apply
	opts := metav1.ApplyOptions{
		FieldManager: "cluster-addon-controller",
		Force:        true,
	}

	_, err = dr.Apply(ctx, obj.GetName(), obj, opts)
	if err != nil {
		fmt.Println("########### Failed to apply resource", obj.GetNamespace(), obj.GetName())
		dump.Println(obj)
		return fmt.Errorf("failed to apply resource: %w", err)
	}

	fmt.Println("########### Applied resource", obj.GetNamespace(), obj.GetName())

	return nil
}

func (r *ClusterAddonReconciler) updateAddonStatus(ctx context.Context, cf *corev1.ConfigMap, addonName string) error {
	return retry.OnError(retry.DefaultRetry, errors.IsConflict, func() error {
		cf.Data[addonName] = fmt.Sprintf("installed@%s", time.Now().Format(time.RFC3339))
		return r.Update(ctx, cf)
	})
}
