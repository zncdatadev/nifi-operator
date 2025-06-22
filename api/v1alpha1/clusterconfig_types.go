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

	// +kubebuilder:validation:Optional
	// +kubebuilder:validation:Type=object
	// +kubebuilder:default={}
	// Ref Pod spec.Volumes: https://kubernetes.io/docs/reference/kubernetes-api/workload-resources/pod-v1/#podspecvolumes
	ExtraVolumes *k8sruntime.RawExtension `json:"extraVolumes,omitempty"`

	// +kubebuilder:validation:Required
	SensitiveProperties *SensitivePropertiesSpec `json:"sensitiveProperties"`

	// +kubebuilder:validation:Optional
	Tls *TlsSpec `json:"tls,omitempty"`

	// +kubebuilder:validation:Optional
	ListenerClass string `json:"listenerClass,omitempty"`

	// +kubebuilder:validation:Optional
	VectorAggregatorConfigMapName string `json:"vectorAggregatorConfigMapName,omitempty"`

	ZookeeperConfigMapName string `json:"zookeeperConfigMapName"`
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
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=nifiPbkdf2AesGcm256
	// +kubebuilder:validation:Enum=nifiArgon2AesGcm128;nifiArgon2AesGcm256;nifiBcryptAesGcm128;nifiBcryptAesGcm256;nifiPbkdf2AesGcm128;nifiPbkdf2AesGcm256;nifiScryptAesGcm128;nifiScryptAesGcm256
	Algorithm string `json:"algorithm,omitempty"`

	// +kubebuilder:validation:Optional
	AutoGenerate bool `json:"autoGenerate,omitempty"`

	// +kubebuilder:validation:Required
	KeySecret string `json:"keySecret"`
}

type TlsSpec struct {
	ServerSecretClass string `json:"serverSecretClass"`
}
