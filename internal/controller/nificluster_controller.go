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

	operatorclient "github.com/zncdatadev/operator-go/pkg/client"
	"github.com/zncdatadev/operator-go/pkg/reconciler"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	nifiv1alpha1 "github.com/zncdatadev/nifi-operator/api/v1alpha1"
	"github.com/zncdatadev/nifi-operator/internal/controller/cluster"
)

var logger = ctrl.Log.WithName("controller")

// NifiClusterReconciler reconciles a NifiCluster object
type NifiClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=nifi.kubedoop.dev,resources=nificlusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=nifi.kubedoop.dev,resources=nificlusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nifi.kubedoop.dev,resources=nificlusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=core,resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=authentication.kubedoop.dev,resources=authenticationclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.

func (r *NifiClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger.Info("Reconciling NifiCluster", "name", req.Name, "namespace", req.Namespace)
	// Fetch the NifiCluster instance

	instance := &nifiv1alpha1.NifiCluster{}
	if err := r.Get(ctx, req.NamespacedName, instance); err != nil {
		if client.IgnoreNotFound(err) == nil {
			logger.Info("NifiCluster resource not found, ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get NifiCluster")
		return ctrl.Result{}, err
	}

	resourceClient := &operatorclient.Client{
		Client:         r.Client,
		OwnerReference: instance,
	}

	clientinfo := reconciler.ClusterInfo{
		GVK: &metav1.GroupVersionKind{
			Group:   nifiv1alpha1.GroupVersion.Group,
			Version: nifiv1alpha1.GroupVersion.Version,
			Kind:    "NifiCluster",
		},
		ClusterName: instance.Name,
	}

	reconciler := cluster.NewReconciler(resourceClient, clientinfo, &instance.Spec)

	if err := reconciler.RegisterResources(ctx); err != nil {
		logger.Error(err, "Failed to register resources for NifiCluster", "name", instance.Name)
		return ctrl.Result{}, err
	}

	return reconciler.Run(ctx)
}

// SetupWithManager sets up the controller with the Manager.
func (r *NifiClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&nifiv1alpha1.NifiCluster{}).
		Named("nificluster").
		Complete(r)
}
