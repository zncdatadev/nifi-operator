package security

import (
	"fmt"
	"net/url"
	"path"
	"strings"

	authv1alpha1 "github.com/zncdatadev/operator-go/pkg/apis/authentication/v1alpha1"
	"github.com/zncdatadev/operator-go/pkg/config/properties"
	corev1 "k8s.io/api/core/v1"
)

var _ Authenticator = &oidcAuthenticator{}

type oidcAuthenticator struct {
	clusterName string
	config      *authv1alpha1.OidcSpec
	provider    *authv1alpha1.OIDCProvider
}

func (a *oidcAuthenticator) GetEnvVars() []corev1.EnvVar {
	envVars := []corev1.EnvVar{
		{
			Name: "OIDC_CLIENT_ID",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					Key: "CLIENT_ID",
					LocalObjectReference: corev1.LocalObjectReference{
						Name: a.config.ClientCredentialsSecret,
					},
				},
			},
		},
		{
			Name: "OIDC_CLIENT_SECRET",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					Key: "CLIENT_SECRET",
					LocalObjectReference: corev1.LocalObjectReference{
						Name: a.config.ClientCredentialsSecret,
					},
				},
			},
		},
	}

	return envVars
}

func (a *oidcAuthenticator) GetVolumes() []corev1.Volume {
	volumes := []corev1.Volume{
		{
			Name: NifiAdminUsername,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: oidcAdminPasswordSecretname(a.clusterName),
					Items: []corev1.KeyToPath{
						{
							Key:  NifiAdminUsername,
							Path: NifiAdminUsername,
						},
					},
				},
			},
		},
	}

	return volumes
}

func (a *oidcAuthenticator) GetVolumeMounts() []corev1.VolumeMount {
	volumeMounts := []corev1.VolumeMount{
		{
			Name:      NifiAdminUsername,
			MountPath: UserMountDir,
			ReadOnly:  true,
		},
	}

	return volumeMounts
}

func (a *oidcAuthenticator) ExtendNifiProperties() *properties.Properties {

	scopes := a.provider.Scopes
	scopes = append(scopes, a.config.ExtraScopes...)

	cfg := properties.NewProperties()
	issuer := url.URL{
		Scheme: "http",
		Host:   a.provider.Hostname,
		Path:   a.provider.RootPath,
	}

	if a.provider.Port != 0 {
		issuer.Host = fmt.Sprintf("%s:%d", a.provider.Hostname, a.provider.Port)
	}

	cfg.Add("nifi.security.user.oidc.discovery.url", issuer.String())
	cfg.Add("nifi.security.user.oidc.client.id", `{{ getenv "OIDC_CLIENT_ID" }}`)
	cfg.Add("nifi.security.user.oidc.client.secret", `{{ getenv "OIDC_CLIENT_SECRET" }}`)
	cfg.Add("nifi.security.user.oidc.extra.scopes", strings.Join(scopes, ","))
	cfg.Add("nifi.security.user.oidc.claim.identifying.user", a.provider.PrincipalClaim)
	// TODO: add oidc tls config
	return cfg
}
func (a *oidcAuthenticator) GetArgs() string {
	args := `
export NIFI_ADMIN_PASSWORD="$(cat ` + getAdminPasswordMountDir() + ` | htpasswd -niB admin | cut -d: -f2s)"
	`

	return args
}

func (a *oidcAuthenticator) GetLoginIdentiryProvider() string {
	return getIdentityProvider()
}

func getAdminPasswordMountDir() string {
	return path.Join(UserMountDir, NifiAdminUsername)
}
