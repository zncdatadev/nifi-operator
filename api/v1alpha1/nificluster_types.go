/*
Copyright 2025 ZNCDataDev.

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

package v1alpha1

import (
	commonsv1alpha1 "github.com/zncdatadev/operator-go/pkg/apis/commons/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NifiClusterSpec defines the desired state of NifiCluster.
type NifiClusterSpec struct {
	// +kubebuilder:validation:Required
	ClusterConfig *ClusterConfigSpec `json:"clusterConfig"`

	// +kubebuilder:validation:Optional
	ClusterOperation *commonsv1alpha1.ClusterOperationSpec `json:"clusterOperation,omitempty"`

	// +kubebuilder:validation:Optional
	// +default:value={"repo": "quay.io/zncdatadev", "pullPolicy": "IfNotPresent"}
	Image *ImageSpec `json:"image,omitempty"`

	// +kubebuilder:validation:Required
	Nodes *NodesSpec `json:"nodes"`
}

// NodesSpec defines the nodes spec.
type NodesSpec struct {
	RoleGroups map[string]RoleGroupSpec `json:"roleGroups,omitempty"`

	// +kubebuilder:validation:Optional
	RoleConfig *commonsv1alpha1.RoleConfigSpec `json:"roleConfig,omitempty"`

	// +kubebuilder:validation:Optional
	Config *ConfigSpec `json:"config,omitempty"`

	*commonsv1alpha1.OverridesSpec `json:",inline"`

	JVMArgumentOverrides *JVMArgumentOverridesSpec `json:"jvmArgumentOverrides,omitempty"`
}

// RoleGroupSpec defines the role group spec.
type RoleGroupSpec struct {
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=1
	Replicas *int32 `json:"replicas"`
	// +kubebuilder:validation:Optional
	// +default:value={"gracefulShutdownTimeout": "30s"}
	Config                         *ConfigSpec `json:"config,omitempty"`
	*commonsv1alpha1.OverridesSpec `json:",inline"`
	JVMArgumentOverrides           *JVMArgumentOverridesSpec `json:"jvmArgumentOverrides,omitempty"`
}

// ConfigSpec defines the config spec.
type ConfigSpec struct {
	*commonsv1alpha1.RoleGroupConfigSpec `json:",inline"`
}

type JVMArgumentOverridesSpec struct {
	Add         []string `json:"add,omitempty"`
	Remove      []string `json:"remove,omitempty"`
	RemoveRegex []string `json:"removeRegex,omitempty"`
}

// NifiClusterStatus defines the observed state of NifiCluster.
type NifiClusterStatus struct {
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// NifiCluster is the Schema for the nificlusters API.
type NifiCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NifiClusterSpec   `json:"spec,omitempty"`
	Status NifiClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NifiClusterList contains a list of NifiCluster.
type NifiClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NifiCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NifiCluster{}, &NifiClusterList{})
}
