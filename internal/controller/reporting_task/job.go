package reportingtask

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/zncdatadev/operator-go/pkg/builder"
	"github.com/zncdatadev/operator-go/pkg/client"
	"github.com/zncdatadev/operator-go/pkg/constants"
	"github.com/zncdatadev/operator-go/pkg/reconciler"
	"github.com/zncdatadev/operator-go/pkg/util"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/zncdatadev/nifi-operator/internal/common/security"
)

const (
	ReportingTaskCertVolumeName  = "tls"
	ReportingTaskCertVolumeMount = "/kubedoop/cert"

	// The Python script path in the kubedoop NiFi image
	ReportingTaskScriptPath = "/kubedoop/python/create_nifi_reporting_task.py"
)

// ReportingTaskJobBuilder builds the Job that creates a NiFi ReportingTask
// to enable JVM and NiFi Prometheus metrics.
//
// The Job runs a Python script (create_nifi_reporting_task.py) that uses the
// nipyapi library to authenticate and run the required REST calls to the
// NiFi REST API.
type ReportingTaskJobBuilder struct {
	builder.ObjectMeta

	Image          *util.Image
	ClusterName    string
	Namespace      string
	HttpsPort      int32
	MetricsPort    int32
	Authentication *security.Authentication
	TlsEnabled     bool
	TlsSecretClass string
}

func NewReportingTaskJobBuilder(
	client *client.Client,
	clusterName string,
	image *util.Image,
	httpsPort int32,
	metricsPort int32,
	authentication *security.Authentication,
	tlsEnabled bool,
	tlsSecretClass string,
	options ...builder.Option,
) *ReportingTaskJobBuilder {
	productVersion := image.ProductVersion
	jobName := fmt.Sprintf(
		"%s-create-reporting-task-%s",
		clusterName,
		strings.ReplaceAll(productVersion, ".", "-"),
	)

	return &ReportingTaskJobBuilder{
		ObjectMeta:     *builder.NewObjectMeta(client, jobName, options...),
		Image:          image,
		ClusterName:    clusterName,
		Namespace:      client.GetOwnerNamespace(),
		HttpsPort:      httpsPort,
		MetricsPort:    metricsPort,
		Authentication: authentication,
		TlsEnabled:     tlsEnabled,
		TlsSecretClass: tlsSecretClass,
	}
}

func (b *ReportingTaskJobBuilder) Build(_ context.Context) (ctrlclient.Object, error) {
	imageTag, err := b.Image.GetImageWithTag()
	if err != nil {
		return nil, fmt.Errorf("failed to get image with tag: %w", err)
	}

	container := b.buildContainer(imageTag)

	volumes := b.buildVolumes()

	backoffLimit := int32(100)
	ttlSecondsAfterFinished := int32(120)
	restartPolicy := corev1.RestartPolicyOnFailure

	var nifiUID int64 = 1000
	var rootGroup int64 = 0
	var fsGroup int64 = 1000

	job := &batchv1.Job{
		ObjectMeta: b.GetObjectMeta(),
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			TTLSecondsAfterFinished: &ttlSecondsAfterFinished,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: b.GetLabels(),
				},
				Spec: corev1.PodSpec{
					Containers:    []corev1.Container{*container},
					Volumes:       volumes,
					RestartPolicy: restartPolicy,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsUser:  &nifiUID,
						RunAsGroup: &rootGroup,
						FSGroup:    &fsGroup,
					},
				},
			},
		},
	}

	if b.Image.PullSecretName != "" {
		job.Spec.Template.Spec.ImagePullSecrets = []corev1.LocalObjectReference{
			{Name: b.Image.PullSecretName},
		}
	}

	return job, nil
}

func (b *ReportingTaskJobBuilder) buildContainer(imageTag string) *corev1.Container {
	reportingTaskFQDN := ReportingTaskFQDNServiceName(b.ClusterName, b.Namespace)

	var nifiConnectURL string
	if b.TlsEnabled {
		nifiConnectURL = fmt.Sprintf("https://%s:%d/nifi-api", reportingTaskFQDN, b.HttpsPort)
	} else {
		nifiConnectURL = fmt.Sprintf("http://%s:%d/nifi-api", reportingTaskFQDN, b.HttpsPort)
	}

	productVersion := b.Image.ProductVersion
	adminUsername := security.NifiAdminUsername

	// Build the command arguments for the Python script
	args := []string{
		ReportingTaskScriptPath,
		fmt.Sprintf("-n %s", nifiConnectURL),
		fmt.Sprintf("-u %s", adminUsername),
	}

	// Add password argument only when authentication is configured and provides credentials.
	if b.Authentication != nil {
		adminPasswordFile := b.getAdminPasswordFile()
		args = append(args, fmt.Sprintf("-p \"$(cat %s)\"", adminPasswordFile))
	}

	args = append(args,
		fmt.Sprintf("-v %s", productVersion),
		fmt.Sprintf("-m %d", b.MetricsPort),
	)

	// Add CA cert argument if TLS is enabled with a secret class (volume will be mounted)
	if b.TlsEnabled && b.TlsSecretClass != "" {
		args = append(args, fmt.Sprintf("-c %s", path.Join(ReportingTaskCertVolumeMount, "ca.crt")))
	}

	volumeMounts := b.buildVolumeMounts()

	container := &corev1.Container{
		Name:    ReportingTaskContainerName,
		Image:   imageTag,
		Command: []string{"sh", "-c"},
		Args:    []string{strings.Join(args, " ")},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("400m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
		},
		VolumeMounts: volumeMounts,
	}

	return container
}

// getAdminPasswordFile returns the path to the admin password file based on authentication type.
func (b *ReportingTaskJobBuilder) getAdminPasswordFile() string {
	userMountDir := path.Join(constants.KubedoopRoot, "users")
	return path.Join(userMountDir, security.NifiAdminUsername)
}

func (b *ReportingTaskJobBuilder) buildVolumeMounts() []corev1.VolumeMount {
	var volumeMounts []corev1.VolumeMount

	// TLS certificate volume mount — only when a secret class is also configured
	// so the mount and volume are always created together.
	if b.TlsEnabled && b.TlsSecretClass != "" {
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      ReportingTaskCertVolumeName,
			MountPath: ReportingTaskCertVolumeMount,
		})
	}

	// Authentication volume mounts
	if b.Authentication != nil {
		volumeMounts = append(volumeMounts, b.Authentication.GetVolumeMounts()...)
	}

	return volumeMounts
}

func (b *ReportingTaskJobBuilder) buildVolumes() []corev1.Volume {
	var volumes []corev1.Volume

	// TLS certificate volume from secret operator
	if b.TlsEnabled && b.TlsSecretClass != "" {
		tlsVolBuilder := builder.NewSecretOperatorVolume(ReportingTaskCertVolumeName, b.TlsSecretClass)
		volumes = append(volumes, *tlsVolBuilder.Builde())
	}

	// Authentication volumes
	if b.Authentication != nil {
		volumes = append(volumes, b.Authentication.GetVolumes()...)
	}

	return volumes
}

// NewReportingTaskJobReconciler creates a reconciler for the reporting task job.
func NewReportingTaskJobReconciler(
	client *client.Client,
	clusterName string,
	image *util.Image,
	httpsPort int32,
	metricsPort int32,
	authentication *security.Authentication,
	tlsEnabled bool,
	tlsSecretClass string,
	options ...builder.Option,
) *reconciler.SimpleResourceReconciler[builder.ObjectBuilder] {
	jobBuilder := NewReportingTaskJobBuilder(
		client,
		clusterName,
		image,
		httpsPort,
		metricsPort,
		authentication,
		tlsEnabled,
		tlsSecretClass,
		options...,
	)

	return reconciler.NewSimpleResourceReconciler[builder.ObjectBuilder](
		client,
		jobBuilder,
	)
}
