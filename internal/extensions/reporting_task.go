/*
Copyright 2025 ZNCDataDev.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package extensions

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	commonsv1alpha1 "github.com/zncdatadev/operator-go/pkg/apis/commons/v1alpha1"
	"github.com/zncdatadev/operator-go/pkg/common"
	"github.com/zncdatadev/operator-go/pkg/constant"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	nifiv1alpha1 "github.com/zncdatadev/nifi-operator/api/v1alpha1"
	"github.com/zncdatadev/nifi-operator/internal/product"
	"github.com/zncdatadev/nifi-operator/internal/security"
	"github.com/zncdatadev/nifi-operator/internal/version"
	opsecurity "github.com/zncdatadev/operator-go/pkg/security"
)

const (
	reportingTaskContainerName   = "reporting-task"
	reportingTaskCertVolumeName  = "tls"
	reportingTaskCertVolumeMount = "/kubedoop/cert"

	// The Python script path in the kubedoop NiFi image
	reportingTaskScriptPath = "/kubedoop/python/create_nifi_reporting_task.py"
)

// ReportingTaskExtension creates the Service + Job pair that registers NiFi
// 1.x's PrometheusReportingTask via the REST API. NiFi 2.x serves Prometheus
// metrics natively, so the whole extension is a no-op there (unchanged Gen 2
// gate). It runs PostReconcile: the Job talks to the NiFi API, which cannot
// exist before the workloads are built.
type ReportingTaskExtension struct {
	scheme *runtime.Scheme
}

func NewReportingTaskExtension(scheme *runtime.Scheme) *ReportingTaskExtension {
	return &ReportingTaskExtension{scheme: scheme}
}

func (e *ReportingTaskExtension) Name() string { return "nifi-reporting-task" }

func (e *ReportingTaskExtension) PreReconcile(ctx context.Context, c client.Client, cr *nifiv1alpha1.NifiCluster) error {
	return nil
}

func (e *ReportingTaskExtension) PostReconcile(ctx context.Context, c client.Client, cr *nifiv1alpha1.NifiCluster) error {
	if !product.ReportingTaskEnabled(cr) {
		return nil
	}

	var auth *security.Authentication
	if cr.Spec.ClusterConfig.Authentication != nil {
		var err error
		auth, err = security.NewAuthentication(ctx, c, cr.GetName(), cr.Spec.ClusterConfig.Authentication)
		if err != nil {
			return fmt.Errorf("resolving authentication for reporting task: %w", err)
		}
	}

	svc, err := e.buildService(cr)
	if err != nil {
		return err
	}
	job, err := e.buildJob(cr, auth)
	if err != nil {
		return err
	}

	for _, obj := range []client.Object{svc, job} {
		if err := controllerutil.SetControllerReference(cr, obj, e.scheme); err != nil {
			return fmt.Errorf("setting owner reference on %T %s: %w", obj, obj.GetName(), err)
		}
		if err := e.ensure(ctx, c, obj); err != nil {
			return err
		}
	}
	return nil
}

func (e *ReportingTaskExtension) OnReconcileError(ctx context.Context, c client.Client, cr *nifiv1alpha1.NifiCluster, err error) error {
	return nil
}

// ensure creates the object if absent. The Job is immutable once created (its
// pod template cannot be updated), so an existing object is left alone.
func (e *ReportingTaskExtension) ensure(ctx context.Context, c client.Client, obj client.Object) error {
	existing := obj.DeepCopyObject().(client.Object)
	err := c.Get(ctx, client.ObjectKeyFromObject(obj), existing)
	if err == nil {
		return nil
	}
	if client.IgnoreNotFound(err) != nil {
		return fmt.Errorf("getting %T %s: %w", obj, obj.GetName(), err)
	}
	if err := c.Create(ctx, obj); err != nil {
		return fmt.Errorf("creating %T %s: %w", obj, obj.GetName(), err)
	}
	return nil
}

func reportingTaskServiceName(clusterName string) string {
	return fmt.Sprintf("%s-%s", clusterName, reportingTaskContainerName)
}

func reportingTaskFQDN(clusterName, namespace string) string {
	return fmt.Sprintf("%s.%s.svc.cluster.local", reportingTaskServiceName(clusterName), namespace)
}

// webPort returns the NiFi web port matching the cluster's TLS mode.
func webPort(cr *nifiv1alpha1.NifiCluster) (name string, port int32) {
	if cr.Spec.ClusterConfig.Tls != nil {
		return "https", product.GetPort("https")
	}
	return "http", product.GetPort("http")
}

// selectorPod returns the name of the first pod of the first role group (by
// sorted name) with replicas > 0. The Service must target a single node: the
// JWT NiFi 1.25+ generates uses the pod FQDN as issuer, so requests delegated
// to a different node fail.
func selectorPod(cr *nifiv1alpha1.NifiCluster) (string, error) {
	nodes := cr.Spec.Nodes
	if nodes == nil || len(nodes.RoleGroups) == 0 {
		return "", fmt.Errorf("no role groups defined for reporting task service")
	}

	names := make([]string, 0, len(nodes.RoleGroups))
	for name := range nodes.RoleGroups {
		names = append(names, name)
	}
	sort.Strings(names)

	selected := names[0]
	for _, name := range names {
		rg := nodes.RoleGroups[name]
		if rg.Replicas != nil && *rg.Replicas > 0 {
			selected = name
			break
		}
	}

	return fmt.Sprintf("%s-%s-%s-0", cr.GetName(), nifiv1alpha1.RoleName, selected), nil
}

func (e *ReportingTaskExtension) buildService(cr *nifiv1alpha1.NifiCluster) (*corev1.Service, error) {
	pod, err := selectorPod(cr)
	if err != nil {
		return nil, err
	}

	portName, port := webPort(cr)

	// Gen 2 selected on a managed-by value the pods never carried (and always
	// targeted the https container port); both fixed here.
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      reportingTaskServiceName(cr.GetName()),
			Namespace: cr.GetNamespace(),
			Labels: map[string]string{
				constant.LabelKubernetesInstance:  cr.GetName(),
				constant.LabelKubernetesName:      nifiv1alpha1.DefaultProductName,
				constant.LabelKubernetesComponent: nifiv1alpha1.RoleName,
			},
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{
				{
					Name:       portName,
					Port:       port,
					Protocol:   corev1.ProtocolTCP,
					TargetPort: intstr.FromString(portName),
				},
			},
			Selector: map[string]string{
				constant.LabelKubernetesInstance:     cr.GetName(),
				constant.LabelKubernetesComponent:    nifiv1alpha1.RoleName,
				"statefulset.kubernetes.io/pod-name": pod,
			},
		},
	}
	return svc, nil
}

func (e *ReportingTaskExtension) buildJob(cr *nifiv1alpha1.NifiCluster, auth *security.Authentication) (*batchv1.Job, error) {
	productVersion := product.ProductVersion(cr)
	jobName := fmt.Sprintf("%s-create-reporting-task-%s",
		cr.GetName(), strings.ReplaceAll(productVersion, ".", "-"))

	image, err := resolveImage(cr)
	if err != nil {
		return nil, err
	}

	tlsEnabled := cr.Spec.ClusterConfig.Tls != nil
	tlsSecretClass := ""
	if tlsEnabled {
		tlsSecretClass = cr.Spec.ClusterConfig.Tls.ServerSecretClass
	}

	_, port := webPort(cr)
	scheme := "http"
	if tlsEnabled {
		scheme = "https"
	}
	nifiConnectURL := fmt.Sprintf("%s://%s:%d/nifi-api", scheme, reportingTaskFQDN(cr.GetName(), cr.GetNamespace()), port)

	args := []string{
		reportingTaskScriptPath,
		fmt.Sprintf("-n %s", nifiConnectURL),
		fmt.Sprintf("-u %s", security.NifiAdminUsername),
	}
	if auth != nil {
		args = append(args, fmt.Sprintf("-p \"$(cat %s)\"", path.Join(security.UserMountDir, security.NifiAdminUsername)))
	}
	args = append(args,
		fmt.Sprintf("-v %s", productVersion),
		fmt.Sprintf("-m %d", product.GetPort("metrics")),
	)
	if tlsEnabled && tlsSecretClass != "" {
		args = append(args, fmt.Sprintf("-c %s", path.Join(reportingTaskCertVolumeMount, "ca.crt")))
	}

	var volumes []corev1.Volume
	var volumeMounts []corev1.VolumeMount
	if tlsEnabled && tlsSecretClass != "" {
		registration := opsecurity.CredentialsVolume(reportingTaskCertVolumeName, tlsSecretClass)
		volumes = append(volumes, opsecurity.NewSecretProvisioner().Register(registration).Volumes()...)
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      reportingTaskCertVolumeName,
			MountPath: reportingTaskCertVolumeMount,
		})
	}
	if auth != nil {
		volumes = append(volumes, auth.GetVolumes()...)
		volumeMounts = append(volumeMounts, auth.GetVolumeMounts()...)
	}

	backoffLimit := int32(100)
	ttlSecondsAfterFinished := int32(120)
	var nifiUID int64 = 1000
	var rootGroup int64 = 0
	var fsGroup int64 = 1000

	labels := map[string]string{
		constant.LabelKubernetesInstance:  cr.GetName(),
		constant.LabelKubernetesName:      nifiv1alpha1.DefaultProductName,
		constant.LabelKubernetesComponent: nifiv1alpha1.RoleName,
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: cr.GetNamespace(),
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			TTLSecondsAfterFinished: &ttlSecondsAfterFinished,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:    reportingTaskContainerName,
							Image:   image,
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
						},
					},
					Volumes:       volumes,
					RestartPolicy: corev1.RestartPolicyOnFailure,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsUser:  &nifiUID,
						RunAsGroup: &rootGroup,
						FSGroup:    &fsGroup,
					},
				},
			},
		},
	}

	if cr.Spec.Image != nil && cr.Spec.Image.PullSecretName != "" {
		job.Spec.Template.Spec.ImagePullSecrets = []corev1.LocalObjectReference{
			{Name: cr.Spec.Image.PullSecretName},
		}
	}

	return job, nil
}

// resolveImage resolves the NiFi image the same way the role group handler
// does: spec.image folded over the operator defaults, per field.
func resolveImage(cr *nifiv1alpha1.NifiCluster) (string, error) {
	defaults := commonsv1alpha1.ImageSpec{
		Repo:            nifiv1alpha1.DefaultRepository,
		ProductVersion:  nifiv1alpha1.DefaultProductVersion,
		KubedoopVersion: version.BuildVersion,
	}
	return cr.GetSpec().Image.ResolveImage(nifiv1alpha1.DefaultProductName, defaults)
}

var _ common.ClusterExtension[*nifiv1alpha1.NifiCluster] = &ReportingTaskExtension{}
