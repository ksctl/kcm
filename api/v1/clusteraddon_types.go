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

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type AddonStatus string
type CAddonStatus string

const (
	AddonStatusEnabled  AddonStatus = "Enabled"
	AddonStatusDisabled AddonStatus = "Disabled"
	AddonStatusUnknown  AddonStatus = "Unknown"

	CAddonStatusEnabled  CAddonStatus = "Success"
	CAddonStatusDisabled CAddonStatus = "Failed"
)

type Addon struct {
	Name   string      `json:"name"`
	Status AddonStatus `json:"status"`
}

// ClusterAddonSpec defines the desired state of ClusterAddon.
type ClusterAddonSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	Addons []Addon `json:"addons"`
}

// ClusterAddonStatus defines the observed state of ClusterAddon.
type ClusterAddonStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	StatusCode      CAddonStatus `json:"statusCode,omitempty"`
	ReasonOfFailure string       `json:"reasonOfFailure,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// ClusterAddon is the Schema for the clusteraddons API.
type ClusterAddon struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterAddonSpec   `json:"spec,omitempty"`
	Status ClusterAddonStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ClusterAddonList contains a list of ClusterAddon.
type ClusterAddonList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterAddon `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterAddon{}, &ClusterAddonList{})
}
