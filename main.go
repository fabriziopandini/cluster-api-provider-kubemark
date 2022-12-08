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

package main

import (
	"flag"
	"fmt"
	"math/rand"
	"net/http"
	_ "net/http/pprof"
	"os"
	"time"

	infrav1 "github.com/kubernetes-sigs/cluster-api-provider-kubemark/api/v1alpha4"
	"github.com/kubernetes-sigs/cluster-api-provider-kubemark/controllers"
	expinfrav1 "github.com/kubernetes-sigs/cluster-api-provider-kubemark/exp/cluster/api/v1alpha4"
	expclustercontrollers "github.com/kubernetes-sigs/cluster-api-provider-kubemark/exp/cluster/controllers"
	expcontrolplanev1 "github.com/kubernetes-sigs/cluster-api-provider-kubemark/exp/controlplane/api/v1alpha4"
	expcontrolplanecontrollers "github.com/kubernetes-sigs/cluster-api-provider-kubemark/exp/controlplane/controllers"
	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/logs"
	logsv1 "k8s.io/component-base/logs/api/v1"
	_ "k8s.io/component-base/logs/json/register"
	"k8s.io/klog/v2"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	bootstrapv1 "sigs.k8s.io/cluster-api/bootstrap/kubeadm/api/v1beta1"
	"sigs.k8s.io/cluster-api/controllers/remote"
	"sigs.k8s.io/cluster-api/util/flags"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	klog.InitFlags(nil)

	_ = clientgoscheme.AddToScheme(scheme)
	_ = infrav1.AddToScheme(scheme)
	_ = expinfrav1.AddToScheme(scheme)
	_ = expcontrolplanev1.AddToScheme(scheme)
	_ = clusterv1.AddToScheme(scheme)
	_ = bootstrapv1.AddToScheme(scheme)
	// +kubebuilder:scaffold:scheme
}

var (
	metricsBindAddr                         string
	enableLeaderElection                    bool
	leaderElectionLeaseDuration             time.Duration
	leaderElectionRenewDeadline             time.Duration
	leaderElectionRetryPeriod               time.Duration
	watchFilterValue                        string
	watchNamespace                          string
	profilerAddress                         string
	kubemarkMachineConcurrency              int
	kubemarkMachineTemplateConcurrency      int
	kubemarkClusterConcurrency              int
	kubemarkClusterTemplateConcurrency      int
	kubemarkControlPlaneConcurrency         int
	kubemarkControlPlaneTemplateConcurrency int
	syncPeriod                              time.Duration
	webhookPort                             int
	webhookCertDir                          string
	healthAddr                              string
	kubemarkImage                           string
	generateFiles                           bool
	tlsOptions                              = flags.TLSOptions{}
	logOptions                              = logs.NewOptions()
)

// InitFlags initializes the flags.
func InitFlags(fs *pflag.FlagSet) {
	logs.AddFlags(fs, logs.SkipLoggingConfigurationFlags())
	logsv1.AddFlags(logOptions, fs)

	fs.StringVar(&metricsBindAddr, "metrics-bind-addr", "localhost:8080",
		"The address the metric endpoint binds to.")

	fs.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. Enabling this will ensure there is only one active controller manager.")

	fs.DurationVar(&leaderElectionLeaseDuration, "leader-elect-lease-duration", 1*time.Minute,
		"Interval at which non-leader candidates will wait to force acquire leadership (duration string)")

	fs.DurationVar(&leaderElectionRenewDeadline, "leader-elect-renew-deadline", 40*time.Second,
		"Duration that the leading controller manager will retry refreshing leadership before giving up (duration string)")

	fs.DurationVar(&leaderElectionRetryPeriod, "leader-elect-retry-period", 5*time.Second,
		"Duration the LeaderElector clients should wait between tries of actions (duration string)")

	fs.StringVar(&watchNamespace, "namespace", "",
		"Namespace that the controller watches to reconcile cluster-api objects. If unspecified, the controller watches for cluster-api objects across all namespaces.")

	fs.StringVar(&profilerAddress, "profiler-address", "",
		"Bind address to expose the pprof profiler (e.g. localhost:6060)")

	fs.IntVar(&kubemarkMachineConcurrency, "kubemarkmachineconcurrency-concurrency", 10,
		"Number of KubemarkMachine to process simultaneously")

	fs.IntVar(&kubemarkMachineTemplateConcurrency, "kubemarkmachineTemplateconcurrency", 10,
		"Number of KubemarkMachineTemplates to process simultaneously")

	fs.IntVar(&kubemarkClusterConcurrency, "kubemarkcluster-concurrency", 10,
		"Number of KubemarkCluster to process simultaneously")

	fs.IntVar(&kubemarkClusterTemplateConcurrency, "kubemarkclustertemplate-concurrency", 10,
		"Number of KubemarkClusterTemplate to process simultaneously")

	fs.IntVar(&kubemarkControlPlaneConcurrency, "kubemarkcontrolplane-concurrency", 10,
		"Number of KubemarkControlPlane to process simultaneously")

	fs.IntVar(&kubemarkControlPlaneTemplateConcurrency, "kubemarkcontrolplanetemplate-concurrency", 10,
		"Number of KubemarkControlPlaneTemplate to process simultaneously")

	fs.DurationVar(&syncPeriod, "sync-period", 10*time.Minute,
		"The minimum interval at which watched resources are reconciled (e.g. 15m)")

	fs.StringVar(&watchFilterValue, "watch-filter", "",
		fmt.Sprintf("Label value that the controller watches to reconcile cluster-api objects. Label key is always %s. If unspecified, the controller watches for all cluster-api objects.", clusterv1.WatchLabel))

	fs.IntVar(&webhookPort, "webhook-port", 9443,
		"Webhook Server port")

	fs.StringVar(&webhookCertDir, "webhook-cert-dir", "/tmp/k8s-webhook-server/serving-certs/",
		"Webhook cert dir, only used when webhook-port is specified.")

	fs.StringVar(&healthAddr, "health-addr", ":9440",
		"The address the health endpoint binds to.")
	// TODO (elmiko) update the following default image link when we have a home for the kubemark images
	flag.StringVar(&kubemarkImage, "kubemark-image", "quay.io/elmiko/kubemark",
		"The location of the kubemark image")

	flag.BoolVar(&generateFiles, "generate-control-plane-files", false,
		"when this flag is set the manager generates control plane files for the current pod and then exits")

	flags.AddTLSOptions(fs, &tlsOptions)
}

func main() {
	rand.Seed(time.Now().UnixNano())

	InitFlags(pflag.CommandLine)
	pflag.CommandLine.SetNormalizeFunc(cliflag.WordSepNormalizeFunc)
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()

	if err := logsv1.ValidateAndApply(logOptions, nil); err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// klog.Background will automatically use the right logger.
	ctrl.SetLogger(klog.Background())

	// If the generate-control-plane-files flag is set the manager generates control plane files for the current pod and then exits.
	// Note: we are using the manager instead of another binary for convenience (the manager is already built and packaged
	// into an image that is published during the release process).
	if generateFiles {
		doGenerateFiles()
	}

	// Otherwise, run the manager.
	runManger()
}

func doGenerateFiles() {
	c, err := client.New(getConfig(), client.Options{Scheme: scheme})
	if err != nil {
		setupLog.Error(err, "unable to generate client")
		os.Exit(1)
	}

	if err := expcontrolplanecontrollers.GenerateFiles(ctrl.SetupSignalHandler(), setupLog, c); err != nil {
		setupLog.Error(err, "unable to generate control plane files")
		os.Exit(1)
	}
	os.Exit(0)
}

func runManger() {
	if profilerAddress != "" {
		klog.Infof("Profiler listening for requests at %s", profilerAddress)
		go func() {
			klog.Info(http.ListenAndServe(profilerAddress, nil))
		}()
	}

	tlsOptionOverrides, err := flags.GetTLSOptionOverrideFuncs(tlsOptions)
	if err != nil {
		setupLog.Error(err, "unable to add TLS settings to the webhook server")
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(getConfig(), ctrl.Options{
		Scheme:                     scheme,
		MetricsBindAddress:         metricsBindAddr,
		LeaderElection:             enableLeaderElection,
		LeaderElectionID:           "kubemark-manager-leader-election-capi",
		LeaseDuration:              &leaderElectionLeaseDuration,
		RenewDeadline:              &leaderElectionRenewDeadline,
		RetryPeriod:                &leaderElectionRetryPeriod,
		LeaderElectionResourceLock: resourcelock.LeasesResourceLock,
		Namespace:                  watchNamespace,
		SyncPeriod:                 &syncPeriod,
		ClientDisableCacheFor:      []client.Object{
			// TODO: Disable Cache for ConfigMap, Sercrets, Pods and shift to using PartialObjectMetadata for Get/List. Same in BackingClusterTracker.
			// &corev1.ConfigMap{},
			// &corev1.Secret{},
			// &corev1.Pod{},
		},
		Port:                   webhookPort,
		HealthProbeBindAddress: healthAddr,
		CertDir:                webhookCertDir,
		TLSOpts:                tlsOptionOverrides,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Setup the context that's going to be used in controllers and for the manager.
	ctx := ctrl.SetupSignalHandler()

	backingClusterTracker := controllers.NewBackingClusterTracker(mgr)

	if err := (&expclustercontrollers.KubemarkClusterReconciler{
		Client:                mgr.GetClient(),
		BackingClusterTracker: backingClusterTracker,
		WatchFilterValue:      watchFilterValue,
	}).SetupWithManager(ctx, mgr, concurrency(kubemarkClusterConcurrency)); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "KubemarkCluster")
		os.Exit(1)
	}
	if err := (&expcontrolplanecontrollers.KubemarkControlPlaneReconciler{
		Client:                mgr.GetClient(),
		BackingClusterTracker: backingClusterTracker,
		WatchFilterValue:      watchFilterValue,
	}).SetupWithManager(ctx, mgr, concurrency(kubemarkControlPlaneConcurrency)); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "KubemarkControlPlane")
		os.Exit(1)
	}

	if err := (&controllers.KubemarkMachineReconciler{
		Client:                mgr.GetClient(),
		BackingClusterTracker: backingClusterTracker,
		// TODO: move to contextual logging
		Log:              ctrl.Log.WithName("controllers").WithName("KubemarkMachine"),
		Scheme:           mgr.GetScheme(),
		KubemarkImage:    kubemarkImage,
		WatchFilterValue: watchFilterValue,
	}).SetupWithManager(ctx, mgr, concurrency(kubemarkMachineConcurrency)); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "KubemarkMachine")
		os.Exit(1)
	}
	if err := (&controllers.KubemarkMachineTemplateReconciler{
		Client: mgr.GetClient(),
		// TODO: move to contextual logging
		Log:              ctrl.Log.WithName("controllers").WithName("KubemarkMachineTemplate"),
		Scheme:           mgr.GetScheme(),
		WatchFilterValue: watchFilterValue,
	}).SetupWithManager(ctx, mgr, concurrency(kubemarkMachineTemplateConcurrency)); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "KubemarkMachineTemplate")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	setupLog.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func getConfig() *rest.Config {
	restConfig := ctrl.GetConfigOrDie()
	restConfig.UserAgent = remote.DefaultClusterAPIUserAgent("cluster-api-kubemark-manager")
	return restConfig
}

func concurrency(c int) controller.Options {
	return controller.Options{MaxConcurrentReconciles: c}
}
