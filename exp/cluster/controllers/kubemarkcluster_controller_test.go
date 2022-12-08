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
	"fmt"
	"testing"

	infrav1 "github.com/kubernetes-sigs/cluster-api-provider-kubemark/api/v1alpha4"
	"github.com/kubernetes-sigs/cluster-api-provider-kubemark/controllers"
	expinfrav1 "github.com/kubernetes-sigs/cluster-api-provider-kubemark/exp/cluster/api/v1alpha4"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
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

func TestLBServiceHandler(t *testing.T) {
	t.Run("Test Generate, Lookup, Delete", func(t *testing.T) {
		g := NewWithT(t)

		s := clusterScope{
			backingCluster: &controllers.BackingCluster{
				Client:    fake.NewClientBuilder().WithScheme(testScheme).Build(),
				Namespace: "my-backing-cluster-namespace",
			},
			cluster: &clusterv1.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "my-cluster-namespace",
					Name:      "my-cluster",
				},
			},
			kubemarkCluster: &expinfrav1.KubemarkCluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "my-cluster-namespace",
					Name:      "my-kubemark-cluster",
				},
			},
		}
		lb := s.LoadBalancerServiceHandler()

		// Generate
		svc1, err := lb.Generate(ctx)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(svc1).ToNot(BeNil())

		g.Expect(svc1.Name).To(Equal(fmt.Sprintf("%s-lb", s.cluster.Name)))
		g.Expect(svc1.Namespace).To(Equal(s.backingCluster.Namespace))
		g.Expect(svc1.OwnerReferences).To(HaveLen(1))
		g.Expect(svc1.Spec.Type).To(Equal(corev1.ServiceTypeClusterIP))
		g.Expect(svc1.Spec.Ports).To(ConsistOf(corev1.ServicePort{Port: lbServicePort, TargetPort: intstr.FromInt(apiServerPodPort)}))

		// Fake ClusterIP address being assigned
		patch := client.MergeFrom(svc1.DeepCopy())
		svc1.Spec.ClusterIP = "1.2.3.4"
		g.Expect(s.backingCluster.Client.Patch(ctx, svc1, patch)).To(Succeed())

		// Lookup
		svc2, err := lb.Lookup(ctx)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(svc2).ToNot(BeNil())

		g.Expect(svc1.Spec.ClusterIP).To(Equal("1.2.3.4"))

		// Delete
		err = lb.Delete(ctx)
		g.Expect(err).ToNot(HaveOccurred())

		svc3 := &corev1.Service{}
		err = s.backingCluster.Client.Get(ctx, lb.ObjectKey(), svc3)
		g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})
	t.Run("Test LookupOrGenerate", func(t *testing.T) {
		g := NewWithT(t)

		s := clusterScope{
			backingCluster: &controllers.BackingCluster{
				Client:    fake.NewClientBuilder().WithScheme(testScheme).Build(),
				Namespace: "my-backing-cluster-namespace",
			},
			cluster: &clusterv1.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "my-cluster-namespace",
					Name:      "my-cluster",
				},
			},
			kubemarkCluster: &expinfrav1.KubemarkCluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "my-cluster-namespace",
					Name:      "my-kubemark-cluster",
				},
			},
		}
		lb := s.LoadBalancerServiceHandler()

		// LookupOrGenerate must create if the service is not already there
		svc1, err := lb.LookupOrGenerate(ctx)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(svc1).ToNot(BeNil())

		g.Expect(svc1.Name).To(Equal(fmt.Sprintf("%s-lb", s.cluster.Name)))
		g.Expect(svc1.Namespace).To(Equal(s.backingCluster.Namespace))
		g.Expect(svc1.OwnerReferences).To(HaveLen(1))
		g.Expect(svc1.Spec.Type).To(Equal(corev1.ServiceTypeClusterIP))
		g.Expect(svc1.Spec.Ports).To(ConsistOf(corev1.ServicePort{Port: lbServicePort, TargetPort: intstr.FromInt(apiServerPodPort)}))

		// Fake ClusterIP address being assigned
		patch := client.MergeFrom(svc1.DeepCopy())
		svc1.Spec.ClusterIP = "1.2.3.4"
		g.Expect(s.backingCluster.Client.Patch(ctx, svc1, patch)).To(Succeed())

		// LookupOrGenerate must read if the service already there
		svc2, err := lb.LookupOrGenerate(ctx)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(svc2).ToNot(BeNil())

		g.Expect(svc2.Spec.ClusterIP).To(Equal("1.2.3.4"))
	})
}
