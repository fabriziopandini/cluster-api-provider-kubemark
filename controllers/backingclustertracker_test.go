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
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	infrav1 "github.com/kubernetes-sigs/cluster-api-provider-kubemark/api/v1alpha4"
	. "github.com/onsi/gomega"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/tools/record"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/config/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

var (
	testScheme = runtime.NewScheme()
	ctx        = ctrl.SetupSignalHandler()
)

func init() {
	_ = corev1.AddToScheme(testScheme)
	_ = infrav1.AddToScheme(testScheme)
}

func TestBackingClusterClient(t *testing.T) {
	// inject fake client creator.
	clientCreator = createFakeClient
	// inject fake cache creator.
	cacheCreator = createFakeCache
	// make the health check to run more frequently.
	healthCheckPollInterval = 100 * time.Millisecond
	// prevents health check to check API server liveness.
	unsafeSkipAPIServerCheck = true

	const (
		machinesNamespace = "my-namespace"
	)

	t.Run("Management cluster client should be used for a KubemarkMachine without BackingClusterSpec, same namespace of the KubemarkMachine", func(t *testing.T) {
		g := NewWithT(t)

		// KubemarkMachine without BackingClusterSpec defined.
		m := &infrav1.KubemarkMachine{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: machinesNamespace,
				Name:      "my-machine",
			},
		}

		// Get Backing cluster for the machine.
		mgr := newFakeManager(m)
		bct := NewBackingClusterTracker(mgr)
		bc, err := bct.GetFor(ctx, m)
		g.Expect(err).ToNot(HaveOccurred())

		// Management cluster client must be used.
		g.Expect(bc.Client).To(BeIdenticalTo(mgr.GetClient()))
		// Same namespace of the KubemarkMachine must be used.
		g.Expect(bc.Namespace).To(Equal(machinesNamespace))
	})

	t.Run("Management cluster client should be used for Machines without BackingClusterSpec, same namespace of the machine", func(t *testing.T) {
		g := NewWithT(t)

		const (
			// Number of KubemarkMachines
			numberOfMachines = 5000

			// Number of BackingClusters
			numberOfBackingClusters = 10

			// This simulates different controllers running in parallel and accessing the same machine/same backing clusters
			numberOfConcurrentAccess = 10
		)

		var (
			backingClusterName = func(i int) string { return fmt.Sprintf("my-machine-backing-cluster-secret-%d", i) }

			backingClusterNamespaceName = func(i int) string { return fmt.Sprintf("my-machine-backing-cluster-namespace-%d", i) }
		)
		objs := []client.Object{}
		machines := []*infrav1.KubemarkMachine{}
		backingClusters := sets.NewString()
		for i := 0; i < numberOfMachines; i++ {
			backingCluster := backingClusterName(i % numberOfBackingClusters) // machines are spread on backing clusters

			// KubemarkMachine with a BackingClusterSpec defined.
			m := &infrav1.KubemarkMachine{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: machinesNamespace,
					Name:      fmt.Sprintf("my-machine-%d", i),
				},
				Spec: infrav1.KubemarkMachineSpec{
					BackingCluster: &infrav1.BackingClusterSpec{
						SecretRef: corev1.LocalObjectReference{
							Name: backingCluster,
						},
						Namespace: backingClusterNamespaceName(i),
					},
				},
			}

			// Secret reference by the KubemarkMachine's BackingClusterSpec, with a kubeconfig pointing to the BackingCluster.
			if !backingClusters.Has(backingCluster) {
				config := &clientcmdapi.Config{
					Kind:       "Config",
					APIVersion: clientcmdapi.SchemeGroupVersion.String(),
					Clusters: map[string]*clientcmdapi.Cluster{
						"test-cluster": {
							Server: "http://localhost:6443",
						},
					},
					Contexts: map[string]*clientcmdapi.Context{
						"test-context": {
							Cluster:   "test-cluster",
							Namespace: "test-namespace",
						},
					},
					CurrentContext: "test-context",
				}
				configData, err := clientcmd.Write(*config)
				g.Expect(err).NotTo(HaveOccurred())

				s := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      backingCluster,
						Namespace: machinesNamespace, // secret must be in the same namespace of the
					},
					Data: map[string][]byte{
						backingClusterSecretKubeConfigKey: configData,
					},
					Type: clusterv1.ClusterSecretType,
				}
				objs = append(objs, s)
				backingClusters.Insert(backingCluster)
			}

			objs = append(objs, m)
			machines = append(machines, m)
		}

		// Get Backing cluster for the machines.
		// NOTE: we are simulating controller running in parallel accessing the same backing cluster;
		// backing cluster is intentionally slowed down by clientCreateDelay, to test try lock works properly.
		mgr := newFakeManager(objs...)
		bct := NewBackingClusterTracker(mgr)

		wg := sync.WaitGroup{}
		for i, m := range machines {
			m := m
			for r := 0; r < numberOfConcurrentAccess; r++ {
				wg.Add(1)
				go func(i int) {
					g.Eventually(func() error {
						bc, err := bct.GetFor(ctx, m)
						if err != nil {
							if !errors.Is(err, ErrClusterLocked) {
								panic("fail")
							}
							return errors.New("waiting for the lock")
						}
						if bc.Client == mgr.GetClient() {
							return errors.New("backing cluster client must be different from the management cluster client")
						}
						if bc.Namespace != backingClusterNamespaceName(i) {
							return errors.New("backing cluster namespace must match the one defined in the BackingClusterSpec")
						}

						return nil
					}, clientCreateDelay*5).Should(BeNil()) // We wait longer than clientCreateDelay, but less then clientCreateDelay for each reconcile (so we are sure it reuses existing accessors)

					wg.Done()
				}(i)
			}
		}
		wg.Wait()

		// Check that no accessor gets removed from the backing cluster tracker while it is still used.
		g.Consistently(func() int {
			return len(bct.clusterAccessors)
		}, healthCheckPollInterval*healthCheckUnhealthyThreshold*2).Should(Equal(numberOfBackingClusters)) // Wait more than it usually takes to remove accessors

		// Delete some Kubemark machines thus stopping to use corresponding backing clusters
		numberOfBackingClustersToDrop := numberOfBackingClusters / 2
		for _, m := range machines {
			m := m
			for i := 0; i < numberOfBackingClustersToDrop; i++ {
				if m.Spec.BackingCluster != nil && m.Spec.BackingCluster.SecretRef.Name == backingClusterName(i) {
					mgr.GetClient().Delete(ctx, m)
				}
			}
		}

		// Check that accessors for backing clusters not anymore used gets removed from the backing cluster tracker.
		g.Eventually(func() int {
			return len(bct.clusterAccessors)
		}, healthCheckPollInterval*5).Should(Equal(numberOfBackingClusters - numberOfBackingClustersToDrop)) // Wait more than it usually takes to remove accessors
	})
}

func newFakeManager(objs ...client.Object) ctrl.Manager {
	c := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(objs...).Build()
	return &fakeManger{
		client: c,
	}
}

type fakeManger struct {
	client client.Client
}

func (f fakeManger) SetFields(i interface{}) error {
	panic("implement me")
}

func (f fakeManger) GetConfig() *rest.Config {
	panic("implement me")
}

func (f fakeManger) GetScheme() *runtime.Scheme {
	panic("implement me")
}

func (f fakeManger) GetClient() client.Client {
	return f.client
}

func (f fakeManger) GetFieldIndexer() client.FieldIndexer {
	panic("implement me")
}

func (f fakeManger) GetCache() cache.Cache {
	panic("implement me")
}

func (f fakeManger) GetEventRecorderFor(name string) record.EventRecorder {
	panic("implement me")
}

func (f fakeManger) GetRESTMapper() meta.RESTMapper {
	panic("implement me")
}

func (f fakeManger) GetAPIReader() client.Reader {
	panic("implement me")
}

func (f fakeManger) Add(runnable manager.Runnable) error {
	panic("implement me")
}

func (f fakeManger) Elected() <-chan struct{} {
	panic("implement me")
}

func (f fakeManger) AddMetricsExtraHandler(path string, handler http.Handler) error {
	panic("implement me")
}

func (f fakeManger) AddHealthzCheck(name string, check healthz.Checker) error {
	panic("implement me")
}

func (f fakeManger) AddReadyzCheck(name string, check healthz.Checker) error {
	panic("implement me")
}

func (f fakeManger) Start(ctx context.Context) error {
	panic("implement me")
}

func (f fakeManger) GetWebhookServer() *webhook.Server {
	panic("implement me")
}

func (f fakeManger) GetLogger() logr.Logger {
	panic("implement me")
}

func (f fakeManger) GetControllerOptions() v1alpha1.ControllerConfigurationSpec {
	panic("implement me")
}

const clientCreateDelay = 3 + time.Second

func createFakeClient(scheme *runtime.Scheme, _ *rest.Config, _ client.ObjectKey) (client.Client, meta.RESTMapper, error) {
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	// Add some delays to extend lock o create accessor, thus testing if concurrent request are treated properly.
	time.Sleep(clientCreateDelay)
	return c, nil, nil
}

func createFakeCache(config *rest.Config, opts cache.Options) (cache.Cache, error) {
	return &fakeCache{}, nil
}

type fakeCache struct {
}

func (f fakeCache) Get(ctx context.Context, key client.ObjectKey, obj client.Object, options ...client.GetOption) error {
	panic("implement me")
}

func (f fakeCache) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	panic("implement me")
}

func (f fakeCache) GetInformer(ctx context.Context, obj client.Object) (cache.Informer, error) {
	panic("implement me")
}

func (f fakeCache) GetInformerForKind(ctx context.Context, gvk schema.GroupVersionKind) (cache.Informer, error) {
	panic("implement me")
}

func (f fakeCache) Start(ctx context.Context) error {
	return nil
}

func (f fakeCache) WaitForCacheSync(ctx context.Context) bool {
	return true
}

func (f fakeCache) IndexField(ctx context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
	panic("implement me")
}
