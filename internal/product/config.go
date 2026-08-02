// Package product computes NiFi's product-intrinsic configuration. It is the
// lowest layer of the framework config merge (ProductConfig < role overrides <
// role group overrides), so any value a user writes in the CRD wins over it.
//
// Content parity note: every key/value below reproduces the Gen 2
// internal/controller/node/configmap.go output byte-for-byte, including the
// gomplate templates ({{ getenv "..." }}) the prepare init container renders at
// pod start.
package product

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	commonsv1alpha1 "github.com/zncdatadev/operator-go/pkg/apis/commons/v1alpha1"
	"github.com/zncdatadev/operator-go/pkg/constant"
	"sigs.k8s.io/controller-runtime/pkg/client"

	nifiv1alpha1 "github.com/zncdatadev/nifi-operator/api/v1alpha1"
	"github.com/zncdatadev/nifi-operator/internal/security"
	"github.com/zncdatadev/nifi-operator/internal/util"
)

const (
	DefaultServerTlsStorePassword = "changeit"
	DefaultServerTlsKeyPassword   = "changeit"

	// DefaultGracefulShutdownTimeout is the consumption-time fallback for
	// config.gracefulShutdownTimeout.
	DefaultGracefulShutdownTimeout = "30s"

	propTrue  = "true"
	propFalse = "false"
)

var (
	NifiRoot                 = path.Join(constant.KubedoopRoot, "nifi")
	NifiConfigDir            = path.Join(NifiRoot, "conf")
	NifiSensitivePropertyDir = path.Join(NifiRoot, "sensitiveproperty")
	NifiServerTlsDir         = path.Join(NifiRoot, "server-tls")
)

func nifiRepository(name string) string {
	return name + "-repository"
}

// NifiRepositoryMountPath maps repository names to their (unmounted) data
// paths; preserved verbatim from Gen 2, including the database->data quirk.
var NifiRepositoryMountPath = map[string]string{
	"database":   path.Join(constant.KubedoopDataDir, nifiRepository("data")),
	"flowfile":   path.Join(constant.KubedoopDataDir, nifiRepository("flowfile")),
	"content":    path.Join(constant.KubedoopDataDir, nifiRepository("content")),
	"provenance": path.Join(constant.KubedoopDataDir, nifiRepository("provenance")),
	"state":      path.Join(constant.KubedoopDataDir, nifiRepository("state")),
	"status":     path.Join(constant.KubedoopDataDir, nifiRepository("status")),
	"server-tls": path.Join(constant.KubedoopDataDir, nifiRepository("server_tls")),
}

// ProductVersion resolves the effective NiFi product version for the CR.
func ProductVersion(cr *nifiv1alpha1.NifiCluster) string {
	if cr.Spec.Image != nil && cr.Spec.Image.ProductVersion != "" {
		return cr.Spec.Image.ProductVersion
	}
	return nifiv1alpha1.DefaultProductVersion
}

// UseZooKeeperStateProvider returns true when a ZooKeeper configmap is configured.
func UseZooKeeperStateProvider(clusterConfig *nifiv1alpha1.ClusterConfigSpec) bool {
	return clusterConfig.ZookeeperConfigMapName != nil &&
		*clusterConfig.ZookeeperConfigMapName != ""
}

// ReportingTaskEnabled reports whether the NiFi 1.x PrometheusReportingTask
// Job/Service pair should exist for this CR.
func ReportingTaskEnabled(cr *nifiv1alpha1.NifiCluster) bool {
	cc := cr.Spec.ClusterConfig
	return cc != nil && cc.CreateReportingTaskJob != nil && cc.CreateReportingTaskJob.Enable &&
		strings.HasPrefix(ProductVersion(cr), "1.")
}

// ComputeConfig is the framework ProductConfig hook: it contributes
// nifi.properties and bootstrap.conf as the lowest merge layer. The ctx and
// client exist for the authenticator-contributed keys (the OIDC discovery URL
// needs the AuthenticationClass object), so they flow through the same seam as
// everything else and any user override still wins.
func ComputeConfig(
	ctx context.Context,
	c client.Client,
	cr *nifiv1alpha1.NifiCluster,
	roleName, roleGroupName string,
) (*commonsv1alpha1.OverridesSpec, error) {
	props := nifiProperties(cr)

	if cr.Spec.ClusterConfig != nil && cr.Spec.ClusterConfig.Authentication != nil {
		auth, err := security.NewAuthentication(ctx, c, cr.GetName(), cr.Spec.ClusterConfig.Authentication)
		if err != nil {
			return nil, fmt.Errorf("resolving authentication: %w", err)
		}
		for k, v := range auth.ExtendNifiProperties() {
			props[k] = v
		}
	}

	return &commonsv1alpha1.OverridesSpec{
		ConfigOverrides: map[string]map[string]string{
			"bootstrap.conf":  bootstrapConfig(cr, roleGroupName),
			"nifi.properties": props,
		},
	}, nil
}

// bootstrapConfig renders the bootstrap.conf key set. jvmArgumentOverrides
// (dead in Gen 2, activated here) become numbered java.arg.N entries.
func bootstrapConfig(cr *nifiv1alpha1.NifiCluster, roleGroupName string) map[string]string {
	config := map[string]string{
		"java":                      "java",
		"run.as":                    "",
		"preserve.environment":      propFalse,
		"lib.dir":                   "./lib",
		"conf.dir":                  "./conf",
		"graceful.shutdown.seconds": gracefulShutdownTimeout(cr, roleGroupName),
	}

	for i, arg := range mergedJVMArguments(cr, roleGroupName) {
		config[fmt.Sprintf("java.arg.%d", i+1)] = arg
	}

	return config
}

// gracefulShutdownTimeout resolves the merged config.gracefulShutdownTimeout
// (role group wins over role; CRD default as fallback). Gen 2 wrote the raw
// duration string, so this does too.
func gracefulShutdownTimeout(cr *nifiv1alpha1.NifiCluster, roleGroupName string) string {
	nodes := cr.Spec.Nodes
	if nodes == nil {
		return DefaultGracefulShutdownTimeout
	}
	if rg, ok := nodes.RoleGroups[roleGroupName]; ok {
		if rg.Config != nil && rg.Config.RoleGroupConfigSpec != nil && rg.Config.GracefulShutdownTimeout != nil && *rg.Config.GracefulShutdownTimeout != "" {
			return *rg.Config.GracefulShutdownTimeout
		}
	}
	if nodes.Config != nil && nodes.Config.RoleGroupConfigSpec != nil && nodes.Config.GracefulShutdownTimeout != nil && *nodes.Config.GracefulShutdownTimeout != "" {
		return *nodes.Config.GracefulShutdownTimeout
	}
	return DefaultGracefulShutdownTimeout
}

// mergedJVMArguments folds the role-level and role-group-level
// jvmArgumentOverrides: adds append (role first), removes and removeRegex from
// either level filter the result.
func mergedJVMArguments(cr *nifiv1alpha1.NifiCluster, roleGroupName string) []string {
	nodes := cr.Spec.Nodes
	if nodes == nil {
		return nil
	}

	var specs []*nifiv1alpha1.JVMArgumentOverridesSpec
	if nodes.JVMArgumentOverrides != nil {
		specs = append(specs, nodes.JVMArgumentOverrides)
	}
	if rg, ok := nodes.RoleGroups[roleGroupName]; ok && rg.JVMArgumentOverrides != nil {
		specs = append(specs, rg.JVMArgumentOverrides)
	}
	if len(specs) == 0 {
		return nil
	}

	var args []string
	var removes []string
	var removePatterns []*regexp.Regexp
	for _, s := range specs {
		args = append(args, s.Add...)
		removes = append(removes, s.Remove...)
		for _, expr := range s.RemoveRegex {
			if re, err := regexp.Compile(expr); err == nil {
				removePatterns = append(removePatterns, re)
			}
		}
	}

	filtered := args[:0]
	for _, arg := range args {
		if slices.Contains(removes, arg) {
			continue
		}
		removed := false
		for _, re := range removePatterns {
			if re.MatchString(arg) {
				removed = true
				break
			}
		}
		if !removed {
			filtered = append(filtered, arg)
		}
	}
	return filtered
}

// nifiProperties renders the nifi.properties key set (auth-independent part;
// the handler merges the authenticator's extra keys, which need an API read).
func nifiProperties(cr *nifiv1alpha1.NifiCluster) map[string]string {
	clusterConfig := cr.Spec.ClusterConfig
	enableTls := clusterConfig.Tls != nil

	p := map[string]string{}

	p["nifi.templates.directory"] = path.Join(NifiConfigDir, "templates")
	p["nifi.ui.banner.text"] = "Welcome to Nifi"
	p["nifi.ui.autorefresh.interval"] = "30 sec"
	p["nifi.nar.library.directory"] = path.Join(NifiRoot, "lib")
	p["nifi.nar.library.autoload.directory"] = path.Join(NifiRoot, "extensions")
	p["nifi.nar.working.directory"] = path.Join(NifiRoot, "work", "nar")
	p["nifi.documentation.working.directory"] = path.Join(NifiRoot, "work", "docs", "components")

	// state management
	p["nifi.state.management.configuration.file"] = path.Join(NifiConfigDir, "state-management.xml")
	p["nifi.state.management.provider.local"] = "local-provider"
	if UseZooKeeperStateProvider(clusterConfig) {
		p["nifi.state.management.provider.cluster"] = "zk-provider"
	} else {
		// NiFi 2.x Kubernetes-native: references the KubernetesConfigMapStateProvider
		// defined in state-management.xml.
		p["nifi.state.management.provider.cluster"] = "kubernetes-provider"
	}
	p["nifi.state.management.embedded.zookeeper.start"] = propFalse

	// database repository
	p["nifi.database.directory"] = NifiRepositoryMountPath["database"]
	p["nifi.h2.url.append"] = ";LOCK_TIMEOUT=25000;WRITE_DELAY=0;AUTO_SERVER=FALSE"

	// flow configuration
	p["nifi.flow.configuration.file"] = path.Join(NifiConfigDir, "flow.json.gz") // in v2 use flow.json.gz
	p["nifi.flow.configuration.archive.enabled"] = propTrue
	p["nifi.flow.configuration.archive.dir"] = path.Join(NifiConfigDir, "archive")
	p["nifi.flow.configuration.archive.max.time"] = ""
	p["nifi.flow.configuration.archive.max.count"] = ""
	p["nifi.flowcontroller.autoResumeState"] = propTrue
	p["nifi.flowcontroller.graceful.shutdown.period"] = "10 sec"
	p["nifi.flowservice.writedelay.interval"] = "500 ms"

	// flowfile repository
	p["nifi.flowfile.repository.implementation"] = "org.apache.nifi.controller.repository.WriteAheadFlowFileRepository"
	p["nifi.flowfile.repository.wal.implementation"] = "org.apache.nifi.wali.SequentialAccessWriteAheadLog"
	p["nifi.flowfile.repository.directory"] = NifiRepositoryMountPath["flowfile"]
	p["nifi.flowfile.repository.checkpoint.interval"] = "20 sec"
	p["nifi.flowfile.repository.always.sync"] = propFalse
	p["nifi.flowfile.repository.retain.orphaned.flowfiles"] = propTrue

	p["nifi.swap.manager.implementation"] = "org.apache.nifi.controller.FileSystemSwapManager"
	p["nifi.queue.swap.threshold"] = "20000"

	// content repository
	p["nifi.content.repository.implementation"] = "org.apache.nifi.controller.repository.FileSystemRepository"
	p["nifi.content.claim.max.appendable.size"] = "1 MB"
	p["nifi.content.repository.directory.default"] = NifiRepositoryMountPath["content"]
	p["nifi.content.repository.archive.max.retention.period"] = ""
	p["nifi.content.repository.archive.max.usage.percentage"] = "50%"
	p["nifi.content.repository.archive.enabled"] = propTrue
	p["nifi.content.repository.always.sync"] = propFalse
	p["nifi.content.viewer.url"] = "../nifi/content-viewer"

	// provenance repository
	p["nifi.provenance.repository.implementation"] = "org.apache.nifi.provenance.WriteAheadProvenanceRepository"
	p["nifi.provenance.repository.directory.default"] = NifiRepositoryMountPath["provenance"]
	p["nifi.provenance.repository.max.storage.time"] = ""
	p["nifi.provenance.repository.rollover.time"] = "10 min"
	p["nifi.provenance.repository.rollover.size"] = "100 MB"
	p["nifi.provenance.repository.query.threads"] = "2"
	p["nifi.provenance.repository.index.threads"] = "2"
	p["nifi.provenance.repository.compress.on.rollover"] = propTrue
	p["nifi.provenance.repository.always.sync"] = propFalse
	p["nifi.provenance.repository.indexed.fields"] = "EventType, FlowFileUUID, Filename, ProcessorID, Relationship"
	p["nifi.provenance.repository.indexed.attributes"] = ""
	p["nifi.provenance.repository.index.shard.size"] = "500 MB"
	p["nifi.provenance.repository.max.attribute.length"] = "65536"
	p["nifi.provenance.repository.concurrent.merge.threads"] = "2"
	p["nifi.provenance.repository.buffer.size"] = "100000"
	p["nifi.components.status.repository.implementation"] = "org.apache.nifi.controller.status.history.VolatileComponentStatusRepository"
	p["nifi.components.status.repository.buffer.size"] = "14400"
	p["nifi.components.status.snapshot.frequency"] = "1 min"

	p["nifi.status.repository.questdb.persist.node.days"] = "14"
	p["nifi.status.repository.questdb.persist.component.days"] = "3"
	p["nifi.status.repository.questdb.persist.location"] = NifiRepositoryMountPath["status"]

	// web properties
	if enableTls {
		// Leave nifi.web.https.host blank so Jetty binds to all interfaces (0.0.0.0).
		// Setting it to NODE_ADDRESS (pod FQDN) would restrict Jetty to the pod IP,
		// making localhost:9443 unreachable from within the pod (e.g. kubectl exec).
		p["nifi.web.https.host"] = ""
		p["nifi.web.https.port"] = strconv.FormatInt(int64(GetPort("https")), 10)
		p["nifi.web.https.network.interface.default"] = ""

		// TLS
		p["nifi.security.keystore"] = path.Join(NifiServerTlsDir, "keystore.p12")
		p["nifi.security.keystoreType"] = "PKCS12"
		p["nifi.security.keystorePasswd"] = DefaultServerTlsKeyPassword
		p["nifi.security.truststore"] = path.Join(NifiServerTlsDir, "truststore.p12")
		p["nifi.security.truststoreType"] = "PKCS12"
		p["nifi.security.truststorePasswd"] = DefaultServerTlsStorePassword
	}
	// nifi.web.http.host - leave blank so Jetty binds to all interfaces
	p["nifi.web.http.host"] = ""
	p["nifi.web.http.port"] = strconv.FormatInt(int64(GetPort("http")), 10)
	p["nifi.web.http.network.interface.default"] = ""

	p["nifi.web.jetty.working.directory"] = path.Join(NifiRoot, "work", "jetty")
	p["nifi.web.jetty.threads"] = "200"
	p["nifi.web.max.header.size"] = "16 KB"
	p["nifi.web.proxy.context.path"] = ""

	// nifi.web.proxy.host
	// For NiFi 1.x with the PrometheusReportingTask Job enabled, the Job connects
	// to NiFi via a dedicated Service whose FQDN differs from the pod's NODE_ADDRESS.
	// NiFi validates the Host header against nifi.web.proxy.host, so we must allow
	// that FQDN here — consistent with Stackable Rust operator's get_proxy_hosts().
	if strings.HasPrefix(ProductVersion(cr), "1.") &&
		clusterConfig.CreateReportingTaskJob != nil && clusterConfig.CreateReportingTaskJob.Enable {
		reportingTaskFQDN := fmt.Sprintf("%s-reporting-task.%s.svc.cluster.local:%d",
			cr.GetName(), cr.GetNamespace(), GetPort("https"))
		p["nifi.web.proxy.host"] = reportingTaskFQDN
	}

	// nifi.sensitive.props.key
	p["nifi.sensitive.props.key"] = fmt.Sprintf("${file:UTF-8:%s}", path.Join(constant.KubedoopRoot, "sensitiveproperty", "nifiSensitivePropsKey"))
	p["nifi.sensitive.props.key.protected"] = ""
	if clusterConfig.SensitiveProperties != nil && clusterConfig.SensitiveProperties.Algorithm != "" {
		p["nifi.sensitive.props.algorithm"] = clusterConfig.SensitiveProperties.Algorithm
	}

	// security properties
	p["nifi.administrative.yield.duration"] = "30 sec"
	p["nifi.authorizer.configuration.file"] = path.Join(NifiConfigDir, "authorizers.xml")
	p["nifi.login.identity.provider.configuration.file"] = path.Join(NifiConfigDir, "login-identity-providers.xml")
	p["nifi.security.user.login.identity.provider"] = "login-identity-provider"
	p["nifi.security.user.authorizer"] = "authorizer"
	p["nifi.security.allow.anonymous.authentication"] = propFalse

	// nifi cluster mode
	if enableTls {
		p["nifi.cluster.protocol.is.secure"] = propTrue
	} else {
		p["nifi.cluster.protocol.is.secure"] = propFalse
	}
	p["nifi.cluster.node.protocol.port"] = strconv.FormatInt(int64(GetPort("protocol")), 10)
	p["nifi.cluster.flow.election.max.wait.time"] = "1 min"
	p["nifi.cluster.flow.election.max.candidates"] = ""
	p["nifi.cluster.is.node"] = propTrue
	p["nifi.cluster.node.address"] = `{{ getenv "NODE_ADDRESS" }}`
	if !UseZooKeeperStateProvider(clusterConfig) {
		// Kubernetes-native clustering (NiFi 2.x only): no ZooKeeper — use
		// KubernetesLeaderElectionManager for leader election.
		p["nifi.cluster.leader.election.implementation"] = "KubernetesLeaderElectionManager"
		p["nifi.cluster.leader.election.kubernetes.lease.prefix"] = `{{ getenv "STACKLET_NAME" }}`
	} else {
		// Clustered mode with ZooKeeper.
		p["nifi.cluster.leader.election.implementation"] = "CuratorLeaderElectionManager"
		p["nifi.zookeeper.connect.string"] = `{{ getenv "ZOOKEEPER_HOSTS" }}`
		p["nifi.zookeeper.root.node"] = `{{ getenv "ZOOKEEPER_CHROOT" }}`
	}

	return p
}

// StateManagementXML renders state-management.xml. It is a document-style XML
// file, so it bypasses the key-value merge pipeline and is placed whole into
// the role group ConfigMap by the handler.
func StateManagementXML(clusterConfig *nifiv1alpha1.ClusterConfigSpec) string {
	localProviderBlock := `
	<local-provider>
		<id>local-provider</id>
		<class>org.apache.nifi.controller.state.providers.local.WriteAheadLocalStateProvider</class>
		<property name="Directory">` + NifiRepositoryMountPath["state"] + `</property>
		<property name="Always Sync">false</property>
		<property name="Partitions">16</property>
		<property name="Checkpoint Interval">2 mins</property>
	</local-provider>`

	var clusterProviderBlock string
	if UseZooKeeperStateProvider(clusterConfig) {
		// ZooKeeper-based clustering: use ZooKeeperStateProvider for cluster state.
		clusterProviderBlock = `
	<cluster-provider>
		<id>zk-provider</id>
		<class>org.apache.nifi.controller.state.providers.zookeeper.ZooKeeperStateProvider</class>
		<property name="Connect String">{{ getenv "ZOOKEEPER_HOSTS" }}</property>
		<property name="Root Node">{{ getenv "ZOOKEEPER_CHROOT" }}</property>
		<property name="Session Timeout">15 seconds</property>
		<property name="Access Control">Open</property>
	</cluster-provider>`
	} else {
		// Kubernetes-native clustering (NiFi 2.x only):
		// KubernetesConfigMapStateProvider is the only CLUSTER-scope provider
		// available without ZooKeeper; NiFi 1.x must use ZooKeeper instead.
		clusterProviderBlock = `
	<cluster-provider>
		<id>kubernetes-provider</id>
		<class>org.apache.nifi.kubernetes.state.provider.KubernetesConfigMapStateProvider</class>
		<property name="ConfigMap Name Prefix">{{ getenv "STACKLET_NAME" }}</property>
	</cluster-provider>`
	}

	xml := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<stateManagement>` + localProviderBlock + clusterProviderBlock + `
</stateManagement>
`

	return util.IndentTab4Spaces(xml)
}
