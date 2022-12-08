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

import corev1 "k8s.io/api/core/v1"

// BackingClusterSpec defines spec for a backing cluster.
type BackingClusterSpec struct {
	// SecretRef is the reference to a secret with a `kubeconfig` value
	// providing the kubeconfig for accessing to the BackingCluster.
	// TODO: document RBAC rules required to work in the BackingCluster.
	// TODO: consider if to rename KubeConfigSecretRef
	// TODO: validation (mandatory if BackingClusterSpec is defined; cannot be changed if the KubemarkCluster is ready)
	SecretRef corev1.LocalObjectReference `json:"secretRef,omitempty"`

	// Namespace defines namespace where to host pods running kubemark. If empty
	// the namespace of the kubeconfig will be used as a default.
	// Note: it is not supported using the same backing namespace for two clusters with the same name.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}
