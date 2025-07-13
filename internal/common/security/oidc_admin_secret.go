package security

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"path"

	"github.com/zncdatadev/operator-go/pkg/builder"
	"github.com/zncdatadev/operator-go/pkg/client"
	"github.com/zncdatadev/operator-go/pkg/constants"
	"github.com/zncdatadev/operator-go/pkg/reconciler"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
)

type AdminSecretBuilder struct {
	builder.SecretBuilder
}

var (
	UserMountDir = path.Join(constants.KubedoopRoot, "users")
)

func oidcAdminPasswordSecretname(clusterName string) string {
	return clusterName + "-oidc-admin-password"
}

func NewAdminSecretReconciler(
	client *client.Client,
	clusterName string,
	options ...builder.Option,
) *AdminSecretReconciler {
	b := NewAdminSecretBuilder(
		client,
		oidcAdminPasswordSecretname(clusterName),
		options...,
	)

	return &AdminSecretReconciler{
		GenericResourceReconciler: *reconciler.NewGenericResourceReconciler[builder.ConfigBuilder](
			client,
			b,
		),
	}
}

var _ reconciler.ResourceReconciler[builder.ConfigBuilder] = &AdminSecretReconciler{}

type AdminSecretReconciler struct {
	reconciler.GenericResourceReconciler[builder.ConfigBuilder]
}

func (r *AdminSecretReconciler) Reconcile(ctx context.Context) (ctrl.Result, error) {
	secret := &corev1.Secret{}

	ns := r.Client.GetOwnerNamespace()
	name := r.GetName()

	if err := r.Client.Get(ctx, ctrlclient.ObjectKey{Namespace: ns, Name: name}, secret); err != nil {
		if ctrlclient.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
		// Secret not found, create it
		authLogger.Info("Admin secret not found, creating it", "namespace", ns, "name", name)
		return r.GenericResourceReconciler.Reconcile(ctx)
	}

	// if the secret already exists, we don't need to reconcile it
	authLogger.Info("Admin secret already exists, skipping reconciliation", "namespace", ns, "name", name)

	// check the admin password in the secret, if it is not, raise an error
	if _, ok := secret.Data[NifiAdminUsername]; !ok {
		return ctrl.Result{}, fmt.Errorf("admin secret %s/%s does not contain the admin username", ns, name)
	}
	return ctrl.Result{}, nil
}

func NewAdminSecretBuilder(
	client *client.Client,
	name string,
	options ...builder.Option,
) *AdminSecretBuilder {
	return &AdminSecretBuilder{
		SecretBuilder: *builder.NewSecretBuilder(
			client,
			name,
			options...,
		),
	}
}

func (b *AdminSecretBuilder) Build(ctx context.Context) (ctrlclient.Object, error) {
	// generate a random password for the admin user, length 16
	randomBytes := make([]byte, 16)
	if _, err := rand.Read(randomBytes); err != nil {
		return nil, err
	}

	// convert to base64 string
	password := base64.StdEncoding.EncodeToString(randomBytes)
	if len(password) > 16 {
		password = password[:16]
	}

	b.AddItem(NifiAdminUsername, password)

	return b.GetObject(), nil
}
