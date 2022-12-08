/*
Copyright 2022 The Kubernetes Authors.

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

package v1alpha4

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

const (
	// ControlPlaneFinalizer allows the controller to clean up resources associated with KubemarkControlPlane before
	// removing it from the apiserver.
	ControlPlaneFinalizer = "kubemarkcontrolplane.infrastructure.cluster.x-k8s.io"
)

// KubemarkControlPlaneSpec define spec for a KubemarkControlPlane.
// TODO: Implement support for replicas
type KubemarkControlPlaneSpec struct {
	// Version defines the desired Kubernetes version.
	Version string `json:"version"`
}

// KubemarkControlPlaneStatus define status for a KubemarkControlPlane.
type KubemarkControlPlaneStatus struct {
	// Version represents the minimum Kubernetes version for the control plane machines in the cluster.
	// +optional
	Version *string `json:"version,omitempty"`

	// Initialized denotes whether the control plane has completed initialization such that at least once,
	// the target's control plane has been contactable.
	// +optional
	Initialized bool `json:"initialized"`

	// Ready denotes that the control plane is ready to receive requests.
	// +optional
	Ready bool `json:"ready"`

	// Conditions defines current service state of the KubeadmControlPlane.
	// +optional
	Conditions clusterv1.Conditions `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=kubemarkcontrolplanes,shortName=kmcp,scope=Namespaced,categories=cluster-api
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="Cluster",type="string",JSONPath=".metadata.labels['cluster\\.x-k8s\\.io/cluster-name']",description="Cluster"
// +kubebuilder:printcolumn:name="Initialized",type=boolean,JSONPath=".status.initialized",description="Initialized denotes whether the control plane has completed initialization such that at least once, the target's control plane has been contactable"
// +kubebuilder:printcolumn:name="API Server Available",type=boolean,JSONPath=".status.ready",description="Ready denotes that the control plane is ready to receive requests"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="Time duration since creation of KubeadmControlPlane"
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=".spec.version",description="Kubernetes version associated with this control plane"

// KubemarkControlPlane defines a minimal control plane specifically designed to be used
// for Cluster API scale tests, Cluster API autoscaler tests or Cluster API development.
// Do not use it in production.
type KubemarkControlPlane struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KubemarkControlPlaneSpec   `json:"spec,omitempty"`
	Status KubemarkControlPlaneStatus `json:"status,omitempty"`
}

func (c *KubemarkControlPlane) GetConditions() clusterv1.Conditions {
	return c.Status.Conditions
}

func (c *KubemarkControlPlane) SetConditions(conditions clusterv1.Conditions) {
	c.Status.Conditions = conditions
}

// +kubebuilder:object:root=true

// KubemarkControlPlaneList contains a list of KubemarkControlPlane
type KubemarkControlPlaneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KubemarkControlPlane `json:"items"`
}

func init() {
	SchemeBuilder.Register(&KubemarkControlPlane{}, &KubemarkControlPlaneList{})
}
