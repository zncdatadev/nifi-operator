package node

import (
	"context"
	"fmt"

	commonsv1alpha1 "github.com/zncdatadev/operator-go/pkg/apis/commons/v1alpha1"
	"github.com/zncdatadev/operator-go/pkg/builder"
	resourceClient "github.com/zncdatadev/operator-go/pkg/client"
	"github.com/zncdatadev/operator-go/pkg/constants"
	"github.com/zncdatadev/operator-go/pkg/reconciler"
	"github.com/zncdatadev/operator-go/pkg/util"

	nifiv1alpha1 "github.com/zncdatadev/nifi-operator/api/v1alpha1"
	"github.com/zncdatadev/nifi-operator/internal/common/security"
)

var _ reconciler.RoleReconciler = &Reconciler{}

type Reconciler struct {
	reconciler.BaseRoleReconciler[*nifiv1alpha1.NodesSpec]
	ClusterConfig *nifiv1alpha1.ClusterConfigSpec
	Image         *util.Image
}

func NewReconciler(
	client *resourceClient.Client,
	clusterStopped bool,
	clusterConfig *nifiv1alpha1.ClusterConfigSpec,
	roleInfo reconciler.RoleInfo,
	image *util.Image,
	spec *nifiv1alpha1.NodesSpec,
) *Reconciler {
	return &Reconciler{
		BaseRoleReconciler: *reconciler.NewBaseRoleReconciler(
			client,
			clusterStopped,
			roleInfo,
			spec,
		),
		ClusterConfig: clusterConfig,
		Image:         image,
	}
}

func (r *Reconciler) RegisterResources(ctx context.Context) error {
	for name, rg := range r.Spec.RoleGroups {

		mergedConfig, err := util.MergeObject(r.Spec.Config, rg.Config)
		if err != nil {
			return err
		}
		overrides, err := util.MergeObject(r.Spec.OverridesSpec, rg.OverridesSpec)
		if err != nil {
			return err
		}

		info := reconciler.RoleGroupInfo{
			RoleInfo:      r.RoleInfo,
			RoleGroupName: name,
		}

		reconcilers, err := r.RegisterResourceWithRoleGroup(
			ctx,
			rg.Replicas,
			info,
			overrides,
			mergedConfig,
		)

		if err != nil {
			return err
		}

		for _, reconciler := range reconcilers {
			r.AddResource(reconciler)
		}
	}
	return nil
}

func (r *Reconciler) RegisterResourceWithRoleGroup(
	ctx context.Context,
	replicas *int32,
	info reconciler.RoleGroupInfo,
	overrides *commonsv1alpha1.OverridesSpec,
	roleGroupConfig *nifiv1alpha1.ConfigSpec,
) ([]reconciler.Reconciler, error) {
	reconcilers := make([]reconciler.Reconciler, 0)

	options := func(o *builder.Options) {
		o.ClusterName = info.GetClusterName()
		o.RoleName = info.GetRoleName()
		o.RoleGroupName = info.GetGroupName()

		o.Labels = info.GetLabels()
		o.Annotations = info.GetAnnotations()
	}
	auth, err := r.getAuthentrication(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get authentication: %w", err)
	}

	configmapReconciler := NewConfigReconciler(
		r.Client,
		r.ClusterConfig,
		info,
		roleGroupConfig,
		auth,
	)

	stsReconciler, err := NewStatefulSetReconciler(
		r.Client,
		info,
		r.ClusterConfig,
		Ports,
		r.Image,
		replicas,
		r.ClusterStopped(),
		auth,
		overrides,
		roleGroupConfig,
	)
	if err != nil {
		return nil, err
	}

	serviceReconciler := NewServiceReconciler(
		r.Client,
		info.GetFullName(),
		Ports,
		func(o *builder.ServiceBuilderOptions) {
			o.ListenerClass = constants.ExternalUnstable
			o.Headless = true
			o.ClusterName = info.GetClusterName()
			o.RoleName = info.GetRoleName()
			o.RoleGroupName = info.GetGroupName()
			o.Labels = info.GetLabels()
			o.Annotations = info.GetAnnotations()
		},
	)

	reconcilers = append(reconcilers, configmapReconciler, stsReconciler, serviceReconciler)

	for key := range auth.Authenticators {
		if key == security.AuthenticatorTypeLDAP {
			adminSecretReconciler := security.NewAdminSecretReconciler(
				r.Client,
				r.RoleInfo.GetClusterName(),
				options,
			)

			reconcilers = append(reconcilers, adminSecretReconciler)
		}
	}

	return reconcilers, nil
}

func (b *Reconciler) getAuthentrication(ctx context.Context) (*security.Authentication, error) {
	if b.ClusterConfig == nil || b.ClusterConfig.Authentication == nil {
		return nil, nil
	}

	auth, err := security.NewAuthentication(
		ctx,
		b.Client,
		b.RoleInfo.GetClusterName(),
		b.ClusterConfig.Authentication,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create authentication: %w", err)
	}

	return auth, nil
}
