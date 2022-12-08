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
	"k8s.io/apimachinery/pkg/types"
	"os"
	"time"

	infrav1 "github.com/kubernetes-sigs/cluster-api-provider-kubemark/api/v1alpha4"
	"github.com/kubernetes-sigs/cluster-api-provider-kubemark/controllers"
	expinfrav1 "github.com/kubernetes-sigs/cluster-api-provider-kubemark/exp/cluster/api/v1alpha4"
	expcontrolplanev1 "github.com/kubernetes-sigs/cluster-api-provider-kubemark/exp/controlplane/api/v1alpha4"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	bootstrapv1 "sigs.k8s.io/cluster-api/bootstrap/kubeadm/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/annotations"
	"sigs.k8s.io/cluster-api/util/kubeconfig"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/predicates"
	"sigs.k8s.io/cluster-api/util/secret"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	serviceCIDR = "10.96.0.0/16"
	podCIDR     = "10.244.0.0/16"
	dnsDomain   = "cluster.local"
)

// KubemarkControlPlaneReconciler reconciles a KubemarkControlPlane object
type KubemarkControlPlaneReconciler struct {
	client.Client
	BackingClusterTracker *controllers.BackingClusterTracker

	// WatchFilterValue is the label value used to filter events prior to reconciliation.
	WatchFilterValue string
}

// SetupWithManager will add watches for this controller.
func (r *KubemarkControlPlaneReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager, options controller.Options) error {
	c, err := ctrl.NewControllerManagedBy(mgr).
		For(&expcontrolplanev1.KubemarkControlPlane{}).
		WithOptions(options).
		WithEventFilter(predicates.ResourceNotPausedAndHasFilterLabel(ctrl.LoggerFrom(ctx), r.WatchFilterValue)).
		Build(r)
	if err != nil {
		return errors.Wrap(err, "failed setting up with a controller manager")
	}

	err = c.Watch(
		&source.Kind{Type: &clusterv1.Cluster{}},
		handler.EnqueueRequestsFromMapFunc(r.ClusterToKubemarkControlPlane),
		predicates.All(ctrl.LoggerFrom(ctx),
			predicates.ClusterUnpausedAndInfrastructureReady(ctrl.LoggerFrom(ctx)),
		),
	)
	if err != nil {
		return errors.Wrap(err, "failed adding Watch for Clusters to KubemarkControlPlanes")
	}
	return nil
}

// ClusterToKubemarkControlPlane is a handler.ToRequestsFunc to be used to enqueue requests for reconciliation
// for KubemarkControlPlane based on updates to a Cluster.
func (r *KubemarkControlPlaneReconciler) ClusterToKubemarkControlPlane(o client.Object) []ctrl.Request {
	c, ok := o.(*clusterv1.Cluster)
	if !ok {
		panic(fmt.Sprintf("Expected a Cluster but got a %T", o))
	}

	controlPlaneRef := c.Spec.ControlPlaneRef
	if controlPlaneRef != nil && controlPlaneRef.Kind == "KubemarkControlPlane" {
		return []ctrl.Request{{NamespacedName: client.ObjectKey{Namespace: controlPlaneRef.Namespace, Name: controlPlaneRef.Name}}}
	}
	return nil
}

// RBAC rules required by the KubemarkClusterReconciler to work on the management cluster.
// +kubebuilder:rbac:groups=controlplane.cluster.x-k8s.io,resources=kubemarkcontrolplanes;kubemarkcontrolplanes/status,verbs=patch;get;list;watch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=kubemarkclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters;clusters/status,verbs=get;list;watch

// RBAC rules required by the KubemarkClusterReconciler to work on the backing cluster.
// +kubebuilder:rbac:groups="",resources=secrets,verbs=create;delete;get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=create;delete;get;list;watch
// +kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources=roles,verbs=create;delete;get;list;watch
// +kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources=rolebindings,verbs=create;delete;get;list;watch

func (r *KubemarkControlPlaneReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, rerr error) {
	log := ctrl.LoggerFrom(ctx)

	log.Info("DEBUG: Reconcile!")

	// Fetch the KubemarkControlPlane instance
	kubemarkControlPlane := &expcontrolplanev1.KubemarkControlPlane{}
	if err := r.Client.Get(ctx, req.NamespacedName, kubemarkControlPlane); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("KubemarkControlPlane instance not found")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Fetch the Cluster.
	cluster, err := util.GetOwnerCluster(ctx, r.Client, kubemarkControlPlane.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, err
	}
	if cluster == nil {
		log.Info("Waiting for Cluster Controller to set OwnerRef on KubemarkControlPlane")
		return ctrl.Result{}, nil
	}

	log = log.WithValues("cluster", klog.KObj(cluster))

	// Fetch the KubemarkCluster.
	kubemarkCluster := &expinfrav1.KubemarkCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cluster.Spec.InfrastructureRef.Name,
			Namespace: cluster.Spec.InfrastructureRef.Namespace,
		},
	}
	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(kubemarkCluster), kubemarkCluster); err != nil {
		return ctrl.Result{}, err
	}

	log = log.WithValues("kubemarkcluster", klog.KObj(kubemarkCluster))
	ctx = ctrl.LoggerInto(ctx, log)

	// Stop reconciliation if the kubemarkControlPlane or the cluster are paused.
	if annotations.IsPaused(cluster, kubemarkControlPlane) {
		log.Info("Reconciliation is paused for this object")
		return ctrl.Result{}, nil
	}

	// Initialize the patch helper
	patchHelper, err := patch.NewHelper(kubemarkControlPlane, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}
	// Always attempt to Patch the KubemarkControlPlane object and status after each reconciliation.
	defer func() {
		if err := patchHelper.Patch(ctx, kubemarkControlPlane); err != nil {
			log.Error(err, "failed to patch KubemarkControlPlane")
			if rerr == nil {
				rerr = err
			}
		}
	}()

	// Add finalizer first if not exist to avoid the race condition between init and delete
	if !controllerutil.ContainsFinalizer(kubemarkControlPlane, expcontrolplanev1.ControlPlaneFinalizer) {
		log.Info("Adding finalizer")
		controllerutil.AddFinalizer(kubemarkControlPlane, expcontrolplanev1.ControlPlaneFinalizer)
		return ctrl.Result{}, nil
	}

	// Gets the backing cluster hosting the infrastructure for this kubemarkCluster.
	backingClusterClient, err := r.BackingClusterTracker.GetFor(ctx, kubemarkCluster)
	if err != nil {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, err
	}

	// Prepare the reconcile scope.
	s := &controlPlaneScope{
		client:               r.Client,
		backingCluster:       backingClusterClient,
		cluster:              cluster,
		kubemarkCluster:      kubemarkCluster,
		kubemarkControlPlane: kubemarkControlPlane,
	}

	// Handle deleted clusters
	if !kubemarkControlPlane.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, s)
	}

	// Handle non-deleted clusters
	return r.reconcileNormal(ctx, s)
}

func (r *KubemarkControlPlaneReconciler) reconcileNormal(ctx context.Context, s *controlPlaneScope) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	// Wait for the cluster infrastructure to be ready before creating machines
	if !s.cluster.Status.InfrastructureReady {
		log.Info("Cluster infrastructure is not ready yet")
		return ctrl.Result{}, nil
	}

	// If ControlPlaneEndpoint is not set, return early
	if !s.cluster.Spec.ControlPlaneEndpoint.IsValid() {
		log.Info("Cluster does not yet have a ControlPlaneEndpoint defined")
		return ctrl.Result{}, nil
	}

	phases := []func(context.Context, *controlPlaneScope) (ctrl.Result, error){
		r.reconcileCertificates,
		r.reconcileKubeConfig,
		r.reconcilePods,
	}

	res := ctrl.Result{}
	errs := []error{}
	for _, phase := range phases {
		phaseResult, err := phase(ctx, s)
		if err != nil {
			errs = append(errs, err)
		}
		if len(errs) > 0 {
			continue
		}
		res = util.LowestNonZeroResult(res, phaseResult)
	}
	return res, kerrors.NewAggregate(errs)
}

// reconcileCertificates reconcile the cluster certificates in the management cluster, as required by the CAPI contract.
// TODO: change the implementation so we have logs when creating, we fail if certificates are missing after CP has been generated.
func (r *KubemarkControlPlaneReconciler) reconcileCertificates(ctx context.Context, s *controlPlaneScope) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.Info("DEBUG: reconcileCertificates")
	if err := s.caSecretHandler().LookupOrGenerate(ctx); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to generate cluster's certificate authorities")
	}
	return ctrl.Result{}, nil
}

// reconcileKubeConfig reconcile the cluster admin kubeconfig in the management cluster, as required by the CAPI contract.
// TODO: change the implementation so we have logs when creating
func (r *KubemarkControlPlaneReconciler) reconcileKubeConfig(ctx context.Context, s *controlPlaneScope) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.Info("DEBUG: reconcileKubeConfig")
	// If the secret with the CA is not yet in cache, wait fo in a bit before giving up.
	if err := util.PollImmediate(250*time.Millisecond, 5*time.Second, func() (bool, error) {
		if _, err := secret.GetFromNamespacedName(ctx, s.backingCluster.Client, client.ObjectKeyFromObject(s.cluster), secret.ClusterCA); err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	}); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to read cluster CA while generating admin kubeconfig")
	}

	// Note: the kubemarkControlPlane doesn't support implement kubeconfig client certificate renewal,
	// but this is considered acceptable for the goals of the kubemark provider.
	if err := s.kubeConfigSecretHandler().LookupOrGenerate(ctx); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to generate secret with the cluster's admin kubeconfig")
	}
	return ctrl.Result{}, nil
}

// reconcilePods reconcile pods hosting a control plane replicas.
// Note: The implementation currently manage one replica without remediation support, but there is already part of
// scaffolding for implementing support for n replicas.
// TODO: implement, support for n replicas, remediation
func (r *KubemarkControlPlaneReconciler) reconcilePods(ctx context.Context, s *controlPlaneScope) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.Info("DEBUG: reconcilePods")

	// Create RBAC rules for the pod to run.
	if err := s.podHandler().LookupAndGenerateRBAC(ctx); err != nil {
		return ctrl.Result{}, err
	}

	// Gets the list of pods hosting a control plane replicas.
	pods, err := r.getPods(ctx, s)
	if err != nil {
		return ctrl.Result{}, err
	}

	if len(pods.Items) < 1 {
		log.Info("Scaling up control plane replicas to 1")
		if err := s.podHandler().Generate(ctx, s.kubemarkControlPlane.Spec.Version); err != nil {
			return ctrl.Result{}, errors.Wrap(err, "failed to generate control plane pod")
		}
		// Requeue so we can refresh the list of pods hosting a control plane replicas.
		return ctrl.Result{Requeue: true}, nil
	}

	// TODO: double check the semantic of ready and initialized.

	s.kubemarkControlPlane.Status.Ready = true
	for _, pod := range pods.Items {
		for _, container := range pod.Status.ContainerStatuses {
			if container.State.Running == nil {
				s.kubemarkControlPlane.Status.Ready = false
				break
			}
		}
	}

	if s.kubemarkControlPlane.Status.Ready {
		log.Info("Control plane initialized")
		s.kubemarkControlPlane.Status.Initialized = true
		return ctrl.Result{}, nil
	}

	// Wait for the pod to become running.
	log.Info("Waiting for Control plane pods to become running")
	// TODO: watch for CP pods in the backing cluster and drop requeueAfter
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

func (r *KubemarkControlPlaneReconciler) getPods(ctx context.Context, s *controlPlaneScope) (*corev1.PodList, error) {
	options := []client.ListOption{
		client.InNamespace(s.backingCluster.Namespace),
		client.MatchingLabels{
			clusterv1.ClusterLabelName:             s.cluster.Name,
			clusterv1.MachineControlPlaneLabelName: "",
		},
	}

	// TODO: live client or wait for cache update ...
	pods := &corev1.PodList{}
	if err := s.backingCluster.List(ctx, pods, options...); err != nil {
		return nil, errors.Wrap(err, "failed to list control plane pods")
	}
	return pods, nil
}

func (r *KubemarkControlPlaneReconciler) reconcileDelete(ctx context.Context, s *controlPlaneScope) (ctrl.Result, error) {
	// Delete all pods
	// TODO: move in handler
	pods, err := r.getPods(ctx, s)
	if err != nil {
		return ctrl.Result{}, err
	}

	for _, pod := range pods.Items {
		if err := s.podHandler().Delete(ctx, pod.Name); err != nil {
			if !apierrors.IsNotFound(err) {
				return ctrl.Result{}, errors.Wrap(err, "failed to generate control plane pod")
			}
		}
	}

	// TODO: Cleanup RBAC (might be they should be renamed by Cluster)

	// TODO: Delete kubeconfig? it should go away via garbage collector...

	// TODO: Delete all secrets? it should go away via garbage collector...

	// ControlPlane is deleted so remove the finalizer.
	controllerutil.RemoveFinalizer(s.kubemarkControlPlane, expcontrolplanev1.ControlPlaneFinalizer)
	return ctrl.Result{}, nil
}

type controlPlaneScope struct {
	client               client.Client
	backingCluster       *controllers.BackingCluster
	cluster              *clusterv1.Cluster
	kubemarkCluster      *expinfrav1.KubemarkCluster
	kubemarkControlPlane *expcontrolplanev1.KubemarkControlPlane
}

func (s *controlPlaneScope) caSecretHandler() *caSecretHandler {
	return &caSecretHandler{
		controlPlaneScope: s,
	}
}

// caSecretHandler implement handling for the secrets storing the control plane certificate authorities.
type caSecretHandler struct {
	*controlPlaneScope
}

func (ca *caSecretHandler) LookupOrGenerate(ctx context.Context) error {
	certificates := secret.NewCertificatesForInitialControlPlane(&bootstrapv1.ClusterConfiguration{})
	controllerRef := metav1.NewControllerRef(ca.kubemarkControlPlane, infrav1.GroupVersion.WithKind("kubemarkControlPlane"))

	// Generate cluster certificates on the management cluster if not already there.
	if err := certificates.LookupOrGenerate(ctx, ca.client, client.ObjectKeyFromObject(ca.cluster), *controllerRef); err != nil {
		return errors.Wrap(err, "failed to generate cluster certificates on the management cluster")
	}

	// TODO: generate certificates on the backing cluster, they are required by generate files

	return nil
}

func (s *controlPlaneScope) kubeConfigSecretHandler() *kubeConfigSecretHandler {
	return &kubeConfigSecretHandler{
		controlPlaneScope: s,
	}
}

// kubeConfigSecretHandler implement handling for the secret storing the cluster admin kubeconfig.
type kubeConfigSecretHandler struct {
	*controlPlaneScope
}

func (ca *kubeConfigSecretHandler) LookupOrGenerate(ctx context.Context) error {
	controllerRef := metav1.NewControllerRef(ca.kubemarkControlPlane, infrav1.GroupVersion.WithKind("kubemarkControlPlane"))

	// If the secret with the KubeConfig already exists, then no-op.
	if k, _ := secret.GetFromNamespacedName(ctx, ca.backingCluster.Client, client.ObjectKeyFromObject(ca.cluster), secret.Kubeconfig); k != nil {
		return nil
	}

	// Otherwise it is required to generate the secret storing the cluster admin kubeconfig.
	if err := kubeconfig.CreateSecretWithOwner(ctx, ca.client, client.ObjectKeyFromObject(ca.cluster), ca.cluster.Spec.ControlPlaneEndpoint.String(), *controllerRef); err != nil {
		return errors.Wrap(err, "failed to generate cluster certificates on the management cluster")
	}
	return nil
}

// kubeConfigSecretHandler implement handling for the pod hosting a control plane replica.
func (s *controlPlaneScope) podHandler() *controlPlanePodHandler {
	return &controlPlanePodHandler{
		controlPlaneScope: s,
	}
}

// controlPlanePodHandler implement handling for the Pod implementing a control plane.
type controlPlanePodHandler struct {
	*controlPlaneScope
}

func (p *controlPlanePodHandler) LookupAndGenerateRBAC(ctx context.Context) error {
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: p.backingCluster.Namespace,
			Name:      "kubemark-control-plane",
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:     []string{"get"},
				APIGroups: []string{""}, // "" indicates the core API group
				Resources: []string{"secrets"},
			},
		},
	}
	if err := p.backingCluster.Get(ctx, client.ObjectKeyFromObject(role), role); err != nil {
		switch {
		case apierrors.IsNotFound(err):
			if err := p.backingCluster.Create(ctx, role); err != nil {
				return errors.Wrap(err, "failed to create kubemark-control-plane Role")
			}
			break
		case apierrors.IsAlreadyExists(err):
			break
		default:
			return errors.Wrap(err, "failed to get kubemark-control-plane Role")
		}
	}
	roleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: p.backingCluster.Namespace,
			Name:      "kubemark-control-plane",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:     "User",
				APIGroup: "rbac.authorization.k8s.io",
				// TODO: create a service account and use it here instead of default + use it in the Pod
				Name:      "system:serviceaccount:default:default",
				Namespace: p.backingCluster.Namespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     "kubemark-control-plane",
		},
	}
	if err := p.backingCluster.Get(ctx, client.ObjectKeyFromObject(roleBinding), roleBinding); err != nil {
		switch {
		case apierrors.IsNotFound(err):
			if err := p.backingCluster.Create(ctx, roleBinding); err != nil {
				return errors.Wrap(err, "failed to create kubemark-control-plane RoleBinding")
			}
			break
		case apierrors.IsAlreadyExists(err):
			break
		default:
			return errors.Wrap(err, "failed to get kubemark-control-plane RoleBinding")
		}
	}
	return nil
}

func (p *controlPlanePodHandler) Generate(ctx context.Context, kubernetesVersion string) error {
	// Gets info about the Pod is running the manager in.
	managerPodNamespace := os.Getenv("POD_NAMESPACE")
	managerPodName := os.Getenv("POD_NAME")
	managerPodUID := types.UID(os.Getenv("POD_UID"))

	// Gets the Pod is running the manager in from the management cluster and validate it is the right one.
	managerPod := &corev1.Pod{}
	managerPodKey := types.NamespacedName{Namespace: managerPodNamespace, Name: managerPodName}
	if err := p.client.Get(ctx, managerPodKey, managerPod); err != nil {
		return errors.Wrap(err, "failed to get manager pod")
	}
	if managerPod.UID != managerPodUID {
		return errors.Errorf("manager pod UID does not match, expected %s, got %s", managerPodUID, managerPod.UID)
	}

	// Identify the Container is running the manager in, so we can get the image currently in use for the manager.
	managerContainer := &corev1.Container{}
	for i := range managerPod.Spec.Containers {
		c := managerPod.Spec.Containers[i]
		if c.Name == "manager" {
			managerContainer = &c
		}
	}

	if managerContainer == nil {
		return errors.New("failed to get container from manager pod")
	}

	// Generate the control plane Pod in the BackingCluster.
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: p.backingCluster.Namespace,
			//  Kubernetes will generate a name with the cluster name as a prefix.
			GenerateName: fmt.Sprintf("%s-control-plane-", p.kubemarkCluster.Name),
			Labels: map[string]string{
				//  Following labels will be used to identify the control plane pods later on.
				clusterv1.ClusterLabelName:             p.cluster.Name,
				clusterv1.MachineControlPlaneLabelName: "",
			},
		},
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{
				// Use an init container to generate all the key, certificates and KubeConfig files
				// required for the control plane to run.
				generateFilesContainer(managerContainer.Image, p.cluster.Name, p.cluster.Spec.ControlPlaneEndpoint.Host),
			},
			Containers: []corev1.Container{
				// Stacked etcd member for this control plane instance.
				etcdContainer(kubernetesVersion),
				// The control plane instance.
				// Note: control plane components are wired up in order to work well with immutable upgrades (each control plane instance is self-contained),
				apiServerContainer(kubernetesVersion),
				schedulerContainer(kubernetesVersion),
				controllerManagerContainer(kubernetesVersion),
				// eventually adds a dubug container with a volume containing all the generated files
				// TODO: add the debug container conditionally, e.g. if there is an annotation on the KubemarkControlPlane object.
				// debugContainer(),
			},
			PriorityClassName: "system-node-critical",
			SecurityContext: &corev1.PodSecurityContext{
				SeccompProfile: &corev1.SeccompProfile{
					Type: "RuntimeDefault",
				},
			},
			RestartPolicy: corev1.RestartPolicyAlways,
			Volumes: []corev1.Volume{
				{
					Name: "etcd-data",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				},
				{
					Name: "etc-kubernetes",
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				},
			},
		},
	}

	if err := p.backingCluster.Create(ctx, pod); err != nil {
		return errors.Wrap(err, "failed to create control plane pod")
	}

	// Wait for the pod to show up in the cache
	if err := util.PollImmediate(250*time.Millisecond, 5*time.Second, func() (bool, error) {
		if err := p.backingCluster.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	}); err != nil {
		return errors.Wrap(err, "failed to get newly created control plane pod")
	}
	return nil
}

func (p *controlPlanePodHandler) Delete(ctx context.Context, podName string) error {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: p.backingCluster.Namespace,
			Name:      podName,
		},
	}
	if err := p.backingCluster.Delete(ctx, pod); err != nil {
		return errors.Wrap(err, "failed to delete control plane pod")
	}
	return nil
}

func generateFilesContainer(managerImage string, clusterName string, controlPaneEndPointHost string) corev1.Container {
	c := corev1.Container{
		Name: "generate-files",
		// Note: we are using the manager instead of another binary for convenience (the manager is already built and packaged
		// into an image that is published during the release process).
		Image:           managerImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command: []string{
			"/manager",
			"--generate-control-plane-files",
		},
		Env: []corev1.EnvVar{
			{
				Name: "POD_NAMESPACE",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						APIVersion: "v1",
						FieldPath:  "metadata.namespace",
					},
				},
			},
			{
				Name: "POD_NAME",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						APIVersion: "v1",
						FieldPath:  "metadata.name",
					},
				},
			},
			{
				Name: "POD_IP",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						APIVersion: "v1",
						FieldPath:  "status.podIP",
					},
				},
			},
			{
				Name:  "CLUSTER_NAME",
				Value: clusterName,
			},
			{
				Name:  "CONTROL_PLANE_ENDPOINT_HOST",
				Value: controlPaneEndPointHost,
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "etc-kubernetes",
				MountPath: "/etc/kubernetes",
			},
		},
	}
	return c
}

func etcdContainer(kubernetesVersion string) corev1.Container {
	var etcdVersion string
	// TODO: mirror map from kubeadm
	switch kubernetesVersion {
	default:
		etcdVersion = "3.5.4-0"
	}

	c := corev1.Container{
		Name:            "etcd",
		Image:           fmt.Sprintf("registry.k8s.io/etcd:%s", etcdVersion),
		ImagePullPolicy: corev1.PullIfNotPresent,
		Env: []corev1.EnvVar{
			{
				Name: "POD_NAME",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						APIVersion: "v1",
						FieldPath:  "metadata.name",
					},
				},
			},
			{
				Name: "POD_IP",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						APIVersion: "v1",
						FieldPath:  "status.podIP",
					},
				},
			},
		},
		Command: []string{
			"etcd",
			"--advertise-client-urls=https://$(POD_IP):2379",
			"--cert-file=/etc/kubernetes/pki/etcd/server.crt",
			"--client-cert-auth=true",
			"--data-dir=/var/lib/etcd",
			"--experimental-initial-corrupt-check=true",
			"--experimental-watch-progress-notify-interval=5s",
			"--initial-advertise-peer-urls=https://$(POD_IP):2380",
			"--initial-cluster=$(POD_NAME)=https://$(POD_IP):2380",
			"--key-file=/etc/kubernetes/pki/etcd/server.key",
			"--listen-client-urls=https://127.0.0.1:2379,https://$(POD_IP):2379",
			"--listen-metrics-urls=http://127.0.0.1:2381",
			"--listen-peer-urls=https://$(POD_IP):2380",
			"--name=$(POD_NAME)",
			"--peer-cert-file=/etc/kubernetes/pki/etcd/peer.crt",
			"--peer-client-cert-auth=true",
			"--peer-key-file=/etc/kubernetes/pki/etcd/peer.key",
			"--peer-trusted-ca-file=/etc/kubernetes/pki/etcd/ca.crt",
			"--snapshot-count=10000",
			"--trusted-ca-file=/etc/kubernetes/pki/etcd/ca.crt",
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("100Mi"),
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "etcd-data",
				MountPath: "/var/lib/etcd",
			},
			{
				Name:      "etc-kubernetes",
				MountPath: "/etc/kubernetes",
			},
		},
		Ports: []corev1.ContainerPort{
			{
				Name:          "etcd-peer",
				ContainerPort: 2380,
			},
			// TODO: check if we can drop this port
			/*
				{
					Name:          "etcd-client",
					ContainerPort: 2379,
				},
			*/
		},
		// TODO: enable probes
		/*
			StartupProbe: &corev1.Probe{
				FailureThreshold: 24,
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path:   "/health?serializable=false",
						Port:   intstr.FromInt(2381),
						Scheme: corev1.URISchemeHTTP,
					},
				},
				InitialDelaySeconds: 10,
				TimeoutSeconds:      15,
				PeriodSeconds:       10,
			},
			LivenessProbe: &corev1.Probe{
				FailureThreshold: 8,
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path:   "/health?exclude=NOSPACE&serializable=true",
						Port:   intstr.FromInt(2381),
						Scheme: corev1.URISchemeHTTP,
					},
				},
				InitialDelaySeconds: 10,
				TimeoutSeconds:      15,
				PeriodSeconds:       10,
			},
		*/
	}
	return c
}

func apiServerContainer(kubernetesVersion string) corev1.Container {
	c := corev1.Container{
		Name:            "kube-apiserver",
		Image:           fmt.Sprintf("registry.k8s.io/kube-apiserver:%s", kubernetesVersion),
		ImagePullPolicy: corev1.PullIfNotPresent,
		Env: []corev1.EnvVar{
			{
				Name: "POD_IP",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						APIVersion: "v1",
						FieldPath:  "status.podIP",
					},
				},
			},
		},
		Command: []string{
			"kube-apiserver",
			"--advertise-address=$(POD_IP)",
			"--allow-privileged=true",
			"--authorization-mode=Node,RBAC",
			"--client-ca-file=/etc/kubernetes/pki/ca.crt",
			"--enable-admission-plugins=NodeRestriction",
			"--enable-bootstrap-token-auth=true",
			"--etcd-cafile=/etc/kubernetes/pki/etcd/ca.crt",
			"--etcd-certfile=/etc/kubernetes/pki/apiserver-etcd-client.crt",
			"--etcd-keyfile=/etc/kubernetes/pki/apiserver-etcd-client.key",
			"--etcd-servers=https://127.0.0.1:2379",
			"--kubelet-client-certificate=/etc/kubernetes/pki/apiserver-kubelet-client.crt",
			"--kubelet-client-key=/etc/kubernetes/pki/apiserver-kubelet-client.key",
			"--kubelet-preferred-address-types=InternalIP,ExternalIP,Hostname",
			"--proxy-client-cert-file=/etc/kubernetes/pki/front-proxy-client.crt",
			"--proxy-client-key-file=/etc/kubernetes/pki/front-proxy-client.key",
			"--requestheader-allowed-names=front-proxy-client",
			"--requestheader-client-ca-file=/etc/kubernetes/pki/front-proxy-ca.crt",
			"--requestheader-extra-headers-prefix=X-Remote-Extra-",
			"--requestheader-group-headers=X-Remote-Group",
			"--requestheader-username-headers=X-Remote-User",
			"--runtime-config=", // TODO: What about this?
			"--secure-port=6443",
			fmt.Sprintf("--service-account-issuer=https://kubernetes.default.svc.%s", dnsDomain),
			"--service-account-key-file=/etc/kubernetes/pki/sa.pub",
			"--service-account-signing-key-file=/etc/kubernetes/pki/sa.key",
			fmt.Sprintf("--service-cluster-ip-range=%s", serviceCIDR),
			"--tls-cert-file=/etc/kubernetes/pki/apiserver.crt",
			"--tls-private-key-file=/etc/kubernetes/pki/apiserver.key",
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("250m"),
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "etc-kubernetes",
				MountPath: "/etc/kubernetes",
			},
		},
		Ports: []corev1.ContainerPort{
			{
				Name:          "api-server",
				ContainerPort: 6443,
			},
		},
		// TODO: enable probes
		/*
			StartupProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path:   "/livez",
						Port:   intstr.FromInt(6443),
						Scheme: corev1.URISchemeHTTPS,
					},
				},
				InitialDelaySeconds: 10,
				TimeoutSeconds:      15,
				PeriodSeconds:       10,
			},
			ReadinessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path:   "/readyz",
						Port:   intstr.FromInt(6443),
						Scheme: corev1.URISchemeHTTPS,
					},
				},
				TimeoutSeconds: 15,
				PeriodSeconds:  1,
			},
			LivenessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path:   "/livez",
						Port:   intstr.FromInt(6443),
						Scheme: corev1.URISchemeHTTPS,
					},
				},
				InitialDelaySeconds: 10,
				TimeoutSeconds:      15,
				PeriodSeconds:       10,
			},
		*/
	}
	return c
}

func schedulerContainer(kubernetesVersion string) corev1.Container {
	c := corev1.Container{
		Name:            "kube-scheduler",
		Image:           fmt.Sprintf("registry.k8s.io/kube-scheduler:%s", kubernetesVersion),
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command: []string{
			"kube-scheduler",
			"--authentication-kubeconfig=/etc/kubernetes/scheduler.conf",
			"--authorization-kubeconfig=/etc/kubernetes/scheduler.conf",
			"--bind-address=127.0.0.1",
			"--kubeconfig=/etc/kubernetes/scheduler.conf",
			"--leader-elect=true",
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("100m"),
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "etc-kubernetes",
				MountPath: "/etc/kubernetes",
			},
		},
		// TODO: enable probes
		/*
			StartupProbe: &corev1.Probe{
				FailureThreshold: 24,
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path:   "/healthz",
						Port:   intstr.FromInt(10259),
						Scheme: corev1.URISchemeHTTPS,
					},
				},
				InitialDelaySeconds: 10,
				TimeoutSeconds:      15,
				PeriodSeconds:       10,
			},
			LivenessProbe: &corev1.Probe{
				FailureThreshold: 8,
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path:   "/healthz",
						Port:   intstr.FromInt(10259),
						Scheme: corev1.URISchemeHTTPS,
					},
				},
				InitialDelaySeconds: 10,
				TimeoutSeconds:      15,
				PeriodSeconds:       10,
			},
		*/
	}
	return c
}

func controllerManagerContainer(kubernetesVersion string) corev1.Container {
	c := corev1.Container{
		Name:            "kube-controller-manager",
		Image:           fmt.Sprintf("registry.k8s.io/kube-controller-manager:%s", kubernetesVersion),
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command: []string{
			"kube-controller-manager",
			"--allocate-node-cidrs=true",
			"--authentication-kubeconfig=/etc/kubernetes/controller-manager.conf",
			"--authorization-kubeconfig=/etc/kubernetes/controller-manager.conf",
			"--bind-address=127.0.0.1",
			"--client-ca-file=/etc/kubernetes/pki/ca.crt",
			fmt.Sprintf("--cluster-cidr=%s", podCIDR),
			"--cluster-name=kubemark",
			"--cluster-signing-cert-file=/etc/kubernetes/pki/ca.crt",
			"--cluster-signing-key-file=/etc/kubernetes/pki/ca.key",
			"--controllers=*,bootstrapsigner,tokencleaner",
			"--enable-hostpath-provisioner=true",
			"--kubeconfig=/etc/kubernetes/controller-manager.conf",
			"--leader-elect=true",
			"--requestheader-client-ca-file=/etc/kubernetes/pki/front-proxy-ca.crt",
			"--root-ca-file=/etc/kubernetes/pki/ca.crt",
			"--service-account-private-key-file=/etc/kubernetes/pki/sa.key",
			fmt.Sprintf("--service-cluster-ip-range=%s", serviceCIDR),
			"--use-service-account-credentials=true",
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("200m"),
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "etc-kubernetes",
				MountPath: "/etc/kubernetes",
			},
		},
		// TODO: enable probes
		/*
			StartupProbe: &corev1.Probe{
				FailureThreshold: 24,
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path:   "/healthz",
						Port:   intstr.FromInt(10257),
						Scheme: corev1.URISchemeHTTPS,
					},
				},
				InitialDelaySeconds: 10,
				TimeoutSeconds:      15,
				PeriodSeconds:       10,
			},
			LivenessProbe: &corev1.Probe{
				FailureThreshold: 8,
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path:   "/healthz",
						Port:   intstr.FromInt(10257),
						Scheme: corev1.URISchemeHTTPS,
					},
				},
				InitialDelaySeconds: 10,
				TimeoutSeconds:      15,
				PeriodSeconds:       10,
			},

		*/
	}
	return c
}

func debugContainer() corev1.Container {
	debugContainer := corev1.Container{
		Name:            "debug",
		Image:           "ubuntu",
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"sleep", "infinity"},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "etc-kubernetes",
				MountPath: "/etc/kubernetes",
			},
		},
	}
	return debugContainer
}
