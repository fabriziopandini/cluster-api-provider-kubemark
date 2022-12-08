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

// KubemarkControlPlaneTemplateSpec define spec for a KubemarkControlPlaneTemplate.
type KubemarkControlPlaneTemplateSpec struct {
	Template KubemarkControlPlaneTemplateResource `json:"template"`
}

// KubemarkControlPlaneTemplateResource defines a template resource for a KubemarkControlPlaneTemplate.
type KubemarkControlPlaneTemplateResource struct {
	// Standard object's metadata.
	// More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#metadata
	// +optional
	ObjectMeta clusterv1.ObjectMeta     `json:"metadata,omitempty"`
	Spec       KubemarkControlPlaneSpec `json:"spec"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=kubemarkcontrolplanetemplates,shortName=kmcpt,scope=Namespaced,categories=cluster-api
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="Time duration since creation of DockerClusterTemplate"

// KubemarkControlPlaneTemplate define a template for KubemarkCluster.
type KubemarkControlPlaneTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec KubemarkControlPlaneTemplateSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// KubemarkControlPlaneTemplateList contains a list of KubemarkControlPlaneTemplate
type KubemarkControlPlaneTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KubemarkControlPlaneTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&KubemarkControlPlaneTemplate{}, &KubemarkControlPlaneTemplateList{})
}
