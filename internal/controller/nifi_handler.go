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
)

// Compile-time proof that NifiCluster wires framework-owned vector.yaml
// generation (inert until a role group enables the Vector agent).
var _ reconciler.VectorAggregatorProvider = (*nifiv1alpha1.NifiCluster)(nil)

// RBAC for the resources the SDK GenericReconciler consumes on behalf of a
// NifiCluster. This is the OPERATOR's own ClusterRole; the workload's Role/
// RoleBinding come from GenericReconcilerConfig.WorkloadRBACRules, and RBAC
// escalation prevention requires this ClusterRole to hold every permission
// that hook grants (leases, configmaps).
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

	// confSubPath is the empty-dir subPath backing /kubedoop/nifi/conf, and
	// also the framework's reserved ConfigMap volume name.
	confSubPath = "config"
)

// NifiRoleGroupHandler builds NiFi role group resources. The embedded
// BaseRoleGroupHandler owns the skeleton (ConfigMap from the merged config,
// headless + client Services, StatefulSet, role-level PDB). The role's shape
// is declared once per pass by DeclareRoles, the product's config is derived
// per role group by ResolveRoleGroup, and the BuildResources override adds
// only what neither seam can model: the gomplate prepare init container,
// git-sync containers, product volumes, and the document-style XML files.
type NifiRoleGroupHandler struct {
	*reconciler.BaseRoleGroupHandler[*nifiv1alpha1.NifiCluster]
}

// NewNifiRoleGroupHandler creates the handler with its reconcile-invariant
// collaborators; everything a role is made of is declared per pass by
// DeclareRoles, with the cr in hand.
func NewNifiRoleGroupHandler(scheme *runtime.Scheme) *NifiRoleGroupHandler {
	base := reconciler.NewBaseRoleGroupHandler[*nifiv1alpha1.NifiCluster](scheme)

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

	return &NifiRoleGroupHandler{BaseRoleGroupHandler: base}
}

// DeclareRoles implements reconciler.RoleProvider: the "node" role's whole
// shape, produced once per reconcile pass with the cr in hand — which is what
// lets the TLS-dependent probe port and the authenticator env be computed here
// instead of living in process-wide handler state.
func (h *NifiRoleGroupHandler) DeclareRoles(
	ctx context.Context, c client.Client, cr *nifiv1alpha1.NifiCluster,
) (reconciler.RoleCatalog, error) {
	clusterConfig := cr.Spec.ClusterConfig
	if clusterConfig == nil {
		return nil, fmt.Errorf("spec.clusterConfig must not be nil")
	}

	auth, err := resolveAuthentication(ctx, c, cr)
	if err != nil {
		return nil, err
	}

	webPort := intstr.FromString("http")
	if clusterConfig.Tls != nil {
		webPort = intstr.FromString("https")
	}

	decl := reconciler.RoleDeclaration{
		MainContainerName: MainContainerName,
		ContainerPorts:    product.Ports,
		ServicePorts:      servicePorts(),

		// The Gen 2 entrypoint: bash trap functions around bin/nifi.sh. Args
		// carry the user's cliOverrides (none by default).
		Command: []string{"/bin/bash", "-x", "-euo", "pipefail", "-c", mainContainerScript()},

		// Gen 2 probes: startup gives NiFi up to 20 minutes to open the web
		// port, liveness recycles a wedged process. Readiness parity (none) is
		// restored in BuildResources — a nil declaration keeps the framework's
		// generated probe.
		LivenessProbe: &corev1.Probe{
			FailureThreshold:    30,
			InitialDelaySeconds: 10,
			PeriodSeconds:       10,
			SuccessThreshold:    1,
			TimeoutSeconds:      3,
			ProbeHandler: corev1.ProbeHandler{
				TCPSocket: &corev1.TCPSocketAction{Port: webPort},
			},
		},
		StartupProbe: &corev1.Probe{
			FailureThreshold:    120,
			InitialDelaySeconds: 10,
			PeriodSeconds:       10,
			SuccessThreshold:    1,
			TimeoutSeconds:      3,
			ProbeHandler: corev1.ProbeHandler{
				TCPSocket: &corev1.TCPSocketAction{Port: webPort},
			},
		},

		// Peers must resolve each other's DNS before readiness for cluster
		// formation.
		PublishNotReadyAddresses: true,

		// Unset preserves the Gen 2 default exposure (external-unstable).
		ListenerClass: listenerClassFor(clusterConfig),

		// POD_NAME/STACKLET_NAME plus the ZooKeeper and authenticator wiring;
		// valueFrom entries can only ride the declaration.
		Env: containerEnv(cr, auth),

		// The consumption-time fallback Gen 2 hardcoded; folded beneath the
		// CR's role and role-group levels, so any user value wins.
		ConfigDefaults: &nifiConfigDefaults,
	}

	return reconciler.RoleCatalog{
		nifiv1alpha1.RoleName: decl,
	}, nil
}

// nifiConfigDefaults is the product's role-config default layer, folded
// beneath the CR's role and role-group levels by the framework.
var nifiConfigDefaults = commonsv1alpha1.RoleGroupConfigSpec{
	GracefulShutdownTimeout: ptrTo(product.DefaultGracefulShutdownTimeout),
}

// ResolveRoleGroup implements reconciler.RoleGroupResolver: the product's
// derived configuration for one role group, folded beneath the user's
// overrides. NiFi's config files are computed here (the authenticator's extra
// keys need the API read this seam provides).
func (h *NifiRoleGroupHandler) ResolveRoleGroup(
	ctx context.Context, c client.Client, cr *nifiv1alpha1.NifiCluster,
	rg *reconciler.RoleGroupBuildContext,
) (*reconciler.Contribution, error) {
	props := product.NifiProperties(cr)

	auth, err := resolveAuthentication(ctx, c, cr)
	if err != nil {
		return nil, err
	}
	if auth != nil {
		for k, v := range auth.ExtendNifiProperties() {
			props[k] = v
		}
	}

	return &reconciler.Contribution{
		ConfigOverrides: map[string]map[string]string{
			"bootstrap.conf":  product.BootstrapConfig(cr, rg.RoleGroupName, gracefulShutdownTimeout(rg)),
			"nifi.properties": props,
		},
	}, nil
}

// gracefulShutdownTimeout reads the folded config (declaration default < role
// < role group); Gen 2 wrote the raw duration string into bootstrap.conf.
func gracefulShutdownTimeout(rg *reconciler.RoleGroupBuildContext) string {
	if effective := rg.EffectiveConfig(); effective != nil &&
		effective.GracefulShutdownTimeout != nil && *effective.GracefulShutdownTimeout != "" {
		return *effective.GracefulShutdownTimeout
	}
	return product.DefaultGracefulShutdownTimeout
}

// BuildResources delegates the skeleton to the framework, then applies the
// NiFi-specific pieces neither the declaration nor the resolver can express.
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

	auth, err := resolveAuthentication(ctx, k8sClient, cr)
	if err != nil {
		return nil, err
	}

	gitSync, err := gitsync.NewGitSyncResources(clusterConfig.CustomComponentsGitSync)
	if err != nil {
		return nil, fmt.Errorf("building git-sync resources: %w", err)
	}

	// Init containers ride the framework's sidecar channel (one-shot static
	// providers, injected in name order within the default phase — the Gen 2
	// rendered order). The prepare container runs on the resolved image the
	// reconciler computed for this role group.
	enabled := &sidecar.SidecarConfig{Enabled: true}
	buildCtx.SidecarManager.Register(
		sidecar.NewStaticContainerProvider(
			h.prepareContainer(cr, buildCtx, auth, buildCtx.ResolvedImage.Reference)), enabled)
	for i := range gitSync.GitSyncInitContainers {
		buildCtx.SidecarManager.Register(
			sidecar.NewStaticContainerProvider(gitSync.GitSyncInitContainers[i]), enabled)
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
		h.finishStatefulSet(resources.StatefulSet, auth, gitSync)
	}

	return resources, nil
}

// finishStatefulSet applies the pod-level Gen 2 parity pieces: the git-sync
// sidecar containers (regular containers, name-sorted rendering order), the
// product volumes and main-container mounts, and the removal of the generated
// readiness probe (Gen 2 had none, and the e2e flow-verification budget — the
// unmodifiable acceptance gate — is calibrated against pods that are Ready at
// container start; RoleDeclaration offers no way to declare "no readiness").
func (h *NifiRoleGroupHandler) finishStatefulSet(
	sts *appsv1.StatefulSet,
	auth *security.Authentication,
	gitSync *gitsync.GitSyncResources,
) {
	podSpec := &sts.Spec.Template.Spec

	for i := range podSpec.Containers {
		if podSpec.Containers[i].Name != MainContainerName {
			continue
		}
		node := &podSpec.Containers[i]
		node.ReadinessProbe = nil
		node.VolumeMounts = append(node.VolumeMounts, corev1.VolumeMount{
			Name:      EmptyDirVolumeName,
			MountPath: product.NifiConfigDir,
			SubPath:   confSubPath,
			ReadOnly:  false,
		})
		if auth != nil {
			node.VolumeMounts = append(node.VolumeMounts, auth.GetVolumeMounts()...)
		}
		node.VolumeMounts = append(node.VolumeMounts, gitSync.GitSyncVolumeMounts...)
		break
	}

	// git-sync's continuous sidecars stay regular containers (Gen 2 parity);
	// sorting Containers by name reproduces the Gen 2 rendered order
	// (git-sync-0 before node). InitContainers keep the sidecar channel's
	// phase ordering.
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
}

func setIfAbsent(data map[string]string, key string, value func() string) {
	if _, exists := data[key]; !exists {
		data[key] = value()
	}
}

// resolveAuthentication resolves the CR's authentication classes; nil when the
// CR declares none. Reads go through the informer cache, so calling this from
// each seam that needs it is cheap and keeps the shared handler stateless.
func resolveAuthentication(
	ctx context.Context, c client.Client, cr *nifiv1alpha1.NifiCluster,
) (*security.Authentication, error) {
	if cr.Spec.ClusterConfig == nil || cr.Spec.ClusterConfig.Authentication == nil {
		return nil, nil
	}
	auth, err := security.NewAuthentication(ctx, c, cr.GetName(), cr.Spec.ClusterConfig.Authentication)
	if err != nil {
		return nil, fmt.Errorf("resolving authentication: %w", err)
	}
	return auth, nil
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
			Name:      confSubPath,
			MountPath: constant.KubedoopConfigDirMount,
			ReadOnly:  true,
		},
		{
			Name:      EmptyDirVolumeName,
			MountPath: product.NifiConfigDir,
			SubPath:   confSubPath,
			ReadOnly:  false,
		},
	}
	if auth != nil {
		volumeMounts = append(volumeMounts, auth.GetVolumeMounts()...)
	}

	return corev1.Container{
		Name:            "prepare",
		Image:           image,
		ImagePullPolicy: corev1.PullIfNotPresent,
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

// prepareContainerScript renders the Gen 2 prepare script. NODE_ADDRESS points
// at the headless Service so the per-pod FQDN resolves.
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

func ptrTo[T any](v T) *T { return &v }

var (
	_ reconciler.RoleGroupHandler[*nifiv1alpha1.NifiCluster] = &NifiRoleGroupHandler{}
	_ reconciler.RoleProvider[*nifiv1alpha1.NifiCluster]     = &NifiRoleGroupHandler{}
)
