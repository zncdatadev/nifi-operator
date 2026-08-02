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

package v1alpha1

import (
	commonsv1alpha1 "github.com/zncdatadev/operator-go/pkg/apis/commons/v1alpha1"
)

// RoleName is the single NiFi role. It becomes a segment of every role-group
// resource name ("<cluster>-node-<group>") and the app.kubernetes.io/component
// label value, so it must stay in sync with what the Gen 2 operator rendered.
const RoleName = "node"

// GetSpec adapts the typed NifiCluster spec to the generic cluster spec the
// operator-go GenericReconciler iterates. The CRD keeps the product-shaped
// spec.nodes field; only the runtime view is generic.
func (r *NifiCluster) GetSpec() *commonsv1alpha1.GenericClusterSpec {
	return r.Spec.ToGenericSpec()
}

// GetStatus exposes the embedded generic status. The framework mutates the
// generic status through this pointer, so product-specific status fields (none
// today) survive a reconcile cycle.
func (r *NifiCluster) GetStatus() *commonsv1alpha1.GenericClusterStatus {
	return &r.Status.GenericClusterStatus
}

// VectorAggregatorConfigMapName implements reconciler.VectorAggregatorProvider
// so the framework owns vector.yaml generation when a role group enables the
// Vector agent. Empty means "not wired" and the framework skips the sidecar.
func (r *NifiCluster) VectorAggregatorConfigMapName() string {
	if r.Spec.ClusterConfig == nil {
		return ""
	}
	return r.Spec.ClusterConfig.VectorAggregatorConfigMapName
}

// ToGenericSpec bridges NifiClusterSpec into GenericClusterSpec:
// nodes -> Roles["node"], with the embedded overrides flattened onto the
// role/role-group levels. jvmArgumentOverrides has no generic slot; the
// product config layer reads it from the typed CR directly.
func (s *NifiClusterSpec) ToGenericSpec() *commonsv1alpha1.GenericClusterSpec {
	result := &commonsv1alpha1.GenericClusterSpec{
		ClusterOperation: s.ClusterOperation,
	}

	if s.Image != nil {
		image := &commonsv1alpha1.ImageSpec{
			Custom:          s.Image.Custom,
			Repo:            s.Image.Repo,
			ProductVersion:  s.Image.ProductVersion,
			KubedoopVersion: s.Image.KubedoopVersion,
		}
		if s.Image.PullPolicy != nil {
			image.PullPolicy = *s.Image.PullPolicy
		}
		result.Image = image
	}

	if s.Nodes == nil {
		return result
	}

	roleSpec := commonsv1alpha1.RoleSpec{
		RoleConfig: s.Nodes.RoleConfig,
	}
	if s.Nodes.Config != nil {
		roleSpec.Config = s.Nodes.Config.RoleGroupConfigSpec
	}
	if s.Nodes.OverridesSpec != nil {
		roleSpec.ConfigOverrides = s.Nodes.ConfigOverrides
		roleSpec.EnvOverrides = s.Nodes.EnvOverrides
		roleSpec.CliOverrides = s.Nodes.CliOverrides
		roleSpec.PodOverrides = s.Nodes.PodOverrides
	}

	roleGroups := make(map[string]commonsv1alpha1.RoleGroupSpec, len(s.Nodes.RoleGroups))
	for name, rg := range s.Nodes.RoleGroups {
		adapted := commonsv1alpha1.RoleGroupSpec{
			Replicas: rg.Replicas,
		}
		if rg.Config != nil {
			adapted.Config = rg.Config.RoleGroupConfigSpec
		}
		if rg.OverridesSpec != nil {
			adapted.ConfigOverrides = rg.ConfigOverrides
			adapted.EnvOverrides = rg.EnvOverrides
			adapted.CliOverrides = rg.CliOverrides
			adapted.PodOverrides = rg.PodOverrides
		}
		roleGroups[name] = adapted
	}
	roleSpec.RoleGroups = roleGroups

	result.Roles = map[string]commonsv1alpha1.RoleSpec{
		RoleName: roleSpec,
	}
	return result
}
