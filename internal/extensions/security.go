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

// Package extensions holds the cluster-scope hooks registered on the
// NifiCluster extension registry: generate-once Secrets, pod RBAC, and the
// NiFi 1.x reporting-task resources.
package extensions

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"github.com/zncdatadev/operator-go/pkg/common"
	"github.com/zncdatadev/operator-go/pkg/reconciler"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	nifiv1alpha1 "github.com/zncdatadev/nifi-operator/api/v1alpha1"
	"github.com/zncdatadev/nifi-operator/internal/security"
)

// SensitivePropsKeyName is the single key of the sensitive-properties Secret.
const SensitivePropsKeyName = "nifiSensitivePropsKey"

// SecurityExtension ensures the generate-once Secrets NiFi needs before any
// workload is built: the sensitive-properties key and, for OIDC clusters, the
// admin fallback password. Both must never converge (a regenerated value would
// invalidate the running cluster), which is exactly EnsureGeneratedSecret's
// contract: create if absent, fill only missing keys, never rewrite a value.
type SecurityExtension struct {
	scheme *runtime.Scheme
}

func NewSecurityExtension(scheme *runtime.Scheme) *SecurityExtension {
	return &SecurityExtension{scheme: scheme}
}

func (e *SecurityExtension) Name() string { return "nifi-security" }

func (e *SecurityExtension) PreReconcile(ctx context.Context, c client.Client, cr *nifiv1alpha1.NifiCluster) error {
	if cr.Spec.ClusterConfig == nil {
		return fmt.Errorf("spec.clusterConfig must not be nil")
	}

	if err := e.ensureSensitiveKeySecret(ctx, c, cr); err != nil {
		return err
	}
	return e.ensureOidcAdminSecret(ctx, c, cr)
}

func (e *SecurityExtension) PostReconcile(ctx context.Context, c client.Client, cr *nifiv1alpha1.NifiCluster) error {
	return nil
}

func (e *SecurityExtension) OnReconcileError(ctx context.Context, c client.Client, cr *nifiv1alpha1.NifiCluster, err error) error {
	return nil
}

// ensureSensitiveKeySecret preserves the Gen 2 contract: with autoGenerate
// false and the Secret absent, the reconcile fails (Degraded) instead of
// generating a key the user intended to supply.
func (e *SecurityExtension) ensureSensitiveKeySecret(ctx context.Context, c client.Client, cr *nifiv1alpha1.NifiCluster) error {
	sensitiveConfig := cr.Spec.ClusterConfig.SensitiveProperties
	if sensitiveConfig == nil {
		return fmt.Errorf("spec.clusterConfig.sensitiveProperties must not be nil")
	}

	if !sensitiveConfig.AutoGenerate {
		secret := &corev1.Secret{}
		key := client.ObjectKey{Namespace: cr.GetNamespace(), Name: sensitiveConfig.KeySecret}
		if err := c.Get(ctx, key, secret); err != nil {
			if apierrors.IsNotFound(err) {
				return fmt.Errorf("sensitive key secret %s/%s not found, but auto generation is disabled",
					cr.GetNamespace(), sensitiveConfig.KeySecret)
			}
			return err
		}
		return nil
	}

	_, err := reconciler.EnsureGeneratedSecret(ctx, c, e.scheme, cr, sensitiveConfig.KeySecret,
		map[string]func() (string, error){SensitivePropsKeyName: generateRandomKey},
		reconciler.WithGeneratedSecretProductName(nifiv1alpha1.DefaultProductName),
	)
	return err
}

// ensureOidcAdminSecret creates the OIDC single-user fallback password. Gen 2
// registered this reconciler for LDAP while the OIDC volume consumed it — the
// registration is now matched to the consumer.
func (e *SecurityExtension) ensureOidcAdminSecret(ctx context.Context, c client.Client, cr *nifiv1alpha1.NifiCluster) error {
	auths := cr.Spec.ClusterConfig.Authentication
	if auths == nil {
		return nil
	}

	auth, err := security.NewAuthentication(ctx, c, cr.GetName(), auths)
	if err != nil {
		return fmt.Errorf("resolving authentication: %w", err)
	}
	if !auth.HasAuthenticator(security.AuthenticatorTypeOIDC) {
		return nil
	}

	_, err = reconciler.EnsureGeneratedSecret(ctx, c, e.scheme, cr,
		security.OidcAdminPasswordSecretName(cr.GetName()),
		map[string]func() (string, error){security.NifiAdminUsername: generateRandomKey},
		reconciler.WithGeneratedSecretProductName(nifiv1alpha1.DefaultProductName),
	)
	return err
}

// generateRandomKey reproduces the Gen 2 generator: base64 of 16 random bytes,
// truncated to 16 characters.
func generateRandomKey() (string, error) {
	randomBytes := make([]byte, 16)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}
	password := base64.StdEncoding.EncodeToString(randomBytes)
	if len(password) > 16 {
		password = password[:16]
	}
	return password, nil
}

var _ common.ClusterExtension[*nifiv1alpha1.NifiCluster] = &SecurityExtension{}
