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

package controllers

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	cryptorand "crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	expinfrav1 "github.com/kubernetes-sigs/cluster-api-provider-kubemark/exp/cluster/api/v1alpha4"
	"github.com/pkg/errors"
	"k8s.io/klog/v2"
	"math/big"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/predicates"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"
	"strings"
	"time"

	"github.com/Masterminds/semver"
	"github.com/go-logr/logr"
	infrav1 "github.com/kubernetes-sigs/cluster-api-provider-kubemark/api/v1alpha4"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	restclient "k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	clientcmdlatest "k8s.io/client-go/tools/clientcmd/api/latest"
	"k8s.io/client-go/util/cert"
	"k8s.io/client-go/util/keyutil"
	"k8s.io/utils/pointer"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/controllers/remote"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/certs"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/secret"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	kubemarkName = "hollow-node"

	// MachineControllerName defines the user-agent name used when creating rest clients
	MachineControllerName = "kubemarkmachine-controller"
)

// KubemarkMachineReconciler reconciles a KubemarkMachine object
type KubemarkMachineReconciler struct {
	client.Client
	BackingClusterTracker *BackingClusterTracker
	Log                   logr.Logger
	Scheme                *runtime.Scheme
	KubemarkImage         string

	// WatchFilterValue is the label value used to filter events prior to reconciliation.
	WatchFilterValue string
}

func (r *KubemarkMachineReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager, options controller.Options) error {
	c, err := ctrl.NewControllerManagedBy(mgr).
		For(&infrav1.KubemarkMachine{}).
		WithOptions(options).
		Watches(
			&source.Kind{Type: &clusterv1.Machine{}},
			handler.EnqueueRequestsFromMapFunc(util.MachineToInfrastructureMapFunc(infrav1.GroupVersion.WithKind("KubemarkMachine"))),
		).
		WithEventFilter(predicates.ResourceNotPausedAndHasFilterLabel(ctrl.LoggerFrom(ctx), r.WatchFilterValue)).
		Build(r)
	if err != nil {
		return errors.Wrap(err, "failed setting up with a controller manager")
	}

	clusterToKubemarkMachines, err := util.ClusterToObjectsMapper(mgr.GetClient(), &infrav1.KubemarkMachineList{}, mgr.GetScheme())
	if err != nil {
		return errors.Wrap(err, "failed create MapFunc for Watch for Clusters to KubemarkMachines")
	}
	err = c.Watch(
		&source.Kind{Type: &clusterv1.Cluster{}},
		handler.EnqueueRequestsFromMapFunc(clusterToKubemarkMachines),
		predicates.ClusterUnpausedAndInfrastructureReady(ctrl.LoggerFrom(ctx)),
	)
	if err != nil {
		return errors.Wrap(err, "failed adding Watch for Clusters to KubemarkMachines")
	}
	return nil
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=kubemarkmachines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=kubemarkmachines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines;machines/status,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters;clusters/status,verbs=get;list;watch
// +kubebuilder:rbac:groups=bootstrap.cluster.x-k8s.io,resources=kubeadmconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=create;get;list;watch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=create;delete

func (r *KubemarkMachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("kubemarkmachine", req.NamespacedName)

	kubemarkMachine := &infrav1.KubemarkMachine{}
	err := r.Get(ctx, req.NamespacedName, kubemarkMachine)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		log.Error(err, "error finding kubemark machine")
		return ctrl.Result{}, err
	}

	// Fetch the Machine.
	machine, err := util.GetOwnerMachine(ctx, r.Client, kubemarkMachine.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, err
	}
	if machine == nil {
		log.Info("Waiting for Machine Controller to set OwnerRef on KubemarkMachine")
		return ctrl.Result{}, nil
	}

	log = log.WithValues("Machine", klog.KObj(machine))
	ctx = ctrl.LoggerInto(ctx, log)

	// Fetch the Cluster.
	cluster, err := util.GetClusterFromMetadata(ctx, r.Client, machine.ObjectMeta)
	if err != nil {
		log.Info("KubemarkMachine owner Machine is missing cluster label or cluster does not exist")
		return ctrl.Result{}, err
	}
	if cluster == nil {
		log.Info(fmt.Sprintf("Please associate this machine with a cluster using the label %s: <name of cluster>", clusterv1.ClusterLabelName))
		return ctrl.Result{}, nil
	}

	log = log.WithValues("Cluster", klog.KObj(cluster))

	// Fetch the KubemarkCluster, if any.
	var kubemarkCluster *expinfrav1.KubemarkCluster
	if cluster.Spec.InfrastructureRef.GroupVersionKind().GroupKind() == expinfrav1.GroupVersion.WithKind("KubemarkCluster").GroupKind() {
		kubemarkCluster = &expinfrav1.KubemarkCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cluster.Spec.InfrastructureRef.Name,
				Namespace: cluster.Spec.InfrastructureRef.Namespace,
			},
		}
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(kubemarkCluster), kubemarkCluster); err != nil {
			return ctrl.Result{}, err
		}

		log = log.WithValues("kubemarkcluster", klog.KObj(kubemarkCluster))
	}

	helper, err := patch.NewHelper(kubemarkMachine, r.Client)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to init patch helper: %w", err)
	}

	controllerutil.AddFinalizer(kubemarkMachine, infrav1.MachineFinalizer)
	if err := helper.Patch(ctx, kubemarkMachine); err != nil {
		log.Error(err, "failed to add finalizer")
		return ctrl.Result{}, err
	}

	defer func() {
		if err := helper.Patch(ctx, kubemarkMachine); err != nil {
			if !apierrors.IsNotFound(err) {
				log.Error(err, "failed to patch kubemarkMachine")
			}
		}
	}()

	backingCluster, err := r.BackingClusterTracker.GetFor(ctx, kubemarkMachine, kubemarkCluster)
	if err != nil {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, err
	}

	if !kubemarkMachine.ObjectMeta.DeletionTimestamp.IsZero() {
		log.Info("deleting machine")

		if err := backingCluster.Delete(ctx, &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      kubemarkMachine.Name,
				Namespace: backingCluster.Namespace,
			},
		}); err != nil {
			if !apierrors.IsNotFound(err) {
				log.Error(err, "error deleting kubemark pod")
				return ctrl.Result{}, err
			}
		}
		if err := backingCluster.Delete(ctx, &v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      kubemarkMachine.Name,
				Namespace: backingCluster.Namespace,
			},
		}); err != nil {
			if !apierrors.IsNotFound(err) {
				log.Error(err, "error deleting kubemark configMap")
				return ctrl.Result{}, err
			}
		}
		controllerutil.RemoveFinalizer(kubemarkMachine, infrav1.MachineFinalizer)
		return ctrl.Result{}, nil
	}

	if kubemarkMachine.Status.Ready {
		log.Info("machine already ready, skipping reconcile")
		return ctrl.Result{}, err
	}

	machinePatchHelper, err := patch.NewHelper(machine, r.Client)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to init patch helper: %w", err)
	}
	defer func() {
		if err := machinePatchHelper.Patch(ctx, machine); err != nil {
			if !apierrors.IsNotFound(err) {
				log.Error(err, "failed to patch machine")
			}
		}
	}()

	if !cluster.Status.InfrastructureReady {
		log.Info("Cluster infrastructure is not ready yet")
		return ctrl.Result{}, nil
	}
	if !conditions.IsTrue(cluster, clusterv1.ControlPlaneInitializedCondition) {
		log.Info("Cluster control plane is not initialized yet")
		// TODO: investigate this, it seems we don't have an event when CP became initialized
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		// return ctrl.Result{}, nil
	}
	if machine.Spec.Bootstrap.DataSecretName == nil {
		log.Info("Bootstrap data secret reference is not yet available")
		return ctrl.Result{}, nil
	}

	// TODO: investigate this, why are we creating a remoteconfig?
	restConfig, err := getRemoteCluster(ctx, log, r.Client, cluster)
	if err != nil {
		log.Error(err, "error getting remote cluster")
		return ctrl.Result{}, err
	}

	var caSecret v1.Secret
	if err := r.Get(ctx, client.ObjectKey{
		Name:      secret.Name(cluster.Name, secret.ClusterCA),
		Namespace: cluster.Namespace,
	}, &caSecret); err != nil {
		log.Error(err, "error getting cluster CA secret")
		return ctrl.Result{}, err
	}

	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	if err != nil {
		log.Error(err, "failed to generate private key")
		return ctrl.Result{}, err
	}
	der, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		log.Error(err, "failed to marshal the private key to DER")
		return ctrl.Result{}, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: keyutil.ECPrivateKeyBlockType, Bytes: der})

	caCert, err := certs.DecodeCertPEM(caSecret.Data[secret.TLSCrtDataName])
	if err != nil {
		log.Error(err, "failed to decode ca certificate")
		return ctrl.Result{}, err
	}
	caKey, err := certs.DecodePrivateKeyPEM(caSecret.Data[secret.TLSKeyDataName])
	if err != nil {
		log.Error(err, "err decoding ca private key")
		return ctrl.Result{}, err
	}

	now := time.Now().UTC()
	kubeletCert := &x509.Certificate{
		SerialNumber: new(big.Int).SetInt64(0),
		Subject: pkix.Name{
			CommonName:   fmt.Sprintf("system:node:%s", kubemarkMachine.Name),
			Organization: []string{"system:nodes"},
		},
		NotBefore: now.Add(time.Minute * -5),
		NotAfter:  now.Add(time.Hour * 24 * 365 * 10),
		KeyUsage:  x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageClientAuth,
		},
	}
	certBytes, err := x509.CreateCertificate(cryptorand.Reader, kubeletCert, caCert, &privateKey.PublicKey, caKey)
	if err != nil {
		log.Error(err, "err creating kubelet certificate")
		return ctrl.Result{}, err
	}

	kubeconfig, err := generateCertificateKubeconfig(restConfig, "/kubeconfig/cert.pem")
	if err != nil {
		log.Error(err, "err generating certificate kubeconfig")
		return ctrl.Result{}, err
	}

	stackedCert := bytes.Buffer{}
	if err := pem.Encode(&stackedCert, &pem.Block{Type: cert.CertificateBlockType, Bytes: certBytes}); err != nil {
		log.Error(err, "err encoding certificate")
		return ctrl.Result{}, err
	}
	if _, err := stackedCert.Write(keyPEM); err != nil {
		log.Error(err, "err writing pem bytes")
		return ctrl.Result{}, err
	}

	secret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kubemarkMachine.Name,
			Namespace: backingCluster.Namespace,
		},
		Data: map[string][]byte{
			"kubeconfig": kubeconfig,
			"cert.pem":   stackedCert.Bytes(),
		},
	}
	if err := backingCluster.Create(ctx, secret); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			log.Error(err, "failed to create secret")
			return ctrl.Result{}, err
		}
	}
	version := machine.Spec.Version
	if version == nil {
		err := errors.New("Machine has no spec.version")
		log.Error(err, "")
		return ctrl.Result{}, err
	}

	kubemarkArgs := []string{
		"--v=3",
		"--morph=kubelet",
		"--log-file=/var/log/kubelet.log",
		"--logtostderr=false",
		fmt.Sprintf("--name=%s", kubemarkMachine.Name),
	}

	// Kubemark extended resources are only supported after version 1.22.0
	// TODO remove the version check once 1.22.0 is no longer supported.
	c, err := semver.NewConstraint(">= 1.22.0")
	if err != nil {
		log.Error(err, "Unable to create version constraint")
		return ctrl.Result{}, err
	}
	v, err := semver.NewVersion(*version)
	if err != nil {
		log.Error(err, "Unable to create version constraint")
		return ctrl.Result{}, err
	}

	if c.Check(v) {
		extendedResources := getKubemarkExtendedResources(kubemarkMachine.Spec.KubemarkOptions)
		extendedResourcesFlag := getKubemarkExtendedResourcesFlag(extendedResources)
		kubemarkArgs = append(kubemarkArgs, extendedResourcesFlag)
	} else {
		if kubemarkMachine.Spec.KubemarkOptions.ExtendedResources != nil {
			err := errors.New("Kubernetes version is too low to support extended resources, must be >=1.22.0")
			log.Error(err, "observed version: %s", *version)
			return ctrl.Result{}, err
		}
	}

	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kubemarkMachine.Name,
			Labels:    map[string]string{"app": kubemarkName},
			Namespace: backingCluster.Namespace,
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:    kubemarkName,
					Image:   fmt.Sprintf("%s:%s", r.KubemarkImage, *version),
					Args:    kubemarkArgs,
					Command: []string{"/kubemark"},
					SecurityContext: &v1.SecurityContext{
						Privileged: pointer.Bool(true),
					},
					VolumeMounts: []v1.VolumeMount{
						{
							MountPath: "/kubeconfig",
							Name:      "kubeconfig",
						},
					},
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceCPU:    resource.MustParse("40m"),
							v1.ResourceMemory: resource.MustParse("10240Ki"),
						},
					},
				},
			},
			Tolerations: []v1.Toleration{
				{
					Key:    "node-role.kubernetes.io/master",
					Effect: v1.TaintEffectNoSchedule,
				},
			},
			Volumes: []v1.Volume{
				{
					Name: "kubeconfig",
					VolumeSource: v1.VolumeSource{
						Secret: &v1.SecretVolumeSource{
							SecretName: secret.Name,
						},
					},
				},
			},
		},
	}

	for _, v := range kubemarkMachine.Spec.ExtraMounts {
		for i, c := range pod.Spec.Containers {
			pod.Spec.Containers[i].VolumeMounts = append(
				c.VolumeMounts,
				v1.VolumeMount{
					MountPath: v.ContainerPath,
					Name:      v.Name,
				})
		}

		pod.Spec.Volumes = append(
			pod.Spec.Volumes,
			v1.Volume{
				Name: v.Name,
				VolumeSource: v1.VolumeSource{
					HostPath: &v1.HostPathVolumeSource{
						Path: v.HostPath,
						Type: v.Type,
					},
				},
			})
	}

	if err = backingCluster.Create(ctx, pod); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			log.Error(err, "failed to create pod")
			return ctrl.Result{}, err
		}
	}

	machine.Spec.ProviderID = pointer.String(fmt.Sprintf("kubemark://%s", kubemarkMachine.Name))
	kubemarkMachine.Status.Ready = true

	return ctrl.Result{}, nil
}

func generateCertificateKubeconfig(bootstrapClientConfig *restclient.Config, pemPath string) ([]byte, error) {
	// Get the CA data from the bootstrap client config.
	caFile, caData := bootstrapClientConfig.CAFile, []byte{}
	if len(caFile) == 0 {
		caData = bootstrapClientConfig.CAData
	}

	// Build resulting kubeconfig.
	kubeconfigData := &clientcmdapi.Config{
		// Define a cluster stanza based on the bootstrap kubeconfig.
		Clusters: map[string]*clientcmdapi.Cluster{"default-cluster": {
			Server:                   bootstrapClientConfig.Host,
			InsecureSkipTLSVerify:    bootstrapClientConfig.Insecure,
			CertificateAuthority:     caFile,
			CertificateAuthorityData: caData,
		}},
		// Define auth based on the obtained client cert.
		AuthInfos: map[string]*clientcmdapi.AuthInfo{"default-auth": {
			ClientCertificate: pemPath,
			ClientKey:         pemPath,
		}},
		// Define a context that connects the auth info and cluster, and set it as the default
		Contexts: map[string]*clientcmdapi.Context{"default-context": {
			Cluster:   "default-cluster",
			AuthInfo:  "default-auth",
			Namespace: "default",
		}},
		CurrentContext: "default-context",
	}

	// Marshal to disk
	return runtime.Encode(clientcmdlatest.Codec, kubeconfigData)
}

func getRemoteCluster(ctx context.Context, logger logr.Logger, mgmtClient client.Reader, cluster *clusterv1.Cluster) (*restclient.Config, error) {
	restConfig, err := remote.RESTConfig(ctx, MachineControllerName, mgmtClient, util.ObjectKey(cluster))
	if err != nil {
		logger.Error(err, "error getting restconfig")
		return nil, err
	}
	restConfig.Timeout = 30 * time.Second

	return restConfig, err
}

// getKubemarkExtendedResourcesFlag returns the raw kubemark command line flags for
// `--extended-resources` if they are specified in the spec.
func getKubemarkExtendedResourcesFlag(extendedResources infrav1.KubemarkExtendedResourceList) string {
	if extendedResources == nil {
		return ""
	}

	resources := []string{}
	for k, v := range extendedResources {
		resources = append(resources, fmt.Sprintf("%s=%s", k, v.String()))
	}

	flags := fmt.Sprintf("--extended-resources=%s", strings.Join(resources, ","))
	return flags
}
