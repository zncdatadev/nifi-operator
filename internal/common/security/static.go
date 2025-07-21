package security

import (
	"strings"

	authv1alpha1 "github.com/zncdatadev/operator-go/pkg/apis/authentication/v1alpha1"
	"github.com/zncdatadev/operator-go/pkg/config/properties"
	"github.com/zncdatadev/operator-go/pkg/util"
	corev1 "k8s.io/api/core/v1"
)

var _ Authenticator = &ldapAuthenticator{}

type staticAuthenticator struct {
	clusterName string
	provider    *authv1alpha1.StaticProvider
}

func (a *staticAuthenticator) GetVolumes() []corev1.Volume {
	volumes := []corev1.Volume{
		{
			Name: NifiAdminUsername,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: a.provider.UserCredentialsSecret.Name,
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

func (a *staticAuthenticator) GetVolumeMounts() []corev1.VolumeMount {
	volumeMounts := []corev1.VolumeMount{
		{
			Name:      NifiAdminUsername,
			MountPath: UserMountDir,
			ReadOnly:  true,
		},
	}

	return volumeMounts
}
func (a *staticAuthenticator) ExtendNifiProperties() *properties.Properties {
	return nil
}

func (a *staticAuthenticator) GetEnvVars() []corev1.EnvVar {
	return nil
}

func (a *staticAuthenticator) GetInitArgs() string {
	args := `
export NIFI_ADMIN_PASSWORD="$(python3 -c 'import bcrypt; print(bcrypt.hashpw(open("` + getAdminPasswordMountDir() + `", "rb").read().strip(), bcrypt.gensalt()).decode("utf-8"), end="")')"
	`

	return args
}

func (a *staticAuthenticator) GetLoginIdentiryProvider() string {
	return getSingleUserLoginIdentityProvider()
}

func getIdentityProvider(provider ...string) string {
	// Note: The file `login-identity-providers.xml` first line must be `<?xml version="1.0" encoding="UTF-8" standalone="no"?>`
	// otherwise, it will cause an error when NiFi starts.
	header := `<?xml version="1.0" encoding="UTF-8" standalone="no"?>
<loginIdentityProviders>`
	foot := `</loginIdentityProviders>
`

	snippet := []string{
		header,
		strings.Join(provider, "\n"),
		foot,
	}
	return util.IndentTab4Spaces(strings.Join(snippet, "\n"))
}

func getSingleUserLoginIdentityProvider() string {
	provider := `
	<provider>
		<identifier>login-identity-provider</identifier>
		<class>org.apache.nifi.authentication.single.user.SingleUserLoginIdentityProvider</class>
		<property name="Username">admin</property>
		<property name="Password">{{ getenv "NIFI_ADMIN_PASSWORD" }}</property>
	</provider>`
	return getIdentityProvider(provider)
}
