# Gen 3 Migration Design — nifi-operator

Status: approved-for-implementation (this PR)
Baseline: `f406784` (Gen 2b, operator-go v0.12.6)
Target: operator-go `main` (Gen 3: `GenericReconciler` + `BaseRoleGroupHandler`), ≥ `5edf2ee`
References: `operator-go/examples/trino-operator` (canonical wiring),
`zookeeper-operator@refactor/base-operator-go` (GetSpec bridge pattern)

## 1. Goal and method

Replace the hand-rolled Gen 2 reconciler tree (`BaseCluster` → `node.Reconciler` →
per-resource reconcilers) with the Gen 3 framework, keeping **rendered-resource
parity** as the acceptance bar: the chainsaw e2e suites (`git-sync`,
`reporting-task`) run unmodified, and every rendered-YAML difference against the
Gen 2 baseline is either eliminated or recorded in §6's intentional-diff list.

## 2. CRD strategy: bridge, don't reshape

The CRD spec schema does not change. `NifiCluster` implements
`common.ClusterInterface` by **bridging** the typed `spec.nodes` into the
generic role map at runtime (same pattern as trino/zookeeper):

```go
func (r *NifiCluster) GetSpec() *commonsv1alpha1.GenericClusterSpec
// nodes → Roles["node"]; ConfigSpec → RoleGroupConfigSpec (embedded);
// OverridesSpec fields flattened; replicas copied.
```

- `NifiClusterStatus` embeds `commonsv1alpha1.GenericClusterStatus` (inline).
  Status schema change is additive (conditions survive, roleGroups ledger added).
- `spec.nodes.jvmArgumentOverrides` stays in the CRD; it is applied to
  `bootstrap.conf` `java.arg.N` entries by the product config layer (it is a
  dead field in Gen 2 — see §6 "activated fields").
- CRD diff gate: after `make generate && make manifests`, the spec portion of
  the CRD must be byte-identical; status/conditions additive only.

## 3. Component mapping

| Gen 2 (internal/) | Gen 3 target |
|---|---|
| `controller/nificluster_controller.go` + `controller/cluster/cluster.go` | `reconciler.NewGenericReconciler[*NifiCluster]` in `cmd/main.go` |
| `controller/node/{role,statefulset,configmap,service,port}.go` | `NifiRoleGroupHandler` embedding `BaseRoleGroupHandler[*NifiCluster]` |
| config assembly (`node/configmap.go`) | `ProductConfig` (nifi.properties, bootstrap.conf via merge pipeline) + handler override (document XMLs) |
| `common/security/sensitive_key.go` | `reconciler.EnsureGeneratedSecret` from a `ClusterExtension.PreReconcile` |
| `common/security/oidc_admin_secret.go` | same `EnsureGeneratedSecret` call site (fixes the LDAP/OIDC registration swap bug, §6) |
| `common/security/{static,ldap,oidc,authentication}.go` | kept as `internal/security`, invoked from the handler (volumes/env/args/properties) |
| `common/git_sync.go` | kept as `internal/gitsync`, applied in the handler's `BuildResources` override; promotion to operator-go tracked in §7 |
| `controller/reporting_task/{job,service}.go` | `ClusterExtension.PostReconcile` (NiFi 1.x only, unchanged gate) |
| RBAC (SA/Role/RoleBinding `<cluster>-nifi`) | SA via framework `ServiceAccountNameFunc`; Role/RoleBinding via `ClusterExtension.PreReconcile` (leases + configmaps for k8s-native leader election) |
| `zookeeperConfigMapName` implicit dependency | `GenericReconcilerConfig.Dependencies` (fail fast with Degraded instead of crash-looping pods) |

Handler configuration (reconcile-invariant, set once in `main.go`):
`ProductName="nifi"`, `ImageDefaults{quay.io/zncdatadev, 2.4.0, version.BuildVersion}`,
`MainContainerName="node"`, `ConfigMountPath="/kubedoop/mount/config"`,
`ConfigGenerator` with default formats **plus `.conf` and `.properties` mapped to the
properties adapter** (bootstrap.conf, nifi.properties byte-format parity: sorted
`k=v\n`). Per-CR inputs (TLS-dependent ports/probes, listenerClass, env wiring,
git-sync containers, auth volumes) go through `RoleGroupBuildContext` in the
`BuildResources` override — never assigned to handler fields (shared-state race).

Config channels:

- `nifi.properties`, `bootstrap.conf` → `ProductConfig` (lowest merge layer);
  gomplate template values (`{{ getenv "NODE_ADDRESS" }}` …) preserved verbatim;
  the `prepare` init container still renders them at startup.
- `login-identity-providers.xml`, `state-management.xml` → whole-document files,
  written `setIfAbsent` in the handler override (framework XML adapter is
  Hadoop-property style, not document XML).
- `configOverrides` becomes live via the framework merge (dead in Gen 2, §6).

## 4. Cluster-scope resources → extensions

Registered on `common.NewExtensionRegistry[*NifiCluster]()`:

1. **SecurityExtension (PreReconcile)**: `EnsureGeneratedSecret` for the
   sensitive-props key (name = `sensitiveProperties.keySecret`, key
   `nifiSensitivePropsKey`, 16-char generator, create-once semantics identical
   to Gen 2, including the `autoGenerate=false` + absent → error path) and for
   `<cluster>-oidc-admin-password` when the resolved authenticator needs it.
2. **RbacExtension (PreReconcile)**: Role + RoleBinding `<cluster>-nifi`
   (leases, configmaps) via operator-go rbac builders.
3. **ReportingTaskExtension (PostReconcile)**: NiFi 1.x gate unchanged
   (`createReportingTaskJob.enable` && version `1.*`); Service + Job ported
   verbatim, with the selector bug fixed (§6.9).

## 5. What stays byte-identical (parity contract)

- Resource names: STS/CM/Service `<cluster>-node-<group>`, PDB `<cluster>-node`,
  SA/Role/RoleBinding `<cluster>-nifi`, secrets (user-named sensitive key,
  `<cluster>-oidc-admin-password`).
- ConfigMap data: `nifi.properties` (alphabetical `k=v`), `bootstrap.conf`
  (sorted `k=v`, `graceful.shutdown.seconds=<raw>`), `state-management.xml`,
  `login-identity-providers.xml` including gomplate placeholders.
- Containers: `prepare` init container (gomplate render script), `node` args
  (trap functions + vector shutdown-file dance), git-sync init+sidecar
  containers/volumes/args/resources, auth volumes/mounts/env, port set
  (http/8088, https/9443, protocol/9088, balance/6243, metrics/8081),
  startup+liveness TCP probes (thresholds 120/30, period 10, timeout 3) restored
  via `MainContainerCustomizer`.
- Replicas semantics incl. `stopped` → replicas 0 with resources preserved.
- git-sync container images/flags; `gitSyncConfig` args now emitted in sorted
  key order (Gen 2 map-iteration order was non-deterministic — not a parity
  break, a determinism fix).

## 6. Intentional-diff list (each item goes in the PR description)

Breaking (needs the documented manual migration for live clusters —
`kubectl delete sts --cascade=orphan`, §8):

0. **Workload ServiceAccount/Role/RoleBinding renamed** by operator-go #616:
   the framework derives `nificluster-<cluster>` (kind-prefixed, not
   configurable) and maintains the Role/RoleBinding itself via
   `WorkloadRBACRules`. The Gen 2 `<cluster>-nifi` objects are left behind on
   upgrade and should be deleted manually; pods move to the derived SA on the
   next rolling restart.

1. **STS selector labels**: Gen 2 `{instance, name=nificluster,
   managed-by=nifi.kubedoop.dev, component, role-group}` → Gen 3 framework set
   `{instance, component, managed-by=operator-go, role-group marker}`. The
   framework deliberately owns selector labels (immutability hazard); no knob
   reproduces the old set.
2. **STS `serviceName`**: `<name>` → `<name>-headless`, with a real headless
   Service added. Also fixes per-pod DNS (`NODE_ADDRESS` FQDN never resolved in
   Gen 2 — the governing Service had an allocated clusterIP).
3. **`podManagementPolicy`**: unset (OrderedReady) → framework `Parallel`.

Non-breaking conventional diffs:

4. Metadata labels move to the framework recommended set
   (`name=nifi`, `managed-by=operator-go`, `version=<resolved>` added).
5. ~~Readiness probe added~~ Reverted to Gen 2 parity (no readiness probe):
   the framework's default TCP readiness delays `readyReplicas`, which the
   e2e flow-verification timing budget is calibrated against — the chainsaw
   reporting-task suite failed at its 120s edge with it enabled. Adding a
   readiness probe is deferred to its own verified change.
6. Client Service: type now honors `clusterConfig.listenerClass`
   (unset → `external-unstable` preserved as default); Gen 2 ignored the field.
7. Container securityContext: **kept at the Gen 2 values** ({runAsUser: 0,
   runAsGroup: 0, allowPrivilegeEscalation: false}, no pod-level context) via
   `WithSecurityContext`. NiFi writes its repositories into the container
   filesystem under /kubedoop/data, so adopting the framework's canonical
   non-root context is a behavior change that needs its own verified change,
   not a migration side effect. (Supersedes the earlier draft of this item.)
8. Status: conditions/roleGroups ledger now written (Gen 2 wrote no status);
   orphaned role-group cleanup becomes active.
9. Bug fixes: reporting-task Service selector (`managed-by` value mismatch made
   it match zero pods) and its targetPort (always "https" even for HTTP
   clusters), OIDC admin secret registered for OIDC (was: LDAP), nil-panic when
   `authentication` unset, duplicate AuthenticationClass GET per reconcile,
   LDAP bind-credentials scope annotation rendered `service=service=<name>`
   (double prefix), git-sync extra args in nondeterministic map order.
10. Activated dead CRD fields: `configOverrides`, `envOverrides` semantics via
    framework merge; `jvmArgumentOverrides` → bootstrap.conf `java.arg.*`;
    `vectorAggregatorConfigMapName` + `logging` via framework Vector pipeline
    (inert when unset).
11. Framework apply/build conventions (all verified in the normalized parity
    diff, no functional effect): the role group ConfigMap volume is named
    `config` (was `nifi-config`), `enableServiceLinks: false` is set, pod
    volume order follows build order instead of name-sorted, and the
    `banzaicloud.com/last-applied` annotations disappear (the Gen 3 apply path
    does not use k8s-objectmatcher).
12. CRD schema: commons-type evolution removes the folded-config CRD defaults
    (`gracefulShutdownTimeout: 30s`, logging `level: INFO`, storage
    `capacity: 10Gi`, pdb `enabled: true`) — the upstream #544/#573 defect-class
    fix; defaults now apply at consumption time and rendered output is
    unchanged. nifi's own `+default {"gracefulShutdownTimeout":"30s"}` on
    `roleGroups[*].config` is removed for the same reason (it made role-level
    settings unreachable). `gracefulShutdownTimeout` gains a duration pattern
    validation. Status gains roleGroups/observedGeneration (additive).

Out of scope (unchanged dead fields): `clusterConfig.tls` server-cert volume
(broken in Gen 2 — keystore paths point at a volume that never existed; kept
as-is, tracked as a follow-up feature), `extraVolumes`.

## 7. operator-go feedback ledger (validated by the framework-steward review)

Accepted — filed upstream:

- **zncdatadev/operator-go#596 — Restore the product half of the Vector shutdown handshake**
  (`CommonBashTrapFunctions`, shutdown-file commands, `IndentTab4Spaces`):
  removed in operator-go #441 with no replacement while the Vector sidecar
  still enforces the contract; 7 operators carry private copies (airflow's
  `BashLibs` is byte-identical, dolphinscheduler reimplemented it, five Gen 2
  repos still import the deleted package). Vendored here in `internal/util/`
  with `TODO(operator-go)` markers.
- **zncdatadev/operator-go#597 — Declarative pod-RBAC channel** (`PodRBACRules` alongside
  `ServiceAccountNameFunc`): nifi (`internal/extensions/rbac.go`) and
  dolphinscheduler ship the same PreReconcile extension. Include the
  escalation footgun in docs: the operator's ClusterRole must hold every
  permission it grants (nifi hit this — leases re-added via marker).
- **zncdatadev/operator-go#598 — Exported apply helper for extensions** (`reconciler.ApplyResource` with
  `WithoutControllerReference`/`CreateOnly` options): three call sites across
  nifi and dolphinscheduler each reinvent the framework's private apply
  semantics, none matching its label-replace/annotation-merge behavior.

Rejected after review (kept product-side, with the framework's own seams):

- git-sync wiring stays in `internal/gitsync/` — the assumed airflow twin does
  not exist (airflow has the CRD field, no implementation).
- `PlainPropertiesMarshaler` stays — `RegisterFormat` is the sanctioned seam;
  a docs note upstream that the default adapter Java-escapes would suffice.
- The vendored secret-volume builder was deleted again — `security.
  CredentialsVolume` + `ScopeString` cover both call sites (rendered deltas:
  explicit `volumeMode: Filesystem` and scope set order; both inert).
- Init containers ride `buildCtx.SidecarManager` +
  `sidecar.NewStaticContainerProvider` (the channel existed; direct pod
  mutation raced the Vector provider's phase ordering).
- Whole-document ConfigMap keys via `resources.ConfigMap.Data[k] = v` are the
  documented extension point, not a gap.

Adopted mid-migration: operator-go #591 (`ProductConfig` gained ctx/client +
error) — the auth-derived nifi.properties keys now flow through the seam
instead of a hand-rolled merge; pin moved 5edf2ee → 0ec90d7.

Second adoption round (pin 0ec90d7 → e8a9495, post-#616/#632):

- **#597 was implemented upstream as #616** (`WorkloadRBACRules` + derived
  ServiceAccount): `internal/extensions/rbac.go` deleted; the SA rename is
  intentional-diff #0.
- **#596 answered by #617 with docs, deliberately no API**: native sidecars
  (KEP-753) supersede the vector shutdown handshake, so the shutdown-file
  commands should eventually be dropped rather than restored; `IndentTab4Spaces`
  and the trap functions stay product-vendored. Upstream #624's commit message
  also records three latent defects in the original trap-function bytes
  (unguarded `term_child_pid` read, `wait_for_termination` always returning 0,
  `errexit` leak) — kept verbatim here for parity, tracked as a follow-up.
- **#598 answered by #620 with docs + pinned apply-rule specs, no exported
  helper**: the reporting-task extension keeps its own create-only ensure.
- **#632 (declare/fold/derive)**: the handler now implements
  `RoleProvider.DeclareRoles` (ports, entrypoint Command, probes, env incl.
  valueFrom, listener class, the "30s" gracefulShutdownTimeout as a declared
  config default) and `RoleGroupResolver` (nifi.properties/bootstrap.conf +
  auth keys); `ImageResolution` moves image policy to the reconciler config.
  Rendered deltas: the main container's script moves from args into command
  (declaration carries Command; args now carry the user's cliOverrides, whose
  Gen 2 replace-the-command semantics are gone), and the framework renders
  imagePullSecrets from `spec.image.pullSecretName` (#622 — the field was dead
  since the commons ImageSpec migration; the bridge now forwards it).
- The generated readiness probe is stripped post-build again (RoleDeclaration
  offers no "no readiness" declaration; nil keeps the generated probe).

Blockers found: none — every gap had a supported workaround.

## 8. Upgrade impact & rollback

In-place operator upgrade over a live Gen 2 cluster: the STS update is rejected
(template labels no longer match the preserved immutable selector). Manual
migration: `kubectl delete sts <cluster>-node-<group> --cascade=orphan`, let the
operator recreate it (pods are adopted; no PVCs exist in Gen 2, so no data
risk). Rollback = redeploy the previous operator image and delete the
`-headless` Services; CRD spec schema is unchanged in both directions.

## 9. Verification

- Phase 0 baseline: green chainsaw run + `kubectl get sts,cm,svc,pdb,sa,secret
  -o yaml` snapshot of a representative CR (git-sync suite CR) on Gen 2.
- After migration: same snapshot, normalized diff (uids/resourceVersions/IPs
  stripped); every residual line maps to §6.
- Gates: `make lint && make test`, CRD sync (`make manifests helm-crd-sync`),
  full chainsaw on kind 1.35.0 with product 2.4.0, e2e directory unmodified.
