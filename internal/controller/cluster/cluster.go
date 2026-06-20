package cluster

import (
	"context"
	"fmt"
	"strings"

	"github.com/zncdatadev/operator-go/pkg/builder"
	resourceClient "github.com/zncdatadev/operator-go/pkg/client"
	"github.com/zncdatadev/operator-go/pkg/reconciler"
	"github.com/zncdatadev/operator-go/pkg/util"
	rbacv1 "k8s.io/api/rbac/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	nifiv1alpha1 "github.com/zncdatadev/nifi-operator/api/v1alpha1"
	"github.com/zncdatadev/nifi-operator/internal/common/security"
	"github.com/zncdatadev/nifi-operator/internal/controller/node"
	reportingtask "github.com/zncdatadev/nifi-operator/internal/controller/reporting_task"
	"github.com/zncdatadev/nifi-operator/internal/version"
)

var (
	logger = ctrl.Log.WithName("cluster")

	_ reconciler.Reconciler = &Reconciler{}
)

type Reconciler struct {
	reconciler.BaseCluster[*nifiv1alpha1.NifiClusterSpec]
	ClusterConfig *nifiv1alpha1.ClusterConfigSpec
}

func NewReconciler(
	client *resourceClient.Client,
	clusterInfo reconciler.ClusterInfo,
	spec *nifiv1alpha1.NifiClusterSpec,
) *Reconciler {

	return &Reconciler{
		BaseCluster: *reconciler.NewBaseCluster(
			client,
			clusterInfo,
			spec.ClusterOperation,
			spec,
		),
		ClusterConfig: spec.ClusterConfig,
	}

}

func (r *Reconciler) GetImage() *util.Image {
	productVersion := nifiv1alpha1.DefaultProductVersion
	if r.Spec.Image.ProductVersion != "" {
		productVersion = r.Spec.Image.ProductVersion
	}

	image := util.NewImage(
		nifiv1alpha1.DefaultProductName,
		version.BuildVersion,
		productVersion,
		func(options *util.ImageOptions) {
			options.Custom = r.Spec.Image.Custom
			options.Repo = r.Spec.Image.Repo
			options.PullPolicy = *r.Spec.Image.PullPolicy
		},
	)

	if r.Spec.Image.KubedoopVersion != "" {
		image.KubedoopVersion = r.Spec.Image.KubedoopVersion
	}

	return image
}

func (r *Reconciler) RegisterResources(ctx context.Context) error {

	// Register RBAC resources (ServiceAccount + Role + RoleBinding) for NiFi pods.
	// Required for KubernetesLeaderElectionManager (leases) and
	// KubernetesConfigMapStateProvider (configmaps) in Kubernetes-native clustering mode.
	if err := r.registerRBACResources(); err != nil {
		return err
	}

	node := node.NewReconciler(
		r.Client,
		r.IsStopped(),
		r.ClusterConfig,
		reconciler.RoleInfo{
			ClusterInfo: r.ClusterInfo,
			RoleName:    "node",
		},
		r.GetImage(),
		r.Spec.Nodes,
	)

	if err := node.RegisterResources(ctx); err != nil {
		return err
	}

	r.AddResource(node)

	r.AddResource(r.sensitiveKeyReconciler())

	// Register reporting task resources (Service + Job) if enabled
	if err := r.registerReportingTaskResources(ctx); err != nil {
		return err
	}

	return nil
}

func (r *Reconciler) registerReportingTaskResources(ctx context.Context) error {
	if r.ClusterConfig.CreateReportingTaskJob == nil || !r.ClusterConfig.CreateReportingTaskJob.Enable {
		logger.Info("Reporting task job is disabled, skipping")
		return nil
	}

	clusterName := r.ClusterInfo.GetClusterName()
	image := r.GetImage()

	// NiFi 2.x+ serves Prometheus metrics natively at /nifi-api/flow/metrics/prometheus.
	// Only install the PrometheusReportingTask Job + Service for NiFi 1.x,
	// consistent with the Stackable Rust operator's build_maybe_reporting_task().
	if !strings.HasPrefix(image.ProductVersion, "1.") {
		logger.Info("NiFi 2.x+ detected, skipping reporting task Job/Service - metrics served natively",
			"productVersion", image.ProductVersion)
		return nil
	}

	// Resolve authentication for the reporting task job
	auth, err := r.getAuthentication(ctx)
	if err != nil {
		return fmt.Errorf("failed to get authentication for reporting task: %w", err)
	}

	// Determine TLS configuration
	tlsEnabled := r.ClusterConfig.Tls != nil
	var tlsSecretClass string
	if tlsEnabled {
		tlsSecretClass = r.ClusterConfig.Tls.ServerSecretClass
	}

	// Port constants from the node package.
	// Select the web port based on TLS: use HTTPS (9443) when TLS is enabled,
	// HTTP (8088) otherwise. This matches node.Ports definitions.
	var webPort int32
	if tlsEnabled {
		webPort = node.GetPort("https")
	} else {
		webPort = node.GetPort("http")
	}
	var metricsPort int32 = 8081

	options := func(o *builder.Options) {
		o.ClusterName = clusterName
		o.Labels = r.ClusterInfo.GetLabels()
		o.Annotations = r.ClusterInfo.GetAnnotations()
	}

	// Create the reporting task service
	svcReconciler, err := reportingtask.NewReportingTaskServiceReconciler(
		r.Client,
		clusterName,
		r.Spec.Nodes,
		webPort,
		options,
	)
	if err != nil {
		return fmt.Errorf("failed to create reporting task service reconciler: %w", err)
	}
	r.AddResource(svcReconciler)

	// Create the reporting task job
	jobReconciler := reportingtask.NewReportingTaskJobReconciler(
		r.Client,
		clusterName,
		image,
		webPort,
		metricsPort,
		auth,
		tlsEnabled,
		tlsSecretClass,
		options,
	)
	r.AddResource(jobReconciler)

	logger.Info("Registered reporting task resources", "cluster", clusterName)
	return nil
}

// registerRBACResources creates the ServiceAccount, Role, and RoleBinding that
// NiFi pods need in Kubernetes-native clustering mode (no ZooKeeper):
//   - leases (coordination.k8s.io): required by KubernetesLeaderElectionManager
//   - configmaps: required by KubernetesConfigMapStateProvider
//
// TODO: skip this when ZooKeeper is configured (ZK-mode doesn't need these).
func (r *Reconciler) registerRBACResources() error {
	clusterName := r.ClusterInfo.GetClusterName()
	saName := node.NifiServiceAccountName(clusterName)
	roleName := clusterName + "-nifi"
	roleBindingName := clusterName + "-nifi"

	options := func(o *builder.Options) {
		o.ClusterName = clusterName
		o.Labels = r.ClusterInfo.GetLabels()
		o.Annotations = r.ClusterInfo.GetAnnotations()
	}

	// ServiceAccount
	saBuilder := builder.NewGenericServiceAccountBuilder(r.Client, saName, options)
	saReconciler := reconciler.NewGenericResourceReconciler(r.Client, saBuilder)
	r.AddResource(saReconciler)

	// Role: allow NiFi pods to manage leases and configmaps in their own namespace
	roleBuilder := builder.NewGenericRoleBuilder(r.Client, roleName, options)
	roleBuilder.AddPolicyRules([]rbacv1.PolicyRule{
		{
			// KubernetesLeaderElectionManager needs leases for leader election
			APIGroups: []string{"coordination.k8s.io"},
			Resources: []string{"leases"},
			Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
		},
		{
			// KubernetesConfigMapStateProvider stores cluster state in ConfigMaps
			APIGroups: []string{""},
			Resources: []string{"configmaps"},
			Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
		},
	})
	roleReconciler := reconciler.NewGenericResourceReconciler(r.Client, roleBuilder)
	r.AddResource(roleReconciler)

	// RoleBinding: bind the Role to the NiFi ServiceAccount
	rbBuilder := builder.NewGenericRoleBindingBuilder(r.Client, roleBindingName, options)
	rbBuilder.AddSubject(saName)
	rbBuilder.SetRoleRef(roleName, false)
	rbReconciler := reconciler.NewGenericResourceReconciler(r.Client, rbBuilder)
	r.AddResource(rbReconciler)

	return nil
}

func (r *Reconciler) getAuthentication(ctx context.Context) (*security.Authentication, error) {
	if r.ClusterConfig == nil || r.ClusterConfig.Authentication == nil {
		return nil, nil
	}

	auth, err := security.NewAuthentication(
		ctx,
		r.Client,
		r.ClusterInfo.GetClusterName(),
		r.ClusterConfig.Authentication,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create authentication: %w", err)
	}

	return auth, nil
}

func (r *Reconciler) sensitiveKeyReconciler() reconciler.Reconciler {

	sensitiveConfig := r.ClusterConfig.SensitiveProperties

	sensitiveKeyReconciler := security.NewSensitiveKeyReconciler(
		r.Client,
		sensitiveConfig.KeySecret,
		sensitiveConfig.AutoGenerate,
		func(o *builder.Options) {
			o.ClusterName = r.ClusterInfo.GetClusterName()
			o.Labels = r.ClusterInfo.GetLabels()
			o.Annotations = r.ClusterInfo.GetAnnotations()
		},
	)

	return sensitiveKeyReconciler
}
