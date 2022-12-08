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

// KubemarkClusterTemplateSpec define spec for a KubemarkClusterTemplate.
type KubemarkClusterTemplateSpec struct {
	Template KubemarkClusterTemplateResource `json:"template"`
}

// KubemarkClusterTemplateResource defines a template resource for a KubemarkClusterTemplate.
type KubemarkClusterTemplateResource struct {
	// Standard object's metadata.
	// More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#metadata
	// +optional
	ObjectMeta clusterv1.ObjectMeta `json:"metadata,omitempty"`
	Spec       KubemarkClusterSpec  `json:"spec"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=kubemarkclustertemplates,shortName=kmct,scope=Namespaced,categories=cluster-api
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="Time duration since creation of DockerClusterTemplate"

// KubemarkClusterTemplate define a template for KubemarkCluster.
type KubemarkClusterTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec KubemarkClusterTemplateSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// KubemarkClusterTemplateList contains a list of KubemarkClusterTemplate
type KubemarkClusterTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []KubemarkClusterTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&KubemarkClusterTemplate{}, &KubemarkClusterTemplateList{})
}
