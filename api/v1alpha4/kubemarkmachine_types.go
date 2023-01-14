/*
Copyright 2020 The Kubernetes Authors.

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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

// KubemarkExtendedResourceName defines names for KubemarkExtendedResourceList.
type KubemarkExtendedResourceName string

// KubemarkExtendedResourceList defines a list of resources to be used in KubemarkProcessOptions.
type KubemarkExtendedResourceList map[KubemarkExtendedResourceName]resource.Quantity

const (
	// MachineFinalizer allows the controller to clean up resources associated with KubemarkMachine before
	// removing it from the apiserver.
	MachineFinalizer = "kubemarkmachine.infrastructure.cluster.x-k8s.io"

	// KubemarkExtendedResourceCPU defines ExtendedResource type CPU for KubemarkMachines.
	KubemarkExtendedResourceCPU KubemarkExtendedResourceName = "cpu"

	// KubemarkExtendedResourceMemory defines ExtendedResource type memory for KubemarkMachines.
	KubemarkExtendedResourceMemory KubemarkExtendedResourceName = "memory"
)

// KubemarkMachineSpec defines the desired state of KubemarkMachine.
type KubemarkMachineSpec struct {
	// ProviderID will be the kubemark pod name in ProviderID format (kubemark:////<podname>)
	// +optional
	ProviderID *string `json:"providerID,omitempty"`

	// ExtraMounts describes additional mount points for the node container
	// These may be used to bind a hostPath
	// +optional
	ExtraMounts []Mount `json:"extraMounts,omitempty"`

	// KubemarkOptions are API representations of command line flags that
	// will be passed to the `kubemark` binary.
	// +optional
	KubemarkOptions KubemarkProcessOptions `json:"kubemarkOptions,omitempty"`

	// KubemarkHollowPodClusterSecretRef is a reference to a secret with a kubeconfig for an external cluster used for kubemark pods.
	// Deprecated: use BackingCluster instead; if both are defined, BackingCluster takes the precedence.
	KubemarkHollowPodClusterSecretRef *corev1.ObjectReference `json:"kubemarkHollowPodClusterSecretRef,omitempty"`

	// ComputeCluster defines the cluster where to host the pod running kubemark.
	// If empty, pods running kubemark will be created in the current management cluster.
	// +optional
	ComputeCluster *ComputeClusterSpec `json:"computeCluster,omitempty"`
}

// Mount specifies a host volume to mount into a container.
// This is a simplified version of kind v1alpha4.Mount types.
type Mount struct {
	// Name of the mount.
	Name string `json:"name"`

	// Path of the mount within the container.
	ContainerPath string `json:"containerPath"`

	// Path of the mount on the host. If the hostPath doesn't exist, then runtimes
	// should report error. If the hostpath is a symbolic link, runtimes should
	// follow the symlink and mount the real destination to container.
	HostPath string `json:"hostPath"`

	// Type for HostPath Volume
	// Defaults to ""
	// More info: https://kubernetes.io/docs/concepts/storage/volumes#hostpath
	// validations taken from https://github.com/kubernetes/api/blob/master/core/v1/types.go#L664
	// +kubebuilder:validation:Enum:="";"DirectoryOrCreate";"Directory";"FileOrCreate";"File";"Socket";"CharDevice";"BlockDevice"
	// +optional
	Type *corev1.HostPathType `json:"type,omitempty"`
}

// KubemarkProcessOptions contain fields that are converted to command line flags
// when running the kubemark container for a hollow node.
type KubemarkProcessOptions struct {
	// ExtendedResources is a map of resource-type:value pairs that describe
	// resources which the result machine and node will advertise as capacity.
	// These will only be used with Kubernetes v1.22+.
	// Defaults to {"cpu": "1", "memory": "4G"}
	ExtendedResources KubemarkExtendedResourceList `json:"extendedResources,omitempty"`
}

// KubemarkMachineStatus defines the observed state of KubemarkMachine.
type KubemarkMachineStatus struct {
	// Ready is true when the provider resource is ready.
	// +optional
	Ready bool `json:"ready"`

	// Conditions defines current service state of the DockerMachine.
	// +optional
	Conditions clusterv1.Conditions `json:"conditions,omitempty"`
}

// +kubebuilder:subresource:status
// +kubebuilder:object:root=true
// +kubebuilder:printcolumn:name="Cluster",type="string",JSONPath=".metadata.labels['cluster\\.x-k8s\\.io/cluster-name']",description="Cluster"
// +kubebuilder:printcolumn:name="Machine",type="string",JSONPath=".metadata.ownerReferences[?(@.kind==\"Machine\")].name",description="Machine object which owns with this KubemarkMachine"
// +kubebuilder:printcolumn:name="ProviderID",type="string",JSONPath=".spec.providerID",description="Provider ID"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.ready",description="Machine ready status"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="Time duration since creation of DockerMachine"

// KubemarkMachine is the Schema for the kubemarkmachines API.
type KubemarkMachine struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KubemarkMachineSpec   `json:"spec,omitempty"`
	Status KubemarkMachineStatus `json:"status,omitempty"`
}

// GetConditions returns the set of conditions for this object.
func (m *KubemarkMachine) GetConditions() clusterv1.Conditions {
	return m.Status.Conditions
}

// SetConditions sets the conditions on this object.
func (m *KubemarkMachine) SetConditions(conditions clusterv1.Conditions) {
	m.Status.Conditions = conditions
}

// GetComputeCluster returns the compute cluster for this object.
func (m *KubemarkMachine) GetComputeCluster() *ComputeClusterSpec {
	if m.Spec.ComputeCluster != nil {
		return m.Spec.ComputeCluster
	}
	if m.Spec.KubemarkHollowPodClusterSecretRef != nil {
		return &ComputeClusterSpec{KubeConfigSecretRef: corev1.LocalObjectReference{Name: m.Spec.KubemarkHollowPodClusterSecretRef.Name}}
	}
	return nil
}

// +kubebuilder:object:root=true

// KubemarkMachineList contains a list of KubemarkMachine.
type KubemarkMachineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KubemarkMachine `json:"items"`
}

func init() {
	SchemeBuilder.Register(&KubemarkMachine{}, &KubemarkMachineList{})
}
