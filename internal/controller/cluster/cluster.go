package cluster

import (
	"context"

	"github.com/zncdatadev/operator-go/pkg/builder"
	resourceClient "github.com/zncdatadev/operator-go/pkg/client"
	"github.com/zncdatadev/operator-go/pkg/reconciler"
	"github.com/zncdatadev/operator-go/pkg/util"

	nifiv1alpha1 "github.com/zncdatadev/nifi-operator/api/v1alpha1"
	"github.com/zncdatadev/nifi-operator/internal/controller/node"
	"github.com/zncdatadev/nifi-operator/internal/security"
	"github.com/zncdatadev/nifi-operator/internal/version"
)

var _ reconciler.Reconciler = &Reconciler{}

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
	return nil
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
