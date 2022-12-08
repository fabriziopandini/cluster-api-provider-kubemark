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
	"time"

	infrav1 "github.com/kubernetes-sigs/cluster-api-provider-kubemark/api/v1alpha4"
	"github.com/kubernetes-sigs/cluster-api-provider-kubemark/controllers"
	expinfrav1 "github.com/kubernetes-sigs/cluster-api-provider-kubemark/exp/cluster/api/v1alpha4"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/annotations"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/predicates"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	apiServerPodPort = 6443
	lbServicePort    = 6443
)

// KubemarkClusterReconciler reconciles a KubemarkCluster object
type KubemarkClusterReconciler struct {
	client.Client
	BackingClusterTracker *controllers.BackingClusterTracker

	// WatchFilterValue is the label value used to filter events prior to reconciliation.
	WatchFilterValue string
}

// SetupWithManager will add watches for this controller.
func (r *KubemarkClusterReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager, options controller.Options) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&expinfrav1.KubemarkCluster{}).
		WithOptions(options).
		WithEventFilter(predicates.ResourceNotPausedAndHasFilterLabel(ctrl.LoggerFrom(ctx), r.WatchFilterValue)).
		Complete(r)
}

// RBAC rules required by the KubemarkClusterReconciler to work on the management cluster.
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=kubemarkclusters;kubemarkclusters/status,verbs=patch;get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;list;watch

// RBAC rules required by the KubemarkClusterReconciler to work on the backing cluster.
// +kubebuilder:rbac:groups="",resources=services,verbs=create;delete;get;list;watch

func (r *KubemarkClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, rerr error) {
	log := ctrl.LoggerFrom(ctx)

	// Fetch the KubemarkCluster instance
	kubemarkCluster := &expinfrav1.KubemarkCluster{}
	if err := r.Client.Get(ctx, req.NamespacedName, kubemarkCluster); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Fetch the Cluster.
	cluster, err := util.GetOwnerCluster(ctx, r.Client, kubemarkCluster.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, err
	}
	if cluster == nil {
		log.Info("Waiting for Cluster Controller to set OwnerRef on KubemarkCluster")
		return ctrl.Result{}, nil
	}

	log = log.WithValues("cluster", klog.KObj(cluster))
	ctx = ctrl.LoggerInto(ctx, log)

	// Stop reconciliation if the kubemarkCluster or the cluster are paused.
	if annotations.IsPaused(cluster, kubemarkCluster) {
		log.Info("Reconciliation is paused for this object")
		return ctrl.Result{}, nil
	}

	// Initialize the patch helper
	patchHelper, err := patch.NewHelper(kubemarkCluster, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}
	// Always attempt to Patch the KubemarkCluster object and status after each reconciliation.
	defer func() {
		if err := patchHelper.Patch(ctx, kubemarkCluster); err != nil {
			log.Error(err, "failed to patch KubemarkCluster")
			if rerr == nil {
				rerr = err
			}
		}
	}()

	// Add finalizer first if not exist to avoid the race condition between init and delete
	if !controllerutil.ContainsFinalizer(kubemarkCluster, expinfrav1.ClusterFinalizer) {
		controllerutil.AddFinalizer(kubemarkCluster, expinfrav1.ClusterFinalizer)
		return ctrl.Result{}, nil
	}

	// Gets the backing cluster hosting the infrastructure for this kubemarkCluster.
	backingClusterClient, err := r.BackingClusterTracker.GetFor(ctx, kubemarkCluster)
	if err != nil {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, err
	}

	// Prepare the reconcile scope.
	s := &clusterScope{
		backingCluster:  backingClusterClient,
		cluster:         cluster,
		kubemarkCluster: kubemarkCluster,
	}

	// Handle deleted clusters
	if !kubemarkCluster.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, s)
	}

	// Handle non-deleted clusters
	return r.reconcileNormal(ctx, s)
}

func (r *KubemarkClusterReconciler) reconcileNormal(ctx context.Context, s *clusterScope) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	// If the kubemarkCluster is already in status ready then we are done.
	// Note: the kubemarkCluster doesn't have the capability to recover from the manual deletion of lbServiceHandler,
	// but this is considered acceptable for the goals of the kubemark provider.
	// TODO: investigate if we can re-create the service with a well known ClusterIP/Port.
	if s.kubemarkCluster.Status.Ready == true {
		return ctrl.Result{}, nil
	}

	// Get the load balancer service.
	log.Info("Creating the Kubernetes Service acting as a cluster load balancer")
	svc, err := s.LoadBalancerServiceHandler().LookupOrGenerate(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Wait for the cluster IP to show up.
	if svc.Spec.ClusterIP == "" {
		return ctrl.Result{Requeue: true}, err
	}

	// If service ports are not as expected, delete the service at best effort
	// Note: this should never happen (it happens if someone change the service while being created or immediately after).
	if len(svc.Spec.Ports) != 1 {
		_ = s.LoadBalancerServiceHandler().Delete(ctx)
		return ctrl.Result{}, errors.Errorf("service doesn't have the expected port")
	}

	s.kubemarkCluster.Spec.ControlPlaneEndpoint.Host = svc.Spec.ClusterIP
	s.kubemarkCluster.Spec.ControlPlaneEndpoint.Port = int(svc.Spec.Ports[0].Port)

	// KubemarkCluster is ready
	s.kubemarkCluster.Status.Ready = true
	log.Info("Cluster infrastructure ready", "service", klog.KObj(svc))

	return ctrl.Result{}, nil
}

func (r *KubemarkClusterReconciler) reconcileDelete(ctx context.Context, s *clusterScope) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	log.Info("Deleting the Kubernetes Service acting as a load balancer in front of all the control plane instances")
	if err := s.LoadBalancerServiceHandler().Delete(ctx); err != nil {
		return ctrl.Result{}, err
	}
	log.Info("Cluster infrastructure deleted")

	// Cluster is deleted so remove the finalizer.
	controllerutil.RemoveFinalizer(s.kubemarkCluster, expinfrav1.ClusterFinalizer)
	return ctrl.Result{}, nil
}

type clusterScope struct {
	backingCluster  *controllers.BackingCluster
	cluster         *clusterv1.Cluster
	kubemarkCluster *expinfrav1.KubemarkCluster
}

func (s *clusterScope) LoadBalancerServiceHandler() *lbServiceHandler {
	return &lbServiceHandler{
		clusterScope: s,
	}
}

// lbServiceHandler implement handling for the Kubernetes Service acting as a load balancer in front of all the control plane instances.
type lbServiceHandler struct {
	*clusterScope
}

func (lb *lbServiceHandler) ObjectKey() client.ObjectKey {
	return client.ObjectKey{
		// All the object must use the BlusterNamespace.
		Namespace: lb.backingCluster.Namespace,
		// Note: The implementation currently rely on a naming convention without random suffix, and this brings in the
		// limitation that two clusters with the same name cannot be hosted in a single namespace of the backing cluster;
		// however this is considered acceptable for the goals of the kubemark provider.
		Name: fmt.Sprintf("%s-lb", lb.cluster.Name),
	}
}

func (lb *lbServiceHandler) LookupOrGenerate(ctx context.Context) (*corev1.Service, error) {
	// Lookup the load balancer service.
	svc, err := lb.Lookup(ctx)
	if err != nil {
		return nil, err
	}
	if svc != nil {
		return svc, nil
	}
	return lb.Generate(ctx)
}

func (lb *lbServiceHandler) Lookup(ctx context.Context) (*corev1.Service, error) {
	key := lb.ObjectKey()
	secret := &corev1.Service{}
	if err := lb.backingCluster.Get(ctx, key, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, errors.Wrapf(err, "failed to get load balance service")
	}
	return secret, nil
}

func (lb *lbServiceHandler) Generate(ctx context.Context) (*corev1.Service, error) {
	key := lb.ObjectKey()
	secret := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      key.Name,
			Namespace: key.Namespace,
			// Note: the code is taking care of service cleanup during the deletion workflow,
			// so this ownerRef is mostly used to express a semantic relation.
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         infrav1.GroupVersion.String(),
					Kind:               "KubemarkCluster",
					Name:               lb.kubemarkCluster.Name,
					UID:                lb.kubemarkCluster.UID,
					Controller:         pointer.Bool(true),
					BlockOwnerDeletion: pointer.Bool(true),
				},
			},
		},
		Spec: corev1.ServiceSpec{
			// This selector must match labels on apiServerPods.
			Selector: map[string]string{
				clusterv1.ClusterLabelName:             lb.cluster.Name,
				clusterv1.MachineControlPlaneLabelName: "",
			},
			// Currently we support only services of type IP, also
			Type: corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{
				{
					Port:       lbServicePort,
					TargetPort: intstr.FromInt(apiServerPodPort),
				},
			},
		},
	}
	if err := lb.backingCluster.Create(ctx, secret); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil, err
		}
		return nil, errors.Wrapf(err, "failed to create load balance service")
	}
	return secret, nil
}

func (lb *lbServiceHandler) Delete(ctx context.Context) error {
	key := lb.ObjectKey()
	secret := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      key.Name,
			Namespace: key.Namespace,
		},
	}
	if err := lb.backingCluster.Delete(ctx, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return errors.Wrapf(err, "failed to delete load balance service")
	}
	return nil
}
