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

package controllers

import (
	"context"
	"testing"

	infrav1 "github.com/kubernetes-sigs/cluster-api-provider-kubemark/api/v1alpha4"
	"github.com/kubernetes-sigs/cluster-api-provider-kubemark/controllers"
	expinfrav1 "github.com/kubernetes-sigs/cluster-api-provider-kubemark/exp/cluster/api/v1alpha4"
	expcontrolplanev1 "github.com/kubernetes-sigs/cluster-api-provider-kubemark/exp/controlplane/api/v1alpha4"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var (
	testScheme = runtime.NewScheme()
	ctx        = ctrl.SetupSignalHandler()
)

func init() {
	_ = corev1.AddToScheme(testScheme)
	_ = infrav1.AddToScheme(testScheme)
}

func TestCASecretsHandler(t *testing.T) {
	g := NewWithT(t)
	s := controlPlaneScope{
		client: fake.NewClientBuilder().WithScheme(testScheme).Build(),
		backingCluster: &controllers.BackingCluster{
			Client:    fake.NewClientBuilder().WithScheme(testScheme).Build(),
			Namespace: "my-backing-cluster-namespace",
		},
		cluster: &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "my-cluster-namespace", // this must be used as a namespace for secrets.
				Name:      "my-cluster",           // this is used as a prefix for secrets name.
			},
		},
		kubemarkCluster: &expinfrav1.KubemarkCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "my-cluster-namespace",
				Name:      "my-kubemark-cluster",
			},
		},
		kubemarkControlPlane: &expcontrolplanev1.KubemarkControlPlane{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "my-cluster-namespace",
				Name:      "my-kubemark-control-plane",
			},
		},
	}
	ca := s.caSecretHandler()

	err := ca.LookupOrGenerate(ctx)
	g.Expect(err).ToNot(HaveOccurred())

	// Test if secrets with the generated certificates exists.
	// NOTE: it is not required to validated the secret content given that the implementation
	// relies on Cluster API utils.

	ca1 := &corev1.Secret{}
	key1 := client.ObjectKey{Namespace: "my-cluster-namespace", Name: "my-cluster-ca"}
	g.Expect(s.client.Get(ctx, key1, ca1)).To(Succeed())

	ca2 := &corev1.Secret{}
	key2 := client.ObjectKey{Namespace: "my-cluster-namespace", Name: "my-cluster-sa"}
	g.Expect(s.client.Get(ctx, key2, ca2)).To(Succeed())

	ca3 := &corev1.Secret{}
	key3 := client.ObjectKey{Namespace: "my-cluster-namespace", Name: "my-cluster-proxy"}
	g.Expect(s.client.Get(ctx, key3, ca3)).To(Succeed())

	ca4 := &corev1.Secret{}
	key4 := client.ObjectKey{Namespace: "my-cluster-namespace", Name: "my-cluster-etcd"}
	g.Expect(s.client.Get(ctx, key4, ca4)).To(Succeed())
}

func TestKubeConfigSecretHandler(t *testing.T) {
	g := NewWithT(t)
	s := controlPlaneScope{
		client: fake.NewClientBuilder().WithScheme(testScheme).Build(),
		backingCluster: &controllers.BackingCluster{
			Client:    fake.NewClientBuilder().WithScheme(testScheme).Build(),
			Namespace: "my-backing-cluster-namespace",
		},
		cluster: &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "my-cluster-namespace", // this must be used as a namespace for secrets.
				Name:      "my-cluster",           // this is used as a prefix for secrets name.
			},
			Spec: clusterv1.ClusterSpec{
				ControlPlaneEndpoint: clusterv1.APIEndpoint{ // this must be set before secrets are generated.
					Host: "1.2.3.4",
					Port: 6443,
				},
			},
		},
		kubemarkCluster: &expinfrav1.KubemarkCluster{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "my-cluster-namespace",
				Name:      "my-kubemark-cluster",
			},
		},
		kubemarkControlPlane: &expcontrolplanev1.KubemarkControlPlane{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "my-cluster-namespace",
				Name:      "my-kubemark-control-plane",
			},
		},
	}
	g.Expect(s.caSecretHandler().LookupOrGenerate(ctx)).ToNot(HaveOccurred())

	k := s.kubeConfigSecretHandler()
	err := k.LookupOrGenerate(ctx)
	g.Expect(err).ToNot(HaveOccurred())

	// Test if a secret with the generated kubeconfig exists.
	// NOTE: it is not required to validated the secret content given that the implementation
	// relies on Cluster API utils.
	k1 := &corev1.Secret{}
	key1 := client.ObjectKey{Namespace: "my-cluster-namespace", Name: "my-cluster-kubeconfig"}
	g.Expect(s.client.Get(context.TODO(), key1, k1)).To(Succeed())
}
