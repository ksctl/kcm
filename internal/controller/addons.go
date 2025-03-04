package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gookit/goutil/dump"
	"github.com/ksctl/ksctl/v2/pkg/poller"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type AddonURL func(version string) string

type AddonManifest struct {
	Org       string
	Repo      string
	URL       AddonURL
	Namespace *string
}

var addonManifests = map[string]AddonManifest{
	"stack": {
		Org:  "ksctl",
		Repo: "ka",
		URL: func(version string) string {
			return fmt.Sprintf("https://github.com/ksctl/ka/releases/download/%s/install.yaml", version)
		},
	},
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

func (r *ClusterAddonReconciler) DeleteNamespaceIfExists(ctx context.Context, namespace string) error {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
		},
	}
	if err := r.Get(ctx, client.ObjectKey{Name: namespace}, ns); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}
	return r.Delete(ctx, ns)
}

func (r *ClusterAddonReconciler) GetData(ctx context.Context) (*corev1.ConfigMap, error) {
	cf := &corev1.ConfigMap{}

	if err := r.Get(ctx, client.ObjectKey{Namespace: "kcm-system", Name: "kcm-addons"}, cf); err != nil {
		if errors.IsNotFound(err) {
			cf = &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kcm-addons",
					Namespace: "kcm-system",
				},
				Data: map[string]string{},
			}
			if err := r.Create(ctx, cf); err != nil {
				return nil, err
			}
			return cf, nil
		}
		return nil, err
	}
	if cf.Data == nil {
		cf.Data = map[string]string{}
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

func (r *ClusterAddonReconciler) HandleAddon(ctx context.Context, addonName string, addonVer *string) error {
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

	if manifest.Namespace != nil {
		if err := r.CreateNamespaceIfNotExists(ctx, *manifest.Namespace); err != nil {
			return fmt.Errorf("failed to create namespace for ADDON %s: %w", *manifest.Namespace, err)
		}
	}
	addonVersion := ""

	if addonVer == nil {
		v, err := poller.GetSharedPoller().Get(manifest.Org, manifest.Repo)
		if err == nil {
			addonVersion = v[0]
		}
	} else {
		addonVersion = *addonVer
	}

	if err := r.downloadAndOperateManifests(ctx, manifest, r.applyResource, addonVersion); err != nil {
		return fmt.Errorf("failed to install addon %s: %w", addonName, err)
	}

	return r.updateAddonStatus(ctx, cf, addonName, false, addonVersion)
}

func (r *ClusterAddonReconciler) HandleAddonDelete(ctx context.Context, addonName string, addonVer *string) error {
	cf, err := r.GetData(ctx)
	if err != nil {
		return fmt.Errorf("failed to get/create config map: %w", err)
	}

	if _, installed := cf.Data[addonName]; !installed {
		return nil
	}

	manifest, ok := addonManifests[addonName]
	if !ok {
		return fmt.Errorf("addon %s not found in manifest registry", addonName)
	}

	addonVersion := ""

	if addonVer == nil {
		v := cf.Data[addonName]
		_v := AddonState{}
		_ = json.Unmarshal([]byte(v), &_v)
		addonVersion = _v.Ver
	} else {
		addonVersion = *addonVer
	}

	if err := r.downloadAndOperateManifests(ctx, manifest, r.deleteResource, addonVersion); err != nil {
		return fmt.Errorf("failed to uninstall addon %s: %w", addonName, err)
	}

	if manifest.Namespace != nil {
		if err := r.DeleteNamespaceIfExists(ctx, *manifest.Namespace); err != nil {
			return fmt.Errorf("failed to delete namespace for ADDON %s: %w", *manifest.Namespace, err)
		}
	}

	return r.updateAddonStatus(ctx, cf, addonName, true, addonVersion)
}

func (r *ClusterAddonReconciler) downloadAndOperateManifests(
	ctx context.Context,
	manifest AddonManifest,
	operator func(ctx context.Context, obj *unstructured.Unstructured) error,
	version string,
) error {
	resp, err := http.Get(manifest.URL(version))
	if err != nil {
		return fmt.Errorf("failed to download manifest: %w", err)
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)

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

		if err := operator(ctx, obj); err != nil {
			return fmt.Errorf("failed to apply resource %s/%s: %w",
				obj.GetNamespace(), obj.GetName(), err)
		}
	}

	return nil
}

func (r *ClusterAddonReconciler) deleteResource(ctx context.Context, obj *unstructured.Unstructured) error {
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
	opts := metav1.DeleteOptions{}

	err = dr.Delete(ctx, obj.GetName(), opts)
	if err != nil {
		fmt.Println("Failed to delete resource", obj.GetNamespace(), obj.GetName())
		dump.Println(obj)
		return fmt.Errorf("failed to delete resource: %w", err)
	}

	fmt.Println("Delete resource", obj.GetNamespace(), obj.GetName())

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
		fmt.Println("Failed to apply resource", obj.GetNamespace(), obj.GetName())
		dump.Println(obj)
		return fmt.Errorf("failed to apply resource: %w", err)
	}

	fmt.Println("Applied resource", obj.GetNamespace(), obj.GetName())

	return nil
}

func (r *ClusterAddonReconciler) updateAddonStatus(ctx context.Context, cf *corev1.ConfigMap, addonName string, isDelete bool, addonVer string) error {
	return retry.OnError(retry.DefaultRetry, errors.IsConflict, func() error {
		if isDelete {
			delete(cf.Data, addonName)
		} else {
			if cf.Data == nil {
				cf.Data = map[string]string{}
			}
			v, _ := json.Marshal(AddonState{Ver: addonVer, Timestamp: time.Now().UTC()})
			cf.Data[addonName] = string(v)
		}
		return r.Update(ctx, cf)
	})
}

type AddonState struct {
	Ver       string    `json:"version"`
	Timestamp time.Time `json:"timestamp"`
}
