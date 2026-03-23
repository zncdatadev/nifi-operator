package cluster

import (
	"context"
	"fmt"

	"github.com/zncdatadev/operator-go/pkg/builder"
	resourceClient "github.com/zncdatadev/operator-go/pkg/client"
	"github.com/zncdatadev/operator-go/pkg/reconciler"
	"github.com/zncdatadev/operator-go/pkg/util"
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
	image := util.NewImage(
		nifiv1alpha1.DefaultProductName,
		version.BuildVersion,
		nifiv1alpha1.DefaultProductVersion,
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

	// Port constants from the node package
	var httpsPort int32 = 9443
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
		httpsPort,
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
		httpsPort,
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
