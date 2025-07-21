package node

import (
	"context"

	"github.com/zncdatadev/operator-go/pkg/builder"
	"github.com/zncdatadev/operator-go/pkg/client"
	"github.com/zncdatadev/operator-go/pkg/reconciler"
	corev1 "k8s.io/api/core/v1"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

type ServiceBuilder struct {
	*builder.BaseServiceBuilder
}

func NewServiceBuilder(
	client *client.Client,
	name string,
	ports []corev1.ContainerPort,
	options ...builder.ServiceBuilderOption,
) *ServiceBuilder {
	return &ServiceBuilder{
		BaseServiceBuilder: builder.NewServiceBuilder(
			client,
			name,
			ports,
			options...,
		),
	}
}

func (b *ServiceBuilder) Build(_ context.Context) (ctrlclient.Object, error) {
	obj := b.GetObject()
	// set `publishNotReadyAddresses` to true
	// to allow the service to be used for headless services
	if obj.Spec.ClusterIP == corev1.ClusterIPNone {
		obj.Spec.PublishNotReadyAddresses = true
	}
	return obj, nil
}

func NewServiceReconciler(
	client *client.Client,
	name string,
	ports []corev1.ContainerPort,
	options ...builder.ServiceBuilderOption,
) *reconciler.Service {
	svcBuilder := NewServiceBuilder(
		client,
		name,
		ports,
		options...,
	)
	return &reconciler.Service{
		GenericResourceReconciler: *reconciler.NewGenericResourceReconciler[builder.ServiceBuilder](
			client,
			svcBuilder,
		),
	}
}
