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
	infrav1 "github.com/kubernetes-sigs/cluster-api-provider-kubemark/api/v1alpha4"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

const (
	// ClusterFinalizer allows KubemarkCluster reconciler to clean up resources associated with KubemarkCluster before
	// removing it from the API server.
	ClusterFinalizer = "kubemarkcluster.infrastructure.cluster.x-k8s.io"
)

// KubemarkClusterSpec define spec for a KubemarkCluster.
type KubemarkClusterSpec struct {
	// ControlPlaneEndpoint represents the endpoint used to communicate with the control plane.
	// Note: it is not supported providing either controlPlaneEndpoint host or port.
	// TODO: support users providing controlPlaneEndpoint host or port.
	// +optional
	ControlPlaneEndpoint APIEndpoint `json:"controlPlaneEndpoint,omitempty"`

	// BackingCluster defined the cluster where to host pods running kubemark.
	// If empty, pods running kubemark will be created in the management cluster, in the same
	// namespace of the cluster resource.
	// TODO: support service type Loadbalancer, adding an ingress in front of the service, (TBD if one or both, but it is required a solution for making remote backing cluster accessible from the management cluster)
	// +optional
	BackingCluster *infrav1.BackingClusterSpec `json:"backingCluster,omitempty"`
}

// KubemarkClusterStatus define status for a KubemarkCluster.
type KubemarkClusterStatus struct {
	// Ready denotes that the docker cluster (infrastructure) is ready.
	// +optional
	Ready bool `json:"ready"`

	// Conditions defines current service state of the KubeadmCluster.
	// +optional
	Conditions clusterv1.Conditions `json:"conditions,omitempty"`
}

// APIEndpoint represents a reachable Kubernetes API endpoint.
type APIEndpoint struct {
	// Host is the hostname on which the API server is serving.
	Host string `json:"host,omitempty"`

	// Port is the port on which the API server is serving.
	Port int `json:"port,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=kubemarkclusters,shortName=kmc,scope=Namespaced,categories=cluster-api
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="Cluster",type="string",JSONPath=".metadata.labels['cluster\\.x-k8s\\.io/cluster-name']",description="Cluster"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.ready",description="KubemarkCluster ready status"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="Time duration since creation of KubemarkCluster"

// KubemarkCluster define a minimal cluster infrastructure specifically designed to be used
// for Cluster API scale tests, autoscaler tests or development.
// Do not use it in production.
type KubemarkCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KubemarkClusterSpec   `json:"spec,omitempty"`
	Status KubemarkClusterStatus `json:"status,omitempty"`
}

func (in *KubemarkCluster) GetBackingCluster() *infrav1.BackingClusterSpec {
	return in.Spec.BackingCluster
}

func (in *KubemarkCluster) GetConditions() clusterv1.Conditions {
	return in.Status.Conditions
}

func (in *KubemarkCluster) SetConditions(conditions clusterv1.Conditions) {
	in.Status.Conditions = conditions
}

// +kubebuilder:object:root=true

// KubemarkClusterList contains a list of KubemarkCluster
type KubemarkClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KubemarkCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&KubemarkCluster{}, &KubemarkClusterList{})
}
