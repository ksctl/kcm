package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type AddonSKU string

const (
	AddonSKUStack AddonSKU = "stack"
)

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

func (r *ClusterAddonReconciler) HandleAddonDelete(ctx context.Context) error {
	cf, err := r.GetData(ctx)
	if err != nil {
		return err
	}

	keys := make([]string, 0, len(cf.Data))
	for k := range cf.Data {
		keys = append(keys, k)
	}

	for _, addonName := range keys {

		fmt.Println("Addon uninstalling")
		if err := r.Delete(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "kcm-addon-" + addonName,
				Namespace: "ksctl-system",
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "kcm-addon-" + addonName,
						Image: "nginx:alpine",
					},
				},
			},
		}); err != nil {
			return err
		}

		delete(cf.Data, addonName)

		if err := r.UpdateData(ctx, cf); err != nil {
			return err
		}
	}

	return nil
}

func (r *ClusterAddonReconciler) HandleAddon(ctx context.Context, addonName string) error {
	if err := r.CreateNamespaceIfNotExists(ctx, "ksctl-system"); err != nil {
		return err
	}

	cf, err := r.GetData(ctx)
	if err != nil {
		return err
	}

	if cf.Data == nil {
		cf.Data = make(map[string]string)
	}

	if _, ok := cf.Data[addonName]; ok {
		fmt.Println("Addon already installed")
		return nil
	}

	if err := r.Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kcm-addon-" + addonName,
			Namespace: "ksctl-system",
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "kcm-addon-" + addonName,
					Image: "nginx:alpine",
				},
			},
		},
	}); err != nil {
		return err
	}

	cf.Data[addonName] = "installed@v1.0.0"
	if err := r.UpdateData(ctx, cf); err != nil {
		return err
	}

	return nil
}
