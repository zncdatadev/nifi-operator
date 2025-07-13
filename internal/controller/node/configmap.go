package node

import (
	"context"
	"fmt"
	"maps"
	"path"
	"slices"
	"strconv"

	"github.com/zncdatadev/operator-go/pkg/builder"
	"github.com/zncdatadev/operator-go/pkg/client"
	"github.com/zncdatadev/operator-go/pkg/config/properties"
	"github.com/zncdatadev/operator-go/pkg/constants"
	"github.com/zncdatadev/operator-go/pkg/reconciler"
	"github.com/zncdatadev/operator-go/pkg/util"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	nifiv1alpha1 "github.com/zncdatadev/nifi-operator/api/v1alpha1"
	"github.com/zncdatadev/nifi-operator/internal/common/security"
)

const (
	DefaultServerTlsStorePassword = "changeit"
	DefaultServerTlsKeyPassword   = "changeit"
)

var (
	NifiRoot                 = path.Join(constants.KubedoopRoot, "nifi")
	NifiConfigDir            = path.Join(NifiRoot, "conf")
	NifiSensitivePropertyDir = path.Join(NifiRoot, "sensitiveproperty")
	NifiServerTlsDir         = path.Join(NifiRoot, "server-tls")
)

func nifiRepository(name string) string {
	return name + "-repository"
}

var NifiRepositoryMouhtPath = map[string]string{
	"database":   path.Join(constants.KubedoopDataDir, nifiRepository("data")),
	"flowfile":   path.Join(constants.KubedoopDataDir, nifiRepository("flowfile")),
	"content":    path.Join(constants.KubedoopDataDir, nifiRepository("content")),
	"provenance": path.Join(constants.KubedoopDataDir, nifiRepository("provenance")),
	"state":      path.Join(constants.KubedoopDataDir, nifiRepository("state")),
	"server-tls": path.Join(constants.KubedoopDataDir, nifiRepository("server_tls")),
}

type NifiConfigMapBuilder struct {
	builder.ConfigMapBuilder

	ClusterConfig *nifiv1alpha1.ClusterConfigSpec

	ClusterName    string
	RoleName       string
	RoleGroupName  string
	Config         *nifiv1alpha1.ConfigSpec
	Authentication *security.Authentication
}

func NewNifiConfigBuilder(
	client *client.Client,
	clusterConfig *nifiv1alpha1.ClusterConfigSpec,
	roleGroupInfo reconciler.RoleGroupInfo,
	config *nifiv1alpha1.ConfigSpec,
	authentication *security.Authentication,
) *NifiConfigMapBuilder {
	return &NifiConfigMapBuilder{
		ConfigMapBuilder: *builder.NewConfigMapBuilder(
			client,
			roleGroupInfo.GetFullName(),
			func(o *builder.Options) {
				o.Labels = roleGroupInfo.GetLabels()
				o.Annotations = roleGroupInfo.GetAnnotations()
			},
		),
		ClusterConfig:  clusterConfig,
		ClusterName:    roleGroupInfo.ClusterName,
		RoleName:       roleGroupInfo.RoleName,
		RoleGroupName:  roleGroupInfo.RoleGroupName,
		Config:         config,
		Authentication: authentication,
	}
}

func (b *NifiConfigMapBuilder) Build(ctx context.Context) (ctrlclient.Object, error) {

	bootstarpProperties, err := b.getBootstrapConfig()
	if err != nil {
		return nil, err
	}

	b.AddItem("bootstrap.conf", bootstarpProperties)

	nifiProperties, err := b.getNifiProperties(ctx)
	if err != nil {
		return nil, err
	}

	b.AddItem("nifi.properties", nifiProperties)

	if b.ClusterConfig.Authentication != nil {
		b.AddItem("login-identity-providers.xml", b.Authentication.GetLoginIdentiryProvider())
	}

	b.AddItem("state-management.xml", b.getStateManagementConfig())

	return b.GetObject(), nil
}

func (b *NifiConfigMapBuilder) getStateManagementConfig() string {

	xml := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<stateManagement>
	<local-provider>
		<id>local-provider</id>
		<class>org.apache.nifi.controller.state.providers.local.WriteAheadLocalStateProvider</class>
		<property name="Directory">{}</property>
		<property name="Always Sync">false</property>
		<property name="Partitions">16</property>
		<property name="Checkpoint Interval">2 mins</property>
	</local-provider>

	<cluster-provider>
		<id>zk-provider</id>
		<class>org.apache.nifi.controller.state.providers.zookeeper.ZooKeeperStateProvider</class>
		<property name="Connect String">{{ getenv "ZOOKEEPER_HOSTS" }}</property>
		<property name="Root Node">{{ getenv "ZOOKEEPER_CHROOT" }}</property>
		<property name="Session Timeout">15 seconds</property>
		<property name="Access Control">Open</property>
	</cluster-provider>
</stateManagement>
`

	return util.IndentTab4Spaces(xml)
}

func (b *NifiConfigMapBuilder) getBootstrapConfig() (string, error) {
	config := map[string]string{
		"java":                      "java",
		"run.as":                    "",
		"preserve.environment":      "false",
		"lib.dir":                   "./lib",
		"conf.dir":                  "./conf",
		"graceful.shutdown.seconds": b.Config.GracefulShutdownTimeout,
	}

	// TODO: add jvm args, Pay attention to the order of map traversal

	keys := slices.Sorted(maps.Keys(config))

	data := ""
	// marshal the config to a string, key=value
	for _, key := range keys {
		data += fmt.Sprintf("%s=%s\n", key, config[key])
	}

	return data, nil
}

func (b *NifiConfigMapBuilder) getNifiProperties(ctx context.Context) (string, error) {

	enableTls := b.ClusterConfig.Tls != nil

	properties := properties.NewProperties()

	properties.Add("nifi.templates.directory", path.Join(NifiConfigDir, "templates"))
	// nifi.ui.banner.text
	properties.Add("nifi.ui.banner.text", "Welcome to Nifi")
	// nifi.ui.autorefresh.interval
	properties.Add("nifi.ui.autorefresh.interval", "30 sec")
	// nifi.nar.library.directory
	properties.Add("nifi.nar.library.directory", path.Join(NifiRoot, "lib"))
	// nifi.nar.library.autoload.directory
	properties.Add("nifi.nar.library.autoload.directory", path.Join(NifiRoot, "extensions"))
	// nifi.nar.working.directory
	properties.Add("nifi.nar.working.directory", path.Join(NifiRoot, "work", "nar"))
	// nifi.documentation.working.directory
	properties.Add("nifi.documentation.working.directory", path.Join(NifiRoot, "work", "docs", "components"))

	// state management
	// nifi.state.management.configuration.file
	properties.Add("nifi.state.management.configuration.file", path.Join(NifiConfigDir, "state-management.xml"))
	// nifi.state.management.provider.local
	properties.Add("nifi.state.management.provider.local", "local-provider")
	// nifi.state.management.provider.cluster
	properties.Add("nifi.state.management.provider.cluster", "zk-provider")
	// nifi.state.management.embedded.zookeeper.start
	properties.Add("nifi.state.management.embedded.zookeeper.start", "false")

	// database repository
	// nifi.database.directory
	properties.Add("nifi.database.directory", NifiRepositoryMouhtPath["database"])
	// nifi.h2.url.append
	properties.Add("nifi.h2.url.append", ";LOCK_TIMEOUT=25000;WRITE_DELAY=0;AUTO_SERVER=FALSE")

	// flow configuration
	properties.Add("nifi.flow.configuration.file", path.Join(NifiConfigDir, "flow.json.gz")) // in v2 use flow.json.gz
	properties.Add("nifi.flow.configuration.archive.enabled", "true")
	properties.Add("nifi.flow.configuration.archive.dir", path.Join(NifiConfigDir, "archive"))
	properties.Add("nifi.flow.configuration.archive.max.time", "")
	// TODO: add config.storage.flowfileRepo support
	// properties.Add("nifi.flow.configuration.archive.max.storage", "")
	properties.Add("nifi.flow.configuration.archive.max.count", "")
	properties.Add("nifi.flowcontroller.autoResumeState", "true")
	properties.Add("nifi.flowcontroller.graceful.shutdown.period", "10 sec")
	properties.Add("nifi.flowservice.writedelay.interval", "500 ms")

	// flowfile repository
	// nifi.flowfile.repository.implementation
	properties.Add("nifi.flowfile.repository.implementation", "org.apache.nifi.controller.repository.WriteAheadFlowFileRepository")
	// nifi.flowfile.repository.wal.implementation
	properties.Add("nifi.flowfile.repository.wal.implementation", "org.apache.nifi.wali.SequentialAccessWriteAheadLog")
	// nifi.flowfile.repository.directory
	properties.Add("nifi.flowfile.repository.directory", NifiRepositoryMouhtPath["flowfile"])
	// nifi.flowfile.repository.checkpoint.interval
	properties.Add("nifi.flowfile.repository.checkpoint.interval", "20 sec")
	// nifi.flowfile.repository.always.sync
	properties.Add("nifi.flowfile.repository.always.sync", "false")
	// nifi.flowfile.repository.retain.orphaned.flowfiles
	properties.Add("nifi.flowfile.repository.retain.orphaned.flowfiles", "true")

	// nifi.swap.manager.implementation
	properties.Add("nifi.swap.manager.implementation", "org.apache.nifi.controller.FileSystemSwapManager")
	// nifi.queue.swap.threshold
	properties.Add("nifi.queue.swap.threshold", "20000")

	// content repository
	// nifi.content.repository.implementation
	properties.Add("nifi.content.repository.implementation", "org.apache.nifi.content.repository.FileSystemRepository")
	// nifi.content.claim.max.appendable.size
	properties.Add("nifi.content.claim.max.appendable.size", "1 MB")
	// nifi.content.repository.directory.default
	properties.Add("nifi.content.repository.directory.default", NifiRepositoryMouhtPath["content"])
	// nifi.content.repository.archive.max.retention.period
	properties.Add("nifi.content.repository.archive.max.retention.period", "")
	// nifi.content.repository.archive.max.usage.percentage
	properties.Add("nifi.content.repository.archive.max.usage.percentage", "50%")
	// nifi.content.repository.archive.enabled
	properties.Add("nifi.content.repository.archive.enabled", "true")
	// nifi.content.repository.always.sync
	properties.Add("nifi.content.repository.always.sync", "false")
	// nifi.content.viewer.url
	properties.Add("nifi.content.viewer.url", "../nifi/content-viewer")

	// provenance repository
	// nifi.provenance.repository.implementation
	properties.Add("nifi.provenance.repository.implementation", "org.apache.nifi.provenance.WriteAheadProvenanceRepository")
	// nifi.provenance.repository.directory.default
	properties.Add("nifi.provenance.repository.directory.default", NifiRepositoryMouhtPath["provenance"])
	// nifi.provenance.repository.max.storage.time
	properties.Add("nifi.provenance.repository.max.storage.time", "")
	// TODO: add nifi.provenance.repository.max.storage.size support
	// nifi.provenance.repository.max.storage.size
	// properties.Add("nifi.provenance.repository.max.storage.size", "")
	// nifi.provenance.repository.rollover.time
	properties.Add("nifi.provenance.repository.rollover.time", "10 min")
	// nifi.provenance.repository.rollover.size
	properties.Add("nifi.provenance.repository.rollover.size", "100 MB")
	// nifi.provenance.repository.query.threads
	properties.Add("nifi.provenance.repository.query.threads", "2")
	// nifi.provenance.repository.index.threads
	properties.Add("nifi.provenance.repository.index.threads", "2")
	// nifi.provenance.repository.compress.on.rollover
	properties.Add("nifi.provenance.repository.compress.on.rollover", "true")
	// nifi.provenance.repository.always.sync
	properties.Add("nifi.provenance.repository.always.sync", "false")
	// nifi.provenance.repository.indexed.fields
	properties.Add("nifi.provenance.repository.indexed.fields", "EventType, FlowFileUUID, Filename, ProcessorID, Relationship")
	// nifi.provenance.repository.indexed.attributes
	properties.Add("nifi.provenance.repository.indexed.attributes", "")
	// nifi.provenance.repository.index.shard.size
	properties.Add("nifi.provenance.repository.index.shard.size", "500 MB")
	// nifi.provenance.repository.max.attribute.lengt
	properties.Add("nifi.provenance.repository.max.attribute.length", "65536")
	// nifi.provenance.repository.concurrent.merge.threads
	properties.Add("nifi.provenance.repository.concurrent.merge.threads", "2")
	// nifi.provenance.repository.buffer.size
	properties.Add("nifi.provenance.repository.buffer.size", "100000")
	// nifi.components.status.repository.implementation
	properties.Add("nifi.components.status.repository.implementation", "org.apache.nifi.controller.status.history.VolatileComponentStatusRepository")
	// nifi.components.status.repository.buffer.size
	properties.Add("nifi.components.status.repository.buffer.size", "14400")
	// nifi.components.status.snapshot.frequency
	properties.Add("nifi.components.status.snapshot.frequency", "1 min")
	// nifi.status.repository.questdb.persist.node.days
	properties.Add("nifi.status.repository.questdb.persist.node.days", "14")
	// nifi.status.repository.questdb.persist.component.days
	properties.Add("nifi.status.repository.questdb.persist.component.days", "3")
	// nifi.status.repository.questdb.persist.location
	properties.Add("nifi.status.repository.questdb.persist.location", NifiRepositoryMouhtPath["state"])

	// web properties
	// nifi.web.https.hos
	if enableTls {
		// NODE_ADDRESS is constracted by shell script before start nifi,
		// it is pod FQDN
		properties.Add("nifi.web.https.host", `{{ getenv "NODE_ADDRESS" }}`)
		// nifi.web.https.poomitemptyrt
		properties.Add("nifi.web.https.port", strconv.FormatInt(int64(getPort("https")), 10))
		// nifi.web.https.network.interface.default
		properties.Add("nifi.web.https.network.interface.default", "")

		// TLS
		// nifi.security.keystore
		properties.Add("nifi.security.keystore", path.Join(NifiServerTlsDir, "keystore.p12"))
		// nifi.security.keystoreType
		properties.Add("nifi.security.keystoreType", "PKCS12")
		// nifi.security.keystorePasswd
		properties.Add("nifi.security.keystorePasswd", DefaultServerTlsKeyPassword)
		// nifi.security.truststore
		properties.Add("nifi.security.truststore", path.Join(NifiServerTlsDir, "truststore.p12"))
		// nifi.security.truststoreType
		properties.Add("nifi.security.truststoreType", "PKCS12")
		// nifi.security.truststorePasswd
		properties.Add("nifi.security.truststorePasswd", DefaultServerTlsStorePassword)
	}
	// nifi.web.http.host
	properties.Add("nifi.web.http.host", `{{ getenv "NODE_ADDRESS" }}`)
	// nifi.web.http.port
	properties.Add("nifi.web.http.port", strconv.FormatInt(int64(getPort("http")), 10))
	// nifi.web.http.network.interface.default
	properties.Add("nifi.web.http.network.interface.default", "")

	// nifi.web.jetty.working.director
	properties.Add("nifi.web.jetty.working.directory", path.Join(NifiRoot, "work", "jetty"))
	// nifi.web.jetty.threads
	properties.Add("nifi.web.jetty.threads", "200")
	// nifi.web.max.header.size
	properties.Add("nifi.web.max.header.size", "16 KB")
	// nifi.web.proxy.context.path
	properties.Add("nifi.web.proxy.context.path", "")

	// nifi.sensitive.props.key
	properties.Add("nifi.sensitive.props.key", fmt.Sprintf("${file:UTF-8:%s}", path.Join(NifiSensitivePropertyDir, "nifiSensitivePropsKey")))
	// nifi.sensitive.props.key.protected
	properties.Add("nifi.sensitive.props.key.protected", "")
	if b.ClusterConfig.SensitiveProperties != nil && b.ClusterConfig.SensitiveProperties.Algorithm != "" {
		properties.Add("nifi.sensitive.props.algorithm", b.ClusterConfig.SensitiveProperties.Algorithm)
	}

	// security properties
	// nifi.administrative.yield.duration
	properties.Add("nifi.administrative.yield.duration", "30 sec")
	properties.Add("nifi.authorizer.configuration.file", path.Join(NifiConfigDir, "authorizers.xml"))
	properties.Add("nifi.login.identity.provider.configuration.file", path.Join(NifiConfigDir, "login-identity-providers.xml"))
	// nifi.security.user.login.identity.provider
	properties.Add("nifi.security.user.login.identity.provider", "login-identity-provider")
	// nifi.security.user.authorizer
	properties.Add("nifi.security.user.authorizer", "authorizer")
	// nifi.security.allow.anonymous.authentication
	properties.Add("nifi.security.allow.anonymous.authentication", "false")
	// nifi.cluster.protocol.is.secure
	properties.Add("nifi.cluster.protocol.is.secure", "true")
	// nifi.cluster.node.protocol.port
	properties.Add("nifi.cluster.node.protocol.port", strconv.FormatInt(int64(getPort("protocol")), 10))
	// nifi.cluster.flow.election.max.wait.time
	properties.Add("nifi.cluster.flow.election.max.wait.time", "1 min")
	// nifi.cluster.flow.election.max.candidates
	properties.Add("nifi.cluster.flow.election.max.candidates", "")

	// nifi.cluster.is.node
	properties.Add("nifi.cluster.is.node", "true")
	// nifi.cluster.node.address
	properties.Add("nifi.cluster.node.address", `{{ getenv "NODE_ADDRESS" }}`)

	// nifi cluster mode
	if b.ClusterConfig.ZookeeperConfigMapName == nil {
		// If not set zookeeperConfigMapName, use kubernetes as clustering backend
		// nifi.cluster.leader.election.implementation
		properties.Add("nifi.cluster.leader.election.implementation", "KubernetesLeaderElectionManager")
		// nifi.cluster.leader.election.kubernetes.lease.prefix
		properties.Add("nifi.cluster.leader.election.kubernetes.lease.prefix", `{{ getenv "STACKLET_NAME" }}`)
	} else if b.ClusterConfig.ZookeeperConfigMapName != nil && *b.ClusterConfig.ZookeeperConfigMapName != "" {
		// nifi.cluster.leader.election.implementation
		properties.Add("nifi.cluster.leader.election.implementation", "CuratorLeaderElectionManager")
		// nifi.zookeeper.connect.string
		properties.Add("nifi.zookeeper.connect.string", `{{ getenv "ZOOKEEPER_HOSTS" }}`)
		// nifi.zookeeper.root.node
		properties.Add("nifi.zookeeper.root.node", `{{ getenv "ZOOKEEPER_CHROOT" }}`)
	} else {
		// raise error if zookeeperConfigMapName is empty
		return "", fmt.Errorf("zookeeperConfigMapName is required when clustering backend is zookeeper")
	}

	if b.ClusterConfig.Authentication != nil {
		auth, error := security.NewAuthentication(ctx, b.Client, b.ClusterName, b.ClusterConfig.Authentication)
		if error != nil {
			return "", fmt.Errorf("failed to create authentication: %w", error)
		}

		authProperties := auth.ExtendNifiProperties()
		if authProperties != nil {
			for _, key := range authProperties.Keys() {
				value, _ := authProperties.Get(key)
				properties.Add(key, value)
			}
		}
	}

	// Custom properties
	properties.Add("nifi.python.command", "python3")
	properties.Add("nifi.python.framework.source.directory", path.Join(NifiRoot, "python", "framework"))
	properties.Add("nifi.python.framework.working.directory", path.Join(NifiRoot, "python", "working"))
	properties.Add("nifi.python.extensions.source.directory.default", path.Join(NifiRoot, "python", "extensions"))

	// TODO: implement custom components git sync

	data, err := properties.Marshal()
	if err != nil {
		return "", err
	}

	return data, nil
}

func NewConfigReconciler(
	client *client.Client,
	clusterConfig *nifiv1alpha1.ClusterConfigSpec,
	roleGroupInfo reconciler.RoleGroupInfo,
	config *nifiv1alpha1.ConfigSpec,
	authentication *security.Authentication,
) *reconciler.SimpleResourceReconciler[builder.ConfigBuilder] {

	nifiConfigSecretBuilder := NewNifiConfigBuilder(
		client,
		clusterConfig,
		roleGroupInfo,
		config,
		authentication,
	)

	return reconciler.NewSimpleResourceReconciler[builder.ConfigBuilder](
		client,
		nifiConfigSecretBuilder,
	)
}
