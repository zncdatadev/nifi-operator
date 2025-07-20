package security

import (
	"fmt"
	"path"
	"strconv"

	authv1alpha1 "github.com/zncdatadev/operator-go/pkg/apis/authentication/v1alpha1"
	"github.com/zncdatadev/operator-go/pkg/builder"
	"github.com/zncdatadev/operator-go/pkg/config/properties"
	"github.com/zncdatadev/operator-go/pkg/constants"
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
	return path.Join(constants.KubedoopSecretDir, a.provider.BindCredentials.SecretClass)
}

func (a *ldapAuthenticator) GetVolumes() []corev1.Volume {
	secretClass := a.provider.BindCredentials.SecretClass

	svcScope := make([]string, 0)
	podScope := false
	nodeScope := false
	if a.provider.BindCredentials.Scope != nil {
		if a.provider.BindCredentials.Scope.Pod {
			podScope = true
		}
		if a.provider.BindCredentials.Scope.Node {
			nodeScope = true
		}
		if a.provider.BindCredentials.Scope.Services != nil {
			for _, s := range a.provider.BindCredentials.Scope.Services {
				svcScope = append(svcScope, string(constants.ServiceScope)+"="+s)
			}
		}
	}

	b := builder.NewSecretOperatorVolume(a.getBindCredentialsVolumeName(), secretClass)
	b.SetScope(&builder.SecretVolumeScope{
		Pod:     podScope,
		Node:    nodeScope,
		Service: svcScope,
	})

	return []corev1.Volume{*b.Builde()}
}

func (a *ldapAuthenticator) GetVolumeMounts() []corev1.VolumeMount {

	return []corev1.VolumeMount{
		{
			Name:      a.getBindCredentialsVolumeName(),
			MountPath: a.getBindCredentialsMountDir(),
		},
	}
}

func (a *ldapAuthenticator) ExtendNifiProperties() *properties.Properties {
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

func (a *ldapAuthenticator) GetLoginIdentiryProvider() string {

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
		<property name="TLS - Keystore">` + path.Join(constants.KubedoopTlsDir, "ldap", "keystore.p12") + `</property>
		<property name="TLS - Keystore Password">` + DefaultServerTlsKeyPassword + `</property>
		<property name="TLS - Keystore Type">PKCS12</property>
		<property name="TLS - Truststore">` + path.Join(constants.KubedoopTlsDir, "ldap", "truststore.p12") + `</property>
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
