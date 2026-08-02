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

package extensions

import (
	"context"
	"fmt"

	"github.com/zncdatadev/operator-go/pkg/builder"
	"github.com/zncdatadev/operator-go/pkg/common"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	nifiv1alpha1 "github.com/zncdatadev/nifi-operator/api/v1alpha1"
)

// RbacExtension creates the per-CR Role and RoleBinding NiFi pods need in
// Kubernetes-native clustering mode: leases (KubernetesLeaderElectionManager)
// and configmaps (KubernetesConfigMapStateProvider). The ServiceAccount itself
// is ensured by the framework via ServiceAccountNameFunc; the names stay the
// Gen 2 "<cluster>-nifi".
type RbacExtension struct {
	scheme *runtime.Scheme
}

func NewRbacExtension(scheme *runtime.Scheme) *RbacExtension {
	return &RbacExtension{scheme: scheme}
}

func (e *RbacExtension) Name() string { return "nifi-pod-rbac" }

func (e *RbacExtension) PreReconcile(ctx context.Context, c client.Client, cr *nifiv1alpha1.NifiCluster) error {
	name := cr.GetName() + "-nifi"

	role := builder.NewRoleBuilder(name, cr.GetNamespace()).
		AddPolicyRule(rbacv1.PolicyRule{
			// KubernetesLeaderElectionManager needs leases for leader election
			APIGroups: []string{"coordination.k8s.io"},
			Resources: []string{"leases"},
			Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
		}).
		AddPolicyRule(rbacv1.PolicyRule{
			// KubernetesConfigMapStateProvider stores cluster state in ConfigMaps
			APIGroups: []string{""},
			Resources: []string{"configmaps"},
			Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
		}).
		Build()

	roleBinding := builder.NewRoleBindingBuilder(name, cr.GetNamespace()).
		WithRoleRef(name).
		AddServiceAccountSubject(name, cr.GetNamespace()).
		Build()

	for _, obj := range []client.Object{role, roleBinding} {
		if err := controllerutil.SetControllerReference(cr, obj, e.scheme); err != nil {
			return fmt.Errorf("setting owner reference on %T %s: %w", obj, obj.GetName(), err)
		}
		if err := apply(ctx, c, obj); err != nil {
			return err
		}
	}
	return nil
}

func (e *RbacExtension) PostReconcile(ctx context.Context, c client.Client, cr *nifiv1alpha1.NifiCluster) error {
	return nil
}

func (e *RbacExtension) OnReconcileError(ctx context.Context, c client.Client, cr *nifiv1alpha1.NifiCluster, err error) error {
	return nil
}

// apply creates the object or updates the existing one to the desired state.
func apply(ctx context.Context, c client.Client, obj client.Object) error {
	existing := obj.DeepCopyObject().(client.Object)
	err := c.Get(ctx, client.ObjectKeyFromObject(obj), existing)
	if err != nil {
		if client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("getting %T %s: %w", obj, obj.GetName(), err)
		}
		if err := c.Create(ctx, obj); err != nil {
			return fmt.Errorf("creating %T %s: %w", obj, obj.GetName(), err)
		}
		return nil
	}
	obj.SetResourceVersion(existing.GetResourceVersion())
	if err := c.Update(ctx, obj); err != nil {
		return fmt.Errorf("updating %T %s: %w", obj, obj.GetName(), err)
	}
	return nil
}

var _ common.ClusterExtension[*nifiv1alpha1.NifiCluster] = &RbacExtension{}
