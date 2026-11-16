package security

import (
	"context"
	"fmt"
	"slices"

	authv1alpha1 "github.com/zncdatadev/operator-go/pkg/apis/authentication/v1alpha1"
	"github.com/zncdatadev/operator-go/pkg/constant"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	nifiv1alpha1 "github.com/zncdatadev/nifi-operator/api/v1alpha1"
)

var authLogger = ctrl.Log.WithName("security").WithName("authentication")

type AuthenticatorType string

const (
	AuthenticatorTypeLDAP AuthenticatorType = "ldap"
	AuthenticatorTypeOIDC AuthenticatorType = "oidc"
	AuthenticatorStatic   AuthenticatorType = "static"
)

const (
	NifiAdminUsername = "admin"
)

// UserMountDir is where the admin credentials Secret is mounted.
const UserMountDir = constant.KubedoopRoot + "users"

var (
	SupportedAuthTypes = []AuthenticatorType{AuthenticatorTypeLDAP, AuthenticatorTypeOIDC, AuthenticatorStatic}
)

// OidcAdminPasswordSecretName returns the name of the generated admin-password
// Secret used by the OIDC single-user fallback login.
func OidcAdminPasswordSecretName(clusterName string) string {
	return clusterName + "-oidc-admin-password"
}

type Authentication struct {
	Authenticators map[AuthenticatorType][]Authenticator
}

func GetAuthProvider(ctx context.Context, c ctrlclient.Client, authclass string) (*authv1alpha1.AuthenticationProvider, error) {
	obj := &authv1alpha1.AuthenticationClass{}
	if err := c.Get(ctx, ctrlclient.ObjectKey{Name: authclass}, obj); err != nil {
		return nil, fmt.Errorf("getting AuthenticationClass %q: %w", authclass, err)
	}
	authLogger.V(1).Info("Found AuthenticationClass", "name", authclass)
	return obj.Spec.AuthenticationProvider, nil
}

func NewAuthentication(
	ctx context.Context,
	c ctrlclient.Client,
	clusterName string,
	auths []nifiv1alpha1.AuthenticationSpec,
) (*Authentication, error) {
	authenticators := make(map[AuthenticatorType][]Authenticator)
	if len(auths) == 0 {
		return nil, fmt.Errorf("no authentication specifications provided")
	}
	if len(auths) > 1 {
		return nil, fmt.Errorf("multiple authentication specifications are not supported")
	}
	if len(auths[0].AuthenticationClass) == 0 {
		return nil, fmt.Errorf("authentication class is required")
	}

	for _, auth := range auths {
		provider, err := GetAuthProvider(ctx, c, auth.AuthenticationClass)
		if err != nil {
			return nil, err
		}
		if provider == nil {
			return nil, fmt.Errorf("AuthenticationClass %q has no provider", auth.AuthenticationClass)
		}

		if provider.OIDC != nil && slices.Contains(SupportedAuthTypes, AuthenticatorTypeOIDC) {
			oidcAuth := &oidcAuthenticator{clusterName: clusterName, config: auth.Oidc, provider: provider.OIDC}
			authenticators[AuthenticatorTypeOIDC] = append(authenticators[AuthenticatorTypeOIDC], oidcAuth)
		} else if provider.LDAP != nil && slices.Contains(SupportedAuthTypes, AuthenticatorTypeLDAP) {
			ldapAuth := &ldapAuthenticator{clusterName: clusterName, provider: provider.LDAP}
			authenticators[AuthenticatorTypeLDAP] = append(authenticators[AuthenticatorTypeLDAP], ldapAuth)
		} else if provider.Static != nil && slices.Contains(SupportedAuthTypes, AuthenticatorStatic) {
			staticAuth := &staticAuthenticator{clusterName: clusterName, provider: provider.Static}
			authenticators[AuthenticatorStatic] = append(authenticators[AuthenticatorStatic], staticAuth)
		} else {
			return nil, fmt.Errorf("unsupported authentication provider: %s", auth.AuthenticationClass)
		}
	}

	return &Authentication{
		Authenticators: authenticators,
	}, nil
}

// HasAuthenticator reports whether an authenticator of the given type is configured.
func (a *Authentication) HasAuthenticator(t AuthenticatorType) bool {
	return len(a.Authenticators[t]) > 0
}

func (a *Authentication) GetInitArgs() string {
	for _, typedAuthenticator := range a.Authenticators {
		if len(typedAuthenticator) == 1 {
			return typedAuthenticator[0].GetInitArgs()
		}
	}
	return ""
}

func (a *Authentication) GetEnvVars() []corev1.EnvVar {
	for _, typedAuthenticator := range a.Authenticators {
		if len(typedAuthenticator) == 1 {
			return typedAuthenticator[0].GetEnvVars()
		}
	}
	return nil
}

func (a *Authentication) GetVolumes() []corev1.Volume {
	for _, typedAuthenticator := range a.Authenticators {
		if len(typedAuthenticator) == 1 {
			return typedAuthenticator[0].GetVolumes()
		}
	}
	return nil
}

func (a *Authentication) GetVolumeMounts() []corev1.VolumeMount {
	for _, typedAuthenticator := range a.Authenticators {
		if len(typedAuthenticator) == 1 {
			return typedAuthenticator[0].GetVolumeMounts()
		}
	}
	return nil
}

// ExtendNifiProperties returns extra nifi.properties entries the authenticator
// contributes (nil when none).
func (a *Authentication) ExtendNifiProperties() map[string]string {
	for _, typedAuthenticator := range a.Authenticators {
		if len(typedAuthenticator) == 1 {
			return typedAuthenticator[0].ExtendNifiProperties()
		}
	}
	return nil
}

func (a *Authentication) GetLoginIdentityProvider() string {
	for _, typedAuthenticator := range a.Authenticators {
		if len(typedAuthenticator) > 0 {
			return typedAuthenticator[0].GetLoginIdentityProvider()
		}
	}
	return ""
}

type Authenticator interface {
	GetEnvVars() []corev1.EnvVar
	GetVolumes() []corev1.Volume
	GetVolumeMounts() []corev1.VolumeMount
	ExtendNifiProperties() map[string]string
	GetLoginIdentityProvider() string
	GetInitArgs() string
}
