package security

import (
	"context"
	"fmt"
	"slices"

	authv1alpha1 "github.com/zncdatadev/operator-go/pkg/apis/authentication/v1alpha1"
	"github.com/zncdatadev/operator-go/pkg/client"
	"github.com/zncdatadev/operator-go/pkg/config/properties"
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

var (
	SupportedAuthTypes = []AuthenticatorType{AuthenticatorTypeLDAP, AuthenticatorTypeOIDC, AuthenticatorStatic}
)

type Authentication struct {
	Authenticators map[AuthenticatorType][]Authenticator
}

func GetAuthProvider(ctx context.Context, client *client.Client, authclass string) (*authv1alpha1.AuthenticationProvider, error) {
	obj := &authv1alpha1.AuthenticationClass{}
	if err := client.Get(ctx, ctrlclient.ObjectKey{Name: authclass}, obj); err != nil {
		if ctrlclient.IgnoreNotFound(err) != nil {
			return nil, err
		}
		authLogger.Info("AuthenticationClass not found", "name", authclass)
	}
	authLogger.Info("Found AuthenticationClass", "name", authclass)
	return obj.Spec.AuthenticationProvider, nil
}

func NewAuthentication(
	ctx context.Context,
	client *client.Client,
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
	authLogger.Info("Creating authentication", "authClass", auths[0].AuthenticationClass)

	for _, auth := range auths {
		provider, err := GetAuthProvider(ctx, client, auth.AuthenticationClass)
		if err != nil {
			return nil, err
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

func (a *Authentication) ExtendNifiProperties() *properties.Properties {
	for _, typedAuthenticator := range a.Authenticators {
		if len(typedAuthenticator) == 1 {
			return typedAuthenticator[0].ExtendNifiProperties()
		}
	}
	return nil
}

func (a *Authentication) GetLoginIdentiryProvider() string {
	for _, typedAuthenticator := range a.Authenticators {
		if len(typedAuthenticator) > 0 {
			return typedAuthenticator[0].GetLoginIdentiryProvider()
		}
	}
	return ""
}

type Authenticator interface {
	GetEnvVars() []corev1.EnvVar
	GetVolumes() []corev1.Volume
	GetVolumeMounts() []corev1.VolumeMount
	ExtendNifiProperties() *properties.Properties
	GetLoginIdentiryProvider() string
	GetArgs() string
}
