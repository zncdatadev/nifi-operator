package v1alpha1

import (
	authenticationv1alpha1 "github.com/zncdatadev/operator-go/pkg/apis/authentication/v1alpha1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
)

type ClusterConfigSpec struct {
	// +kubebuilder:validation:Optional
	Authentication []AuthenticationSpec `json:"authentication,omitempty"`

	// +kubebuilder:validation:Optional
	// +default:value={"enable": true}
	CreateReportingTaskJob *CreateReportingTaskJobSpec `json:"createReportingTaskJob,omitempty"`

	// +kubebuilder:validation:Type=object
	// +kubebuilder:default={}
	// Ref Pod spec.Volumes: https://kubernetes.io/docs/reference/kubernetes-api/workload-resources/pod-v1/#podspecvolumes
	ExtraVolumes *k8sruntime.RawExtension `json:"extraVolumes,omitempty"`

	// +kubebuilder:validation:Required
	SensitiveProperties *SensitivePropertiesSpec `json:"sensitiveProperties"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default={}
	CustomComponentsGitSync []GitSyncSpec `json:"customComponentsGitSync,omitempty"`

	// +kubebuilder:validation:Optional
	Tls *TlsSpec `json:"tls,omitempty"`

	// +kubebuilder:validation:Optional
	ListenerClass string `json:"listenerClass,omitempty"`

	// +kubebuilder:validation:Optional
	VectorAggregatorConfigMapName string `json:"vectorAggregatorConfigMapName,omitempty"`

	// If set, nifi clustering backend will use this Zookeeper config map.
	// Else, nifi clustering backend will use kubernetes.
	// +kubebuilder:validation:Optional
	ZookeeperConfigMapName *string `json:"zookeeperConfigMapName"`
}

type GitSyncSpec struct {
	// +kubebuilder:validation:Required
	Repo string `json:"repo"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default="main"
	Branch string `json:"branch,omitempty"`

	// The secret that contains the git credentials.
	// The secret must contain:
	// - `GITSYNC_USERNAME`: The username for git authentication.
	// - `GITSYNC_PASSWORD`: The password for git authentication.
	// +kubebuilder:validation:Optional
	CredentialsSecret string `json:"credentialsSecret,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=0
	Depth int32 `json:"depth,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default="/"
	GitFolder string `json:"gitFolder,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default={}
	GitSyncConfig map[string]string `json:"gitSyncConfig,omitempty"`

	// Synchronization interval for git sync.
	// The value is a go duration string, such as "5s" or "2m".
	// +kubebuilder:validation:Optional
	// +kubebuilder:default="20s"
	Wait string `json:"wait,omitempty"`
}

// AuthenticationSpec defines the authentication spec.
type AuthenticationSpec struct {
	// +kubebuilder:validation:Required
	AuthenticationClass string `json:"authenticationClass"`

	// +kubebuilder:validation:Optional
	Oidc *authenticationv1alpha1.OidcSpec `json:"oidc,omitempty"`
}

type CreateReportingTaskJobSpec struct {

	// +kubebuilder:validation:Optional
	// +kubebuilder:default=true
	Enable bool `json:"enable,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Type=object
	// +kubebuilder:default={}
	// Ref PodTemplateSpec: https://kubernetes.io/docs/reference/kubernetes-api/workload-resources/pod-template-v1/
	PodOverrides *k8sruntime.RawExtension `json:"podOverrides,omitempty"`
}

type SensitivePropertiesSpec struct {
	// Only supported algorithms in v2:
	// - NIFI_PBKDF2_AES_GCM_256
	// - NIFI_ARGON2_AES_GCM_256
	// We no longer support deprecated algorithms:
	// - NIFI_BCRYPT_AES_GCM_128
	// - NIFI_BCRYPT_AES_GCM_256
	// - NIFI_PBKDF2_AES_GCM_128
	// - NIFI_ARGON2_AES_GCM_128
	// - NIFI_SCRYPT_AES_GCM_128
	// - NIFI_SCRYPT_AES_GCM_256
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=NIFI_ARGON2_AES_GCM_256
	// +kubebuilder:validation:Enum=NIFI_PBKDF2_AES_GCM_256;NIFI_ARGON2_AES_GCM_256
	Algorithm string `json:"algorithm,omitempty"`

	// +kubebuilder:validation:Optional
	AutoGenerate bool `json:"autoGenerate,omitempty"`

	// A secret container a key `nifiSensitivePropsKey`.
	// If `autoGenerate` is false and the secret is not found, the operator will fail to start.
	// If `autoGenerate` is true, the operator will generate a new key and store it in the secret.
	// +kubebuilder:validation:Required
	KeySecret string `json:"keySecret"`
}

type TlsSpec struct {
	ServerSecretClass string `json:"serverSecretClass"`
}
