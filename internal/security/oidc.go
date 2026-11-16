package security

import (
	"fmt"
	"net/url"
	"strings"

	authv1alpha1 "github.com/zncdatadev/operator-go/pkg/apis/authentication/v1alpha1"
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
					SecretName: OidcAdminPasswordSecretName(a.clusterName),
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

func (a *oidcAuthenticator) ExtendNifiProperties() map[string]string {
	scopes := a.provider.Scopes
	scopes = append(scopes, a.config.ExtraScopes...)

	issuer := url.URL{
		Scheme: "http",
		Host:   a.provider.Hostname,
		Path:   a.provider.RootPath,
	}

	if a.provider.Port != 0 {
		issuer.Host = fmt.Sprintf("%s:%d", a.provider.Hostname, a.provider.Port)
	}

	return map[string]string{
		"nifi.security.user.oidc.discovery.url":          issuer.String(),
		"nifi.security.user.oidc.client.id":              `{{ getenv "OIDC_CLIENT_ID" }}`,
		"nifi.security.user.oidc.client.secret":          `{{ getenv "OIDC_CLIENT_SECRET" }}`,
		"nifi.security.user.oidc.extra.scopes":           strings.Join(scopes, ","),
		"nifi.security.user.oidc.claim.identifying.user": a.provider.PrincipalClaim,
		// TODO: add oidc tls config
	}
}

func (a *oidcAuthenticator) GetInitArgs() string {
	args := `
export NIFI_ADMIN_PASSWORD="$(python3 -c 'import bcrypt; print(bcrypt.hashpw(open("` + getAdminPasswordMountDir() + `", "rb").read().strip(), bcrypt.gensalt()).decode("utf-8"), end="")')"
	`

	return args
}

func (a *oidcAuthenticator) GetLoginIdentityProvider() string {
	return getSingleUserLoginIdentityProvider()
}
