package node

import (
	"context"
	"fmt"

	nifiv1alpha1 "github.com/zncdatadev/nifi-operator/api/v1alpha1"
	"github.com/zncdatadev/nifi-operator/internal/security"
	"github.com/zncdatadev/operator-go/pkg/builder"
	"github.com/zncdatadev/operator-go/pkg/client"
	"github.com/zncdatadev/operator-go/pkg/constants"
	"github.com/zncdatadev/operator-go/pkg/reconciler"
	"github.com/zncdatadev/operator-go/pkg/util"
	corev1 "k8s.io/api/core/v1"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	commonsv1alpha1 "github.com/zncdatadev/operator-go/pkg/apis/commons/v1alpha1"
)

const (
	NifiConfigVolumeName        = "nifi-config"
	NifiAdminPasswordVolumeName = "nifi-admin-password"
	EmptyDirVolumeName          = "empty-dir"
)

var _ builder.StatefulSetBuilder = &StatefulSetBuilder{}

type StatefulSetBuilder struct {
	builder.StatefulSet
	Ports          []corev1.ContainerPort
	ClusterConfig  *nifiv1alpha1.ClusterConfigSpec
	ClusterName    string
	RoleName       string
	Authentication *security.Authentication
}

func NewStatefulSetReconciler(
	client *client.Client,
	roleGroupInfo reconciler.RoleGroupInfo,
	clusterConfig *nifiv1alpha1.ClusterConfigSpec,
	ports []corev1.ContainerPort,
	image *util.Image,
	replicas *int32,
	stopped bool,
	authentication *security.Authentication,
	overrides *commonsv1alpha1.OverridesSpec,
	roleGroupConfig *nifiv1alpha1.ConfigSpec,
) (*reconciler.StatefulSet, error) {

	var commonsRoleGroupConfig *commonsv1alpha1.RoleGroupConfigSpec
	if roleGroupConfig != nil {
		commonsRoleGroupConfig = roleGroupConfig.RoleGroupConfigSpec
	}

	stsBuilder := &StatefulSetBuilder{
		StatefulSet: *builder.NewStatefulSetBuilder(
			client,
			roleGroupInfo.GetFullName(),
			replicas,
			image,
			overrides,
			commonsRoleGroupConfig,
			func(o *builder.Options) {
				o.ClusterName = roleGroupInfo.GetClusterName()
				o.RoleName = roleGroupInfo.GetRoleName()
				o.Labels = roleGroupInfo.GetLabels()
				o.Annotations = roleGroupInfo.GetAnnotations()
			},
		),
		ClusterConfig:  clusterConfig,
		Ports:          ports,
		ClusterName:    roleGroupInfo.GetClusterName(),
		RoleName:       roleGroupInfo.GetRoleName(),
		Authentication: authentication,
	}

	return reconciler.NewStatefulSet(
		client,
		stsBuilder,
		stopped,
	), nil
}

func (b *StatefulSetBuilder) Build(ctx context.Context) (ctrlclient.Object, error) {

	prepareContainer, err := b.getPrepareContainer()
	if err != nil {
		return nil, fmt.Errorf("failed to get prepare container: %w", err)
	}
	b.AddInitContainer(prepareContainer.Build())
	mainContainer, err := b.getMainContainer()
	if err != nil {
		return nil, fmt.Errorf("failed to get main container: %w", err)
	}
	b.AddContainer(mainContainer.Build())

	volumes, err := b.getVolumes(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get volumes: %w", err)
	}
	b.AddVolumes(volumes)

	return b.StatefulSet.Build(ctx)
}

func (b *StatefulSetBuilder) getContainerTemplate(name string) (builder.ContainerBuilder, error) {
	container := builder.NewContainerBuilder(name, b.Image)
	container.SetSecurityContext(0, 0, false)
	container.SetCommand([]string{"/bin/bash", "-x", "-euo", "pipefail", "-c"})

	envVars, err := b.getContainerEnv()
	if err != nil {
		return nil, fmt.Errorf("failed to get container environment variables: %w", err)
	}
	container.AddEnvVars(envVars)
	volumeMounts, err := b.getVolumeMounts()
	if err != nil {
		return nil, fmt.Errorf("failed to get volume mounts: %w", err)
	}
	container.AddVolumeMounts(volumeMounts)
	return container, nil
}

func (b *StatefulSetBuilder) getPrepareContainer() (builder.ContainerBuilder, error) {
	container, err := b.getContainerTemplate("prepare")
	if err != nil {
		return nil, fmt.Errorf("failed to get base container: %w", err)
	}

	args, err := b.getPrepareContainerArgs()
	if err != nil {
		return nil, fmt.Errorf("failed to get prepare container args: %w", err)
	}

	container.SetArgs([]string{args})

	return container, nil
}

func (b *StatefulSetBuilder) getPrepareContainerArgs() (string, error) {
	nodeAddress := fmt.Sprintf("$POD_NAME.%s.%s.svc.cluster.local", b.RoleGroupName, b.Client.GetOwnerNamespace())

	args := `
microdnf install -y gettext

cp -r ` + constants.KubedoopConfigDirMount + ` ` + NifiConfigDir + `

export NODE_ADDRESS="` + nodeAddress + `"

envsubst < ` + constants.KubedoopConfigDirMount + `/nifi.properties > ` + NifiConfigDir + `/nifi.properties
envsubst < ` + constants.KubedoopConfigDirMount + `/login-identity-providers.xml > ` + NifiConfigDir + `/login-identity-providers.xml
envsubst < ` + constants.KubedoopConfigDirMount + `/state-management.xml > ` + NifiConfigDir + `/state-management.xml
`

	return util.IndentTab4Spaces(args), nil
}

func (b *StatefulSetBuilder) getMainContainer() (builder.ContainerBuilder, error) {
	container, err := b.getContainerTemplate(b.RoleName)
	if err != nil {
		return nil, fmt.Errorf("failed to get base container: %w", err)
	}

	args, err := b.getMainContainerArgs()
	if err != nil {
		return nil, fmt.Errorf("failed to get main container args: %w", err)
	}
	container.SetArgs([]string{args})
	container.AddPorts(Ports)

	// TODO: set container resource
	return container, nil
}

func (b *StatefulSetBuilder) getMainContainerArgs() (string, error) {
	args := `
prepare_signal_handlers()
{
    unset term_child_pid
    unset term_kill_needed
    trap 'handle_term_signal' TERM
}

handle_term_signal()
{
    if [ "${term_child_pid}" ]; then
        kill -TERM "${term_child_pid}" 2>/dev/null
    else
        term_kill_needed="yes"
    fi
}

wait_for_termination()
{
    set +e
    term_child_pid=$1
    if [[ -v term_kill_needed ]]; then
        kill -TERM "${term_child_pid}" 2>/dev/null
    fi
    wait ${term_child_pid} 2>/dev/null
    trap - TERM
    wait ${term_child_pid} 2>/dev/null
    set -e
}

rm -rf ` + builder.VectorShutdownFile + `

prepare_signal_handlers

bin/nifi.sh run &

wait_for_termination $!

mkdir -p ` + builder.VectorWatcherDir + ` && touch ` + builder.VectorShutdownFile + `
`

	return util.IndentTab4Spaces(args), nil
}

func (b *StatefulSetBuilder) getContainerEnv() ([]corev1.EnvVar, error) {

	envVars := []corev1.EnvVar{
		{
			Name: "POD_NAME",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.name",
				},
			},
		},
		{
			Name: "ZOOKEEPER_HOSTS",
			ValueFrom: &corev1.EnvVarSource{
				ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
					Key: "ZOOKEEPER_HOSTS",
					LocalObjectReference: corev1.LocalObjectReference{
						Name: b.ClusterConfig.ZookeeperConfigMapName,
					},
				},
			},
		},
		{
			Name: "ZOOKEEPER_CHROOT",
			ValueFrom: &corev1.EnvVarSource{
				ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
					Key: "ZOOKEEPER_CHROOT",
					LocalObjectReference: corev1.LocalObjectReference{
						Name: b.ClusterConfig.ZookeeperConfigMapName,
					},
				},
			},
		},
	}

	if b.Authentication != nil {
		envVars = append(envVars, b.Authentication.GetEnvVars()...)
	}

	return envVars, nil
}

func (b *StatefulSetBuilder) getVolumeMounts() ([]corev1.VolumeMount, error) {
	volumeMounts := []corev1.VolumeMount{
		{
			Name:      NifiConfigVolumeName,
			MountPath: constants.KubedoopConfigDirMount,
			ReadOnly:  true,
		},
		{
			Name:      EmptyDirVolumeName,
			MountPath: NifiConfigDir,
			SubPath:   "config",
			ReadOnly:  false,
		},
	}

	if b.Authentication != nil {
		volumeMounts = append(volumeMounts, b.Authentication.GetVolumeMounts()...)
	}

	return volumeMounts, nil
}

func (b *StatefulSetBuilder) getVolumes(ctx context.Context) ([]corev1.Volume, error) {
	volumes := []corev1.Volume{
		{
			Name: NifiConfigVolumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: b.GetName(),
					},
				},
			},
		},
		{
			Name: EmptyDirVolumeName,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	}
	if b.Authentication != nil {
		volumes = append(volumes, b.Authentication.GetVolumes()...)
	}

	return volumes, nil
}
