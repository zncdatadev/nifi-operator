/*
Copyright 2025 ZNCDataDev.

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

package controller

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"

	commonsv1alpha1 "github.com/zncdatadev/operator-go/pkg/apis/commons/v1alpha1"
	"github.com/zncdatadev/operator-go/pkg/config"
	"github.com/zncdatadev/operator-go/pkg/constant"
	"github.com/zncdatadev/operator-go/pkg/listener"
	"github.com/zncdatadev/operator-go/pkg/reconciler"
	"github.com/zncdatadev/operator-go/pkg/sidecar"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	nifiv1alpha1 "github.com/zncdatadev/nifi-operator/api/v1alpha1"
	"github.com/zncdatadev/nifi-operator/internal/gitsync"
	"github.com/zncdatadev/nifi-operator/internal/product"
	"github.com/zncdatadev/nifi-operator/internal/security"
	nifiutil "github.com/zncdatadev/nifi-operator/internal/util"
	"github.com/zncdatadev/nifi-operator/internal/version"
)

// Compile-time proof that NifiCluster wires framework-owned vector.yaml
// generation (inert until a role group enables the Vector agent).
var _ reconciler.VectorAggregatorProvider = (*nifiv1alpha1.NifiCluster)(nil)

// RBAC for the resources the SDK GenericReconciler owns on behalf of a NifiCluster,
// plus the per-CR Role/RoleBinding the RBAC extension creates for NiFi pods.
// Regenerate config/rbac/role.yaml with `make manifests` after editing.
//
// +kubebuilder:rbac:groups=nifi.kubedoop.dev,resources=nificlusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=nifi.kubedoop.dev,resources=nificlusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nifi.kubedoop.dev,resources=nificlusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=authentication.kubedoop.dev,resources=authenticationclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services;configmaps;serviceaccounts;secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

const (
	// MainContainerName is the primary container name; it equals the Gen 2 role
	// name so the rendered container set is unchanged.
	MainContainerName = nifiv1alpha1.RoleName

	// EmptyDirVolumeName backs NiFi's writable conf directory.
	EmptyDirVolumeName = "empty-dir"
)

// NifiServiceAccountName returns the per-CR ServiceAccount name for NiFi pods
// (unchanged from Gen 2: "<cluster>-nifi").
func NifiServiceAccountName(clusterName string) string {
	return clusterName + "-nifi"
}

// NifiRoleGroupHandler builds NiFi role group resources. The embedded
// BaseRoleGroupHandler owns the skeleton (ConfigMap from the merged config,
// headless + client Services, StatefulSet, role-level PDB); the override below
// adds the NiFi-specific parts: authentication wiring, the gomplate prepare
// init container, git-sync containers, and the document-style XML config files
// that cannot flow through the key-value merge pipeline.
type NifiRoleGroupHandler struct {
	*reconciler.BaseRoleGroupHandler[*nifiv1alpha1.NifiCluster]
}

// NewNifiRoleGroupHandler creates the handler and configures the
// reconcile-invariant framework defaults. Everything that depends on the CR
// (TLS-dependent probes, listener class, auth volumes) rides the per-call
// RoleGroupBuildContext instead.
func NewNifiRoleGroupHandler(scheme *runtime.Scheme) *NifiRoleGroupHandler {
	base := reconciler.NewBaseRoleGroupHandler[*nifiv1alpha1.NifiCluster](defaultImage(), scheme)

	// Opting into the product name lets the framework resolve spec.image every
	// reconcile; an operator upgrade moves clusters onto the co-released image.
	base.ProductName = nifiv1alpha1.DefaultProductName
	base.ImageDefaults = commonsv1alpha1.ImageSpec{
		Repo:            nifiv1alpha1.DefaultRepository,
		ProductVersion:  nifiv1alpha1.DefaultProductVersion,
		KubedoopVersion: version.BuildVersion,
	}

	base.MainContainerName = MainContainerName

	// nifi.properties and bootstrap.conf are plain sorted key=value files whose
	// bytes (and embedded gomplate templates) must not be Java-escaped, so both
	// extensions map to the vendored plain marshaler. Default formats stay
	// registered for any other file a user overrides.
	base.ConfigGenerator = config.NewMultiFormatConfigGenerator()
	base.ConfigGenerator.RegisterDefaultFormats()
	base.ConfigGenerator.RegisterFormat(".properties", nifiutil.PlainPropertiesMarshaler{})
	base.ConfigGenerator.RegisterFormat(".conf", nifiutil.PlainPropertiesMarshaler{})

	// Gen 2 ran NiFi with this exact container security context and no
	// pod-level context. NiFi writes its repositories into the container
	// filesystem under /kubedoop/data, so adopting the framework's canonical
	// non-root context is a behavior change that needs its own verified change,
	// not a migration side effect.
	base.WithSecurityContext(&corev1.SecurityContext{
		RunAsUser:                ptrTo(int64(0)),
		RunAsGroup:               ptrTo(int64(0)),
		AllowPrivilegeEscalation: ptrTo(false),
	}, nil)

	base.SetRoleContainerPorts(nifiv1alpha1.RoleName, product.Ports)
	base.SetRoleServicePorts(nifiv1alpha1.RoleName, servicePorts())

	// Peers must resolve each other's DNS before readiness for cluster
	// formation (the headless Service is new in Gen 3; Gen 2 intended but never
	// achieved per-pod DNS).
	base.PublishNotReadyAddresses = true

	return &NifiRoleGroupHandler{BaseRoleGroupHandler: base}
}

// BuildResources delegates the skeleton to the framework, then applies the
// NiFi-specific pieces.
func (h *NifiRoleGroupHandler) BuildResources(
	ctx context.Context,
	k8sClient client.Client,
	cr *nifiv1alpha1.NifiCluster,
	buildCtx *reconciler.RoleGroupBuildContext,
) (*reconciler.RoleGroupResources, error) {
	clusterConfig := cr.Spec.ClusterConfig
	if clusterConfig == nil {
		return nil, fmt.Errorf("spec.clusterConfig must not be nil")
	}

	// Resolve authentication once per build (Gen 2 resolved it twice per
	// reconcile). nil when the CR declares none.
	var auth *security.Authentication
	if clusterConfig.Authentication != nil {
		var err error
		auth, err = security.NewAuthentication(ctx, k8sClient, cr.GetName(), clusterConfig.Authentication)
		if err != nil {
			return nil, fmt.Errorf("resolving authentication: %w", err)
		}
	}

	gitSync, err := gitsync.NewGitSyncResources(clusterConfig.CustomComponentsGitSync)
	if err != nil {
		return nil, fmt.Errorf("building git-sync resources: %w", err)
	}

	// Resolve the image once and declare it on the context, so the main
	// container, the prepare init container and the sidecar propagation all
	// agree on the same reference.
	image, err := cr.GetSpec().Image.ResolveImage(h.ProductName, h.ImageDefaults)
	if err != nil {
		return nil, fmt.Errorf("resolving image: %w", err)
	}
	buildCtx.Image = image

	// Init containers ride the framework's sidecar channel (one-shot static
	// providers, injected in name order within the default phase — the Gen 2
	// rendered order). Direct pod mutation would race the Vector provider's
	// phase ordering.
	enabled := &sidecar.SidecarConfig{Enabled: true}
	buildCtx.SidecarManager.Register(
		sidecar.NewStaticContainerProvider(h.prepareContainer(cr, buildCtx, auth, image)), enabled)
	for i := range gitSync.GitSyncInitContainers {
		buildCtx.SidecarManager.Register(
			sidecar.NewStaticContainerProvider(gitSync.GitSyncInitContainers[i]), enabled)
	}

	// Per-CR intent, declared before Build.
	buildCtx.ListenerClass = listenerClassFor(clusterConfig)
	cliArgs := buildCtx.MergedConfig.CliArgs
	buildCtx.MainContainerCustomizer = func(c *corev1.Container) error {
		h.customizeMainContainer(c, cr, auth, gitSync, cliArgs)
		return nil
	}

	resources, err := h.BaseRoleGroupHandler.BuildResources(ctx, k8sClient, cr, buildCtx)
	if err != nil {
		return nil, err
	}

	// Document-style XML config files bypass the key-value merge pipeline; a
	// user override of the same key (whole file via configOverrides) wins.
	if resources.ConfigMap != nil {
		if resources.ConfigMap.Data == nil {
			resources.ConfigMap.Data = map[string]string{}
		}
		if auth != nil {
			setIfAbsent(resources.ConfigMap.Data, "login-identity-providers.xml", func() string {
				return auth.GetLoginIdentityProvider()
			})
		}
		setIfAbsent(resources.ConfigMap.Data, "state-management.xml", func() string {
			return product.StateManagementXML(clusterConfig)
		})
	}

	if resources.StatefulSet != nil {
		h.finishStatefulSet(resources.StatefulSet, cr, auth, gitSync)
	}

	return resources, nil
}

// customizeMainContainer reshapes the framework-assembled primary container
// into the Gen 2 "node" container: bash entrypoint with trap functions, the
// product env set, NiFi's writable conf mount, git-sync content mounts, and
// the Gen 2 startup/liveness probes (the framework's TCP readiness probe on
// the web port is kept — Gen 2 had none, which made rolling updates blind).
func (h *NifiRoleGroupHandler) customizeMainContainer(
	c *corev1.Container,
	cr *nifiv1alpha1.NifiCluster,
	auth *security.Authentication,
	gitSync *gitsync.GitSyncResources,
	cliArgs []string,
) {
	clusterConfig := cr.Spec.ClusterConfig

	if len(cliArgs) > 0 {
		// Gen 2 cliOverrides semantics: the override replaces the entrypoint.
		c.Command = slices.Clone(cliArgs)
		c.Args = nil
	} else {
		c.Command = []string{"/bin/bash", "-x", "-euo", "pipefail", "-c"}
		c.Args = []string{mainContainerScript()}
	}

	c.Env = append(c.Env, containerEnv(cr, auth)...)

	c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
		Name:      EmptyDirVolumeName,
		MountPath: product.NifiConfigDir,
		SubPath:   "config",
		ReadOnly:  false,
	})
	if auth != nil {
		c.VolumeMounts = append(c.VolumeMounts, auth.GetVolumeMounts()...)
	}
	c.VolumeMounts = append(c.VolumeMounts, gitSync.GitSyncVolumeMounts...)

	// Gen 2 parity: no readiness probe. The framework's default TCP readiness
	// on the web port moves the moment readyReplicas reaches the spec — the
	// e2e flow-verification budget (unchanged, it is the acceptance gate) is
	// calibrated against pods that are Ready at container start. Introducing a
	// readiness probe is a behavior change for a separate, verified PR.
	c.ReadinessProbe = nil

	webPort := intstr.FromString("http")
	if clusterConfig.Tls != nil {
		webPort = intstr.FromString("https")
	}
	c.LivenessProbe = &corev1.Probe{
		FailureThreshold:    30,
		InitialDelaySeconds: 10,
		PeriodSeconds:       10,
		SuccessThreshold:    1,
		TimeoutSeconds:      3,
		ProbeHandler: corev1.ProbeHandler{
			TCPSocket: &corev1.TCPSocketAction{Port: webPort},
		},
	}
	c.StartupProbe = &corev1.Probe{
		FailureThreshold:    120,
		InitialDelaySeconds: 10,
		PeriodSeconds:       10,
		SuccessThreshold:    1,
		TimeoutSeconds:      3,
		ProbeHandler: corev1.ProbeHandler{
			TCPSocket: &corev1.TCPSocketAction{Port: webPort},
		},
	}
}

// finishStatefulSet applies the pod-level pieces the container customizer
// cannot reach: the git-sync sidecar containers, the product volumes, and
// image pull secrets. Init containers are injected by the sidecar channel.
func (h *NifiRoleGroupHandler) finishStatefulSet(
	sts *appsv1.StatefulSet,
	cr *nifiv1alpha1.NifiCluster,
	auth *security.Authentication,
	gitSync *gitsync.GitSyncResources,
) {
	podSpec := &sts.Spec.Template.Spec

	// git-sync's continuous sidecars stay regular containers (Gen 2 parity);
	// sorting Containers by name reproduces the Gen 2 rendered order
	// (git-sync-0 before node). InitContainers are NOT sorted: their order is
	// the sidecar channel's phase ordering, which the Vector provider relies on.
	podSpec.Containers = append(podSpec.Containers, gitSync.GitSyncContainers...)
	slices.SortStableFunc(podSpec.Containers, func(a, b corev1.Container) int {
		return strings.Compare(a.Name, b.Name)
	})

	podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
		Name: EmptyDirVolumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	})
	if auth != nil {
		podSpec.Volumes = append(podSpec.Volumes, auth.GetVolumes()...)
	}
	podSpec.Volumes = append(podSpec.Volumes, gitSync.GitSyncVolumes...)

	if cr.Spec.Image != nil && cr.Spec.Image.PullSecretName != "" {
		podSpec.ImagePullSecrets = append(podSpec.ImagePullSecrets, corev1.LocalObjectReference{
			Name: cr.Spec.Image.PullSecretName,
		})
	}
}

// prepareContainer builds the init container that copies the mounted config
// into NiFi's writable conf dir and renders the gomplate templates.
func (h *NifiRoleGroupHandler) prepareContainer(
	cr *nifiv1alpha1.NifiCluster,
	buildCtx *reconciler.RoleGroupBuildContext,
	auth *security.Authentication,
	image string,
) corev1.Container {
	volumeMounts := []corev1.VolumeMount{
		{
			Name:      "config",
			MountPath: constant.KubedoopConfigDirMount,
			ReadOnly:  true,
		},
		{
			Name:      EmptyDirVolumeName,
			MountPath: product.NifiConfigDir,
			SubPath:   "config",
			ReadOnly:  false,
		},
	}
	if auth != nil {
		volumeMounts = append(volumeMounts, auth.GetVolumeMounts()...)
	}

	return corev1.Container{
		Name:            "prepare",
		Image:           image,
		ImagePullPolicy: h.ImagePullPolicy,
		Command:         []string{"/bin/bash", "-x", "-euo", "pipefail", "-c"},
		Args:            []string{prepareContainerScript(cr, buildCtx, auth)},
		Env:             containerEnv(cr, auth),
		VolumeMounts:    volumeMounts,
		SecurityContext: &corev1.SecurityContext{
			RunAsUser:                ptrTo(int64(0)),
			RunAsGroup:               ptrTo(int64(0)),
			AllowPrivilegeEscalation: ptrTo(false),
		},
	}
}

// prepareContainerScript renders the Gen 2 prepare script. NODE_ADDRESS now
// points at the headless Service (Gen 2 pointed at a non-headless Service, so
// the per-pod FQDN never resolved — reviewed intentional diff).
func prepareContainerScript(
	cr *nifiv1alpha1.NifiCluster,
	buildCtx *reconciler.RoleGroupBuildContext,
	auth *security.Authentication,
) string {
	nodeAddress := fmt.Sprintf("$POD_NAME.%s-headless.%s.svc.cluster.local",
		buildCtx.ResourceName, cr.GetNamespace())

	authArgs := ""
	if auth != nil {
		authArgs = auth.GetInitArgs()
	}

	args := `
cp ` + path.Join(constant.KubedoopConfigDirMount, "*") + ` ` + product.NifiConfigDir + `

export NODE_ADDRESS="` + nodeAddress + `"
` + authArgs + `

gomplate -f ` + constant.KubedoopConfigDirMount + `/nifi.properties -o ` + product.NifiConfigDir + `/nifi.properties
gomplate -f ` + constant.KubedoopConfigDirMount + `/login-identity-providers.xml -o ` + product.NifiConfigDir + `/login-identity-providers.xml
gomplate -f ` + constant.KubedoopConfigDirMount + `/state-management.xml -o ` + product.NifiConfigDir + `/state-management.xml
`

	return nifiutil.IndentTab4Spaces(args)
}

// mainContainerScript is the Gen 2 "node" container entrypoint.
func mainContainerScript() string {
	args := nifiutil.CommonBashTrapFunctions + `

` + nifiutil.RemoveVectorShutdownFileCommand() + `

prepare_signal_handlers

# sleep infinity

bin/nifi.sh run &

wait_for_termination $!

` + nifiutil.CreateVectorShutdownFileCommand() + `
`

	return nifiutil.IndentTab4Spaces(args)
}

// containerEnv is the shared env set of the prepare and node containers.
func containerEnv(cr *nifiv1alpha1.NifiCluster, auth *security.Authentication) []corev1.EnvVar {
	clusterConfig := cr.Spec.ClusterConfig

	envVars := []corev1.EnvVar{
		{
			Name: "POD_NAME",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.name",
				},
			},
		},
		// STACKLET_NAME is the ConfigMap name prefix for
		// KubernetesConfigMapStateProvider and the leader election lease prefix.
		{
			Name:  "STACKLET_NAME",
			Value: cr.GetName(),
		},
	}

	if clusterConfig.ZookeeperConfigMapName != nil && *clusterConfig.ZookeeperConfigMapName != "" {
		for _, key := range []string{"ZOOKEEPER_HOSTS", "ZOOKEEPER_CHROOT"} {
			envVars = append(envVars, corev1.EnvVar{
				Name: key,
				ValueFrom: &corev1.EnvVarSource{
					ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
						Key: key,
						LocalObjectReference: corev1.LocalObjectReference{
							Name: *clusterConfig.ZookeeperConfigMapName,
						},
					},
				},
			})
		}
	}

	if auth != nil {
		envVars = append(envVars, auth.GetEnvVars()...)
	}

	return envVars
}

// listenerClassFor maps clusterConfig.listenerClass to the framework listener
// class; unset preserves the Gen 2 default exposure (external-unstable).
func listenerClassFor(clusterConfig *nifiv1alpha1.ClusterConfigSpec) listener.ListenerClass {
	switch clusterConfig.ListenerClass {
	case string(listener.ListenerClassClusterInternal):
		return listener.ListenerClassClusterInternal
	case string(listener.ListenerClassExternalStable):
		return listener.ListenerClassExternalStable
	case "", string(listener.ListenerClassExternalUnstable):
		return listener.ListenerClassExternalUnstable
	default:
		return listener.ListenerClassExternalUnstable
	}
}

// servicePorts mirrors the Gen 2 client Service ports (named targetPorts).
func servicePorts() []corev1.ServicePort {
	ports := make([]corev1.ServicePort, 0, len(product.Ports))
	for _, p := range product.Ports {
		ports = append(ports, corev1.ServicePort{
			Name:       p.Name,
			Port:       p.ContainerPort,
			Protocol:   corev1.ProtocolTCP,
			TargetPort: intstr.FromString(p.Name),
		})
	}
	return ports
}

// defaultImage is the operator's static fallback image; spec.image resolved
// with ImageDefaults overrides it per reconcile.
func defaultImage() string {
	return fmt.Sprintf("%s/%s:%s-kubedoop%s",
		nifiv1alpha1.DefaultRepository,
		nifiv1alpha1.DefaultProductName,
		nifiv1alpha1.DefaultProductVersion,
		version.BuildVersion,
	)
}

func setIfAbsent(data map[string]string, key string, value func() string) {
	if _, exists := data[key]; !exists {
		data[key] = value()
	}
}

func ptrTo[T any](v T) *T { return &v }

// Ensure interface implementation.
var _ reconciler.RoleGroupHandler[*nifiv1alpha1.NifiCluster] = &NifiRoleGroupHandler{}
