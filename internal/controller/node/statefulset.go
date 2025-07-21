package node

import (
	"context"
	"fmt"
	"path"

	"github.com/zncdatadev/operator-go/pkg/builder"
	"github.com/zncdatadev/operator-go/pkg/client"
	"github.com/zncdatadev/operator-go/pkg/constants"
	"github.com/zncdatadev/operator-go/pkg/reconciler"
	"github.com/zncdatadev/operator-go/pkg/util"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	nifiv1alpha1 "github.com/zncdatadev/nifi-operator/api/v1alpha1"
	"github.com/zncdatadev/nifi-operator/internal/common/security"
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
				o.RoleGroupName = roleGroupInfo.RoleGroupName
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
	mainContainerBuilder, err := b.getMainContainerBuilder()
	if err != nil {
		return nil, fmt.Errorf("failed to get main container: %w", err)
	}
	mainContainer := mainContainerBuilder.Build()
	b.AddContainer(mainContainer)

	volumes, err := b.getVolumes()
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
	nodeAddress := fmt.Sprintf("$POD_NAME.%s.%s.svc.cluster.local", b.Name, b.Client.GetOwnerNamespace())

	authArgs := ""
	if b.Authentication != nil {
		authArgs = b.Authentication.GetInitArgs()
	}

	args := `
cp ` + path.Join(constants.KubedoopConfigDirMount, "*") + ` ` + NifiConfigDir + `

export NODE_ADDRESS="` + nodeAddress + `"
` + authArgs + `

gomplate -f ` + constants.KubedoopConfigDirMount + `/nifi.properties -o ` + NifiConfigDir + `/nifi.properties
gomplate -f ` + constants.KubedoopConfigDirMount + `/login-identity-providers.xml -o ` + NifiConfigDir + `/login-identity-providers.xml
gomplate -f ` + constants.KubedoopConfigDirMount + `/state-management.xml -o ` + NifiConfigDir + `/state-management.xml
`

	return util.IndentTab4Spaces(args), nil
}

func (b *StatefulSetBuilder) getMainContainerBuilder() (builder.ContainerBuilder, error) {
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
	b.setupMainContainerProbe(container)

	return container, nil
}

func (b *StatefulSetBuilder) setupMainContainerProbe(container builder.ContainerBuilder) {
	port := intstr.FromString("http")

	if b.ClusterConfig.Tls != nil {
		port = intstr.FromString("https")
	}

	container.SetLivenessProbe(&corev1.Probe{
		FailureThreshold:    30,
		InitialDelaySeconds: 10,
		PeriodSeconds:       10,
		SuccessThreshold:    1,
		TimeoutSeconds:      3,
		ProbeHandler: corev1.ProbeHandler{
			TCPSocket: &corev1.TCPSocketAction{
				Port: port,
			},
		},
	})

	container.SetStartupProbe(&corev1.Probe{
		FailureThreshold:    120,
		InitialDelaySeconds: 10,
		PeriodSeconds:       10,
		SuccessThreshold:    1,
		TimeoutSeconds:      3,
		ProbeHandler: corev1.ProbeHandler{
			TCPSocket: &corev1.TCPSocketAction{
				Port: port,
			},
		},
	})
}

func (b *StatefulSetBuilder) getMainContainerArgs() (string, error) {
	args := util.CommonBashTrapFunctions + `

` + util.RemoveVectorShutdownFileCommand() + `

prepare_signal_handlers

# sleep infinity

bin/nifi.sh run &

wait_for_termination $!

` + util.CreateVectorShutdownFileCommand() + `
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
	}

	if b.ClusterConfig.ZookeeperConfigMapName != nil && *b.ClusterConfig.ZookeeperConfigMapName != "" {
		envVars = append(envVars, corev1.EnvVar{
			Name: "ZOOKEEPER_HOSTS",
			ValueFrom: &corev1.EnvVarSource{
				ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
					Key: "ZOOKEEPER_HOSTS",
					LocalObjectReference: corev1.LocalObjectReference{
						Name: *b.ClusterConfig.ZookeeperConfigMapName,
					},
				},
			},
		})
		envVars = append(envVars, corev1.EnvVar{
			Name: "ZOOKEEPER_CHROOT",
			ValueFrom: &corev1.EnvVarSource{
				ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
					Key: "ZOOKEEPER_CHROOT",
					LocalObjectReference: corev1.LocalObjectReference{
						Name: *b.ClusterConfig.ZookeeperConfigMapName,
					},
				},
			},
		})
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

func (b *StatefulSetBuilder) getVolumes() ([]corev1.Volume, error) {
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
