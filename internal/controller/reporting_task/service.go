package reportingtask

import (
	"context"
	"fmt"
	"sort"

	"github.com/zncdatadev/operator-go/pkg/builder"
	"github.com/zncdatadev/operator-go/pkg/client"
	"github.com/zncdatadev/operator-go/pkg/constants"
	"github.com/zncdatadev/operator-go/pkg/reconciler"
	corev1 "k8s.io/api/core/v1"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	nifiv1alpha1 "github.com/zncdatadev/nifi-operator/api/v1alpha1"
)

const (
	ReportingTaskContainerName = "reporting-task"
)

// ReportingTaskServiceName returns the name of the reporting task service.
func ReportingTaskServiceName(clusterName string) string {
	return fmt.Sprintf("%s-%s", clusterName, ReportingTaskContainerName)
}

// ReportingTaskFQDNServiceName returns the FQDN of the reporting task service.
func ReportingTaskFQDNServiceName(clusterName, namespace string) string {
	serviceName := ReportingTaskServiceName(clusterName)
	return fmt.Sprintf("%s.%s.svc.cluster.local", serviceName, namespace)
}

// getReportingTaskServiceSelectorPod returns the name of the first pod
// belonging to the first role group that contains more than 0 replicas.
// If no replicas are set in any rolegroup, return the first rolegroup just in case.
// This is required to only select a single node in the Reporting Task Service.
func getReportingTaskServiceSelectorPod(clusterName string, nodes *nifiv1alpha1.NodesSpec) (string, error) {
	roleName := "node"

	type rgEntry struct {
		name string
		rg   nifiv1alpha1.RoleGroupSpec
	}

	sorted := make([]rgEntry, 0, len(nodes.RoleGroups))
	for name, rg := range nodes.RoleGroups {
		sorted = append(sorted, rgEntry{name: name, rg: rg})
	}
	// Sort the rolegroups to avoid random sorting and therefore unnecessary reconciles
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].name < sorted[j].name
	})

	var selectorRoleGroup string
	for _, entry := range sorted {
		// Just pick the first rolegroup in case no replicas are set
		if selectorRoleGroup == "" {
			selectorRoleGroup = entry.name
		}

		if entry.rg.Replicas != nil && *entry.rg.Replicas > 0 {
			selectorRoleGroup = entry.name
			break
		}
	}

	if selectorRoleGroup == "" {
		return "", fmt.Errorf("no role groups defined for reporting task service")
	}

	return fmt.Sprintf("%s-%s-%s-0", clusterName, roleName, selectorRoleGroup), nil
}

// ReportingTaskServiceBuilder builds a Service that targets a single NiFi node.
// This is necessary because the generated JWT in NiFi 1.25+ uses the pod FQDN
// as the issuer, and the NiFi role service would randomly delegate to different
// NiFi nodes which would then fail requests to other nodes.
type ReportingTaskServiceBuilder struct {
	*builder.BaseServiceBuilder
}

func NewReportingTaskServiceBuilder(
	client *client.Client,
	clusterName string,
	nodes *nifiv1alpha1.NodesSpec,
	httpsPort int32,
	options ...builder.Option,
) (*ReportingTaskServiceBuilder, error) {
	selectorPod, err := getReportingTaskServiceSelectorPod(clusterName, nodes)
	if err != nil {
		return nil, err
	}

	opts := &builder.Options{}
	for _, o := range options {
		o(opts)
	}

	// Build matching labels that include the specific pod name
	// to ensure the service only targets a single NiFi node
	matchingLabels := map[string]string{
		constants.LabelKubernetesInstance:    clusterName,
		constants.LabelKubernetesManagedBy:   constants.KubedoopDomain,
		constants.LabelKubernetesComponent:   "node",
		"statefulset.kubernetes.io/pod-name": selectorPod,
	}

	ports := []corev1.ContainerPort{
		{
			Name:          "https",
			ContainerPort: httpsPort,
		},
	}

	serviceName := ReportingTaskServiceName(clusterName)

	return &ReportingTaskServiceBuilder{
		BaseServiceBuilder: builder.NewServiceBuilder(
			client,
			serviceName,
			ports,
			func(o *builder.ServiceBuilderOptions) {
				o.Labels = opts.Labels
				o.Annotations = opts.Annotations
				o.ClusterName = opts.ClusterName
				o.RoleName = opts.RoleName
				o.RoleGroupName = opts.RoleGroupName
				o.MatchingLabels = matchingLabels
			},
		),
	}, nil
}

func (b *ReportingTaskServiceBuilder) Build(_ context.Context) (ctrlclient.Object, error) {
	return b.GetObject(), nil
}

// NewReportingTaskServiceReconciler creates a reconciler for the reporting task service.
func NewReportingTaskServiceReconciler(
	client *client.Client,
	clusterName string,
	nodes *nifiv1alpha1.NodesSpec,
	httpsPort int32,
	options ...builder.Option,
) (*reconciler.SimpleResourceReconciler[builder.ServiceBuilder], error) {
	svcBuilder, err := NewReportingTaskServiceBuilder(
		client,
		clusterName,
		nodes,
		httpsPort,
		options...,
	)
	if err != nil {
		return nil, err
	}

	return reconciler.NewSimpleResourceReconciler[builder.ServiceBuilder](
		client,
		svcBuilder,
	), nil
}
