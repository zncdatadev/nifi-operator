package security

import (
	"fmt"
	"path"
	"strconv"

	authv1alpha1 "github.com/zncdatadev/operator-go/pkg/apis/authentication/v1alpha1"
	"github.com/zncdatadev/operator-go/pkg/constant"
	opsecurity "github.com/zncdatadev/operator-go/pkg/security"
	corev1 "k8s.io/api/core/v1"
)

const (
	DefaultServerTlsStorePassword = "changeit"
	DefaultServerTlsKeyPassword   = "changeit"
)

var _ Authenticator = &ldapAuthenticator{}

type ldapAuthenticator struct {
	clusterName string
	provider    *authv1alpha1.LDAPProvider
}

func (a *ldapAuthenticator) getBindCredentialsVolumeName() string {
	return fmt.Sprintf("%s-bind-credentials", a.provider.BindCredentials.SecretClass)
}

func (a *ldapAuthenticator) getBindCredentialsMountDir() string {
	return path.Join(constant.KubedoopSecretDir, a.provider.BindCredentials.SecretClass)
}

func (a *ldapAuthenticator) GetVolumes() []corev1.Volume {
	// The framework's secret-operator CSI registration; the mount path stays
	// product-owned (getBindCredentialsMountDir) because login-identity-
	// providers.xml references it.
	registration := opsecurity.CredentialsVolume(a.getBindCredentialsVolumeName(), a.provider.BindCredentials.SecretClass)
	if scope := opsecurity.ScopeString(a.provider.BindCredentials.Scope); scope != "" {
		registration = registration.WithScope(scope)
	}
	return opsecurity.NewSecretProvisioner().Register(registration).Volumes()
}

func (a *ldapAuthenticator) GetVolumeMounts() []corev1.VolumeMount {

	return []corev1.VolumeMount{
		{
			Name:      a.getBindCredentialsVolumeName(),
			MountPath: a.getBindCredentialsMountDir(),
		},
	}
}

func (a *ldapAuthenticator) ExtendNifiProperties() map[string]string {
	return nil
}

func (a *ldapAuthenticator) GetEnvVars() []corev1.EnvVar {
	return nil
}

func (a *ldapAuthenticator) GetInitArgs() string {
	return ""
}

func (a *ldapAuthenticator) getBindCredentialsMountPaths() (usernameFile, passwordFile string) {
	if a.provider.BindCredentials != nil && a.provider.BindCredentials.SecretClass != "" {
		usernameFile = path.Join(a.getBindCredentialsMountDir(), "username")
		passwordFile = path.Join(a.getBindCredentialsMountDir(), "password")
	}
	return
}

func (a *ldapAuthenticator) GetLoginIdentityProvider() string {

	authStrategy := "ANONYMOUS"
	usernameFile, passwordFile := a.getBindCredentialsMountPaths()

	if usernameFile != "" && passwordFile != "" {
		authStrategy = "SIMPLE"
		if a.provider.TLS != nil && a.provider.TLS.Verification != nil {
			authStrategy = "LDAPS"
		}
	}

	protocol := "ldap"
	if a.provider.TLS != nil && a.provider.TLS.Verification != nil {
		protocol = "ldaps"
	}

	searchFilter := a.provider.SearchFilter

	if searchFilter == "" {
		uidField := a.provider.LDAPFieldNames.Uid
		searchFilter = fmt.Sprintf("%s={0}", uidField)
	}

	ldapProvider := `
	<provider>
		<identifier>login-identity-provider</identifier>
		<class>org.apache.nifi.ldap.LdapProvider</class>
		<property name="Authentication Strategy">` + authStrategy + `</property>

		<property name="Manager DN">${{file:UTF-8:` + usernameFile + `}}</property>
		<property name="Manager Password">${{file:UTF-8:` + passwordFile + `}</property>

		<property name="Referral Strategy">THROW</property>
		<property name="Connect Timeout">10 secs</property>
		<property name="Read Timeout">10 secs</property>
		<property name="Url">` + protocol + `://` + a.provider.Hostname + `:` + strconv.Itoa(a.provider.Port) + `</property>
		<property name="User Search Base">` + a.provider.SearchBase + `</property>
		<property name="User Search Filter">` + searchFilter + `</property>

		<property name="TLS - Client Auth">NONE</property>
		<property name="TLS - Keystore">` + path.Join(constant.KubedoopTlsDir, "ldap", "keystore.p12") + `</property>
		<property name="TLS - Keystore Password">` + DefaultServerTlsKeyPassword + `</property>
		<property name="TLS - Keystore Type">PKCS12</property>
		<property name="TLS - Truststore">` + path.Join(constant.KubedoopTlsDir, "ldap", "truststore.p12") + `</property>
		<property name="TLS - Truststore Password">` + DefaultServerTlsStorePassword + `</property>
		<property name="TLS - Truststore Type">PKCS12</property>
		<property name="TLS - Protocol">TLSv1.2</property>
		<property name="TLS - Shutdown Gracefully">true</property>

		<property name="Identity Strategy">USE_DN</property>
		<property name="Authentication Expiration">7 days</property>
	</provider>
	`

	return getIdentityProvider(ldapProvider)
}
