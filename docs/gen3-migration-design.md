# Gen 3 Migration Design — nifi-operator

Status: approved-for-implementation (this PR)
Baseline: `f406784` (Gen 2b, operator-go v0.12.6)
Target: operator-go `main` (Gen 3), currently pinned at `e8a9495`
References: `operator-go/examples/trino-operator` (canonical wiring),
`zookeeper-operator@refactor/base-operator-go` (GetSpec bridge pattern)

## 1. Goal and method

Replace the hand-rolled Gen 2 reconciler tree (`BaseCluster` →
`node.Reconciler` → per-resource reconcilers) with the Gen 3 framework,
keeping **rendered-resource parity** as the acceptance bar: the chainsaw
e2e suites (`git-sync`, `reporting-task`) run unmodified, and every
rendered-YAML difference against the Gen 2 baseline is either eliminated
or recorded in §6's intentional-diff list.

## 2. CRD strategy: bridge, don't reshape

The CRD spec schema does not change. `NifiCluster` implements
`common.ClusterInterface` by **bridging** the typed `spec.nodes` into the
generic role map at runtime (same pattern as trino/zookeeper):

```go
func (r *NifiCluster) GetSpec() *commonsv1alpha1.GenericClusterSpec
// nodes → Roles["node"]; ConfigSpec → RoleGroupConfigSpec (embedded);
// OverridesSpec fields flattened; replicas and pullSecretName copied.
```

- `NifiClusterStatus` embeds `commonsv1alpha1.GenericClusterStatus`
  (inline). The status schema change is additive (conditions survive, the
  roleGroups ledger is added).
- `spec.nodes.jvmArgumentOverrides` stays in the CRD; it is applied to
  `bootstrap.conf` `java.arg.N` entries by the product config layer (it is
  a dead field in Gen 2 — see §6 "activated fields").
- CRD diff gate: after `make generate && make manifests`, the spec portion
  of the CRD must be byte-identical; status/conditions additive only.

## 3. Component mapping

Where each Gen 2 piece of `internal/` landed (as of the `e8a9495` pin):

- `controller/nificluster_controller.go` + `controller/cluster/cluster.go`
  → `reconciler.NewGenericReconciler[*NifiCluster]` in `cmd/main.go`.
- `controller/node/{role,statefulset,configmap,service,port}.go` →
  `NifiRoleGroupHandler` embedding `BaseRoleGroupHandler[*NifiCluster]`,
  with the role's shape declared once per pass by
  `RoleProvider.DeclareRoles` (ports, bash entrypoint as `Command`,
  TLS-dependent probes, valueFrom env, listener class, and the "30s"
  graceful-shutdown fallback as a declared config default).
- Config assembly (`node/configmap.go`) → `RoleGroupResolver`
  (nifi.properties, bootstrap.conf, and the authenticator's extra keys
  through the merge pipeline) plus the handler's `BuildResources` override
  for the document-style XML files.
- `common/security/sensitive_key.go` and
  `common/security/oidc_admin_secret.go` →
  `reconciler.EnsureGeneratedSecret` from
  `SecurityExtension.PreReconcile` (also fixes the Gen 2 LDAP/OIDC
  registration swap, §6).
- `common/security/{static,ldap,oidc,authentication}.go` → kept as
  `internal/security`, invoked from the handler seams.
- `common/git_sync.go` → kept as `internal/gitsync`; init containers ride
  the sidecar channel, the continuous sidecars are appended in the
  `BuildResources` override.
- `controller/reporting_task/{job,service}.go` →
  `ReportingTaskExtension.PostReconcile` (NiFi 1.x only, unchanged gate).
- RBAC for NiFi pods (leases + configmaps for k8s-native leader election)
  → `GenericReconcilerConfig.WorkloadRBACRules`; the framework derives the
  ServiceAccount name and maintains the Role/RoleBinding (operator-go
  #616).
- `zookeeperConfigMapName` implicit dependency →
  `GenericReconcilerConfig.Dependencies` (fail fast with Degraded instead
  of crash-looping pods).

Image policy lives in `GenericReconcilerConfig.ImageResolution`
(`ProductName="nifi"` + defaults), read every reconcile. The handler keeps
only reconcile-invariant collaborators: the `ConfigGenerator` with `.conf`
and `.properties` mapped to a plain non-escaping marshaler (byte-format
parity: sorted `k=v` lines), and the Gen 2 security context via
`WithSecurityContext`.

Config channels:

- `nifi.properties`, `bootstrap.conf` → `RoleGroupResolver` (lowest merge
  layer); gomplate template values (`{{ getenv "NODE_ADDRESS" }}` …)
  preserved verbatim; the `prepare` init container still renders them at
  startup.
- `login-identity-providers.xml`, `state-management.xml` → whole-document
  files, written `setIfAbsent` in the handler override (the framework XML
  adapter is Hadoop-property style, not document XML).
- `configOverrides` becomes live via the framework merge (dead in Gen 2,
  §6).

## 4. Cluster-scope resources → extensions

Registered on `common.NewExtensionRegistry[*NifiCluster]()`:

1. **SecurityExtension (PreReconcile)**: `EnsureGeneratedSecret` for the
   sensitive-props key (name = `sensitiveProperties.keySecret`, key
   `nifiSensitivePropsKey`, 16-char generator, create-once semantics
   identical to Gen 2, including the `autoGenerate=false` + absent → error
   path) and for `<cluster>-oidc-admin-password` when the resolved
   authenticator needs it.
2. **ReportingTaskExtension (PostReconcile)**: NiFi 1.x gate unchanged
   (`createReportingTaskJob.enable` and version `1.*`); Service + Job
   ported verbatim with the selector bug fixed (§6.10), applied
   create-only (Job specs are immutable).

The Gen 2 pod-RBAC extension existed in the first migration round and was
deleted when operator-go #616 moved workload RBAC into the framework (§7).

## 5. What stays byte-identical (parity contract)

- Resource names: STS/CM/Service `<cluster>-node-<group>`, PDB
  `<cluster>-node`, secrets (user-named sensitive key,
  `<cluster>-oidc-admin-password`).
- ConfigMap data: `nifi.properties` (alphabetical `k=v`),
  `bootstrap.conf` (sorted `k=v`, `graceful.shutdown.seconds=<raw>`),
  `state-management.xml`, `login-identity-providers.xml` including
  gomplate placeholders.
- Containers: `prepare` init container (gomplate render script), the
  `node` entrypoint (trap functions + vector shutdown-file dance), git-sync
  init+sidecar containers/volumes/args/resources, auth volumes/mounts/env,
  the port set (http/8088, https/9443, protocol/9088, balance/6243,
  metrics/8081), startup+liveness TCP probes (thresholds 120/30, period
  10, timeout 3).
- Replicas semantics incl. `stopped` → replicas 0 with resources
  preserved.
- git-sync container images/flags; `gitSyncConfig` args now emitted in
  sorted key order (Gen 2 map-iteration order was non-deterministic — not
  a parity break, a determinism fix).

## 6. Intentional-diff list (each item goes in the PR description)

Breaking (needs the documented manual migration for live clusters —
`kubectl delete sts --cascade=orphan`, §8):

1. **Workload ServiceAccount/Role/RoleBinding renamed** by operator-go
   #616: the framework derives `nificluster-<cluster>` (kind-prefixed, not
   configurable) and maintains the Role/RoleBinding itself via
   `WorkloadRBACRules`. The Gen 2 `<cluster>-nifi` objects are left behind
   on upgrade and should be deleted manually; pods move to the derived SA
   on the next rolling restart.
2. **STS selector labels**: Gen 2 `{instance, name=nificluster,
   managed-by=nifi.kubedoop.dev, component, role-group}` → Gen 3 framework
   set `{instance, component, managed-by=operator-go, role-group marker}`.
   The framework deliberately owns selector labels (immutability hazard);
   no knob reproduces the old set.
3. **STS `serviceName`**: `<name>` → `<name>-headless`, with a real
   headless Service added. Also fixes per-pod DNS (the `NODE_ADDRESS` FQDN
   never resolved in Gen 2 — the governing Service had an allocated
   clusterIP).
4. **`podManagementPolicy`**: unset (OrderedReady) → framework `Parallel`.

Non-breaking conventional diffs:

1. Metadata labels move to the framework recommended set (`name=nifi`,
   `managed-by=operator-go`, `version=<resolved>` added).
2. No readiness probe (Gen 2 parity, kept deliberately): the framework's
   default TCP readiness delays `readyReplicas`, which the e2e
   flow-verification timing budget is calibrated against — the chainsaw
   reporting-task suite failed at its 120s edge with it enabled. The
   generated probe is stripped post-build (`RoleDeclaration` has no way to
   declare "none"); introducing one is deferred to its own verified
   change.
3. Client Service: type now honors `clusterConfig.listenerClass` (unset →
   `external-unstable` preserved as default); Gen 2 ignored the field.
4. Container securityContext: **kept at the Gen 2 values** (runAsUser 0,
   runAsGroup 0, allowPrivilegeEscalation false, no pod-level context) via
   `WithSecurityContext`. NiFi writes its repositories into the container
   filesystem under /kubedoop/data, so adopting the framework's canonical
   non-root context is a behavior change that needs its own verified
   change, not a migration side effect.
5. Status: conditions/roleGroups ledger now written (Gen 2 wrote no
   status); orphaned role-group cleanup becomes active.
6. The main container's entrypoint script moves from `args` into
   `command` (operator-go #632: the declaration carries `Command`; args
   now carry the user's `cliOverrides`, whose Gen 2 replace-the-command
   semantics are gone).
7. `spec.image.pullSecretName` is rendered again (operator-go #622; the
   field had been dead since the commons ImageSpec migration — the bridge
   now forwards it and the framework renders pod imagePullSecrets).
8. Bug fixes: reporting-task Service selector (`managed-by` value mismatch
   made it match zero pods) and its targetPort (always "https" even for
   HTTP clusters), OIDC admin secret registered for OIDC (was: LDAP),
   nil-panic when `authentication` unset, duplicate AuthenticationClass
   GET per reconcile, LDAP bind-credentials scope annotation rendered
   `service=service=<name>` (double prefix), git-sync extra args in
   nondeterministic map order.
9. Activated dead CRD fields: `configOverrides`, `envOverrides` semantics
   via framework merge; `jvmArgumentOverrides` → bootstrap.conf
   `java.arg.*`; `vectorAggregatorConfigMapName` + `logging` via the
   framework Vector pipeline (inert when unset).
10. Framework apply/build conventions (verified in the normalized parity
    diff, no functional effect): the role group ConfigMap volume is named
    `config` (was `nifi-config`), `enableServiceLinks: false` is set, pod
    volume order follows build order instead of name-sorted, and the
    `banzaicloud.com/last-applied` annotations disappear (the Gen 3 apply
    path does not use k8s-objectmatcher).
11. CRD schema: commons-type evolution removes the folded-config CRD
    defaults (`gracefulShutdownTimeout: 30s`, logging `level: INFO`,
    storage `capacity: 10Gi`, pdb `enabled: true`) — the upstream
    #544/#573 defect-class fix; defaults now apply at consumption time and
    rendered output is unchanged. nifi's own graceful-shutdown default on
    `roleGroups[*].config` is removed for the same reason (it made
    role-level settings unreachable). `gracefulShutdownTimeout` gains a
    duration pattern validation. Status gains roleGroups and
    observedGeneration (additive).

Out of scope (unchanged dead fields): the `clusterConfig.tls` server-cert
volume (broken in Gen 2 — keystore paths point at a volume that never
existed; kept as-is, tracked as a follow-up feature), `extraVolumes`.

## 7. operator-go feedback ledger

Validated by the framework-steward review, filed upstream, and — one
adoption round later — answered upstream:

- **zncdatadev/operator-go#596 — restore the Vector shutdown-handshake
  helpers** (`CommonBashTrapFunctions`, shutdown-file commands,
  `IndentTab4Spaces`). Answered by #617 with docs, deliberately no API:
  native sidecars (KEP-753) supersede the shutdown handshake, so the
  commands should eventually be dropped rather than restored. The helpers
  stay vendored in `internal/util/`. Upstream #624's commit message also
  records three latent defects in the original trap-function bytes
  (unguarded `term_child_pid` read, `wait_for_termination` always
  returning 0, `errexit` leak) — kept verbatim here for parity, tracked as
  a follow-up.
- **zncdatadev/operator-go#597 — declarative pod-RBAC channel**.
  Implemented upstream as #616 (`WorkloadRBACRules` + derived workload
  ServiceAccount); nifi's `internal/extensions/rbac.go` is deleted and the
  SA rename is intentional-diff §6.1. The escalation footgun the issue
  named (the operator's ClusterRole must hold every permission it grants)
  is now documented upstream.
- **zncdatadev/operator-go#598 — exported apply helper for extensions**.
  Answered by #620 with docs plus pinned apply-rule specs, no exported
  helper; the reporting-task extension keeps its own create-only ensure.

Rejected after review (kept product-side, with the framework's own seams):

- git-sync wiring stays in `internal/gitsync/` — the assumed airflow twin
  does not exist (airflow has the CRD field, no implementation).
- `PlainPropertiesMarshaler` stays — `RegisterFormat` is the sanctioned
  seam; the default adapter Java-escapes values that embed gomplate
  templates.
- The vendored secret-volume builder was deleted —
  `security.CredentialsVolume` + `ScopeString` cover both call sites
  (rendered deltas: explicit `volumeMode: Filesystem` and scope set order;
  both inert).
- Init containers ride `buildCtx.SidecarManager` +
  `sidecar.NewStaticContainerProvider` (the channel existed; direct pod
  mutation raced the Vector provider's phase ordering).
- Whole-document ConfigMap keys via `resources.ConfigMap.Data[k] = v` are
  the documented extension point, not a gap.

Adoption history:

- operator-go #591 (`ProductConfig` gained ctx/client + error): adopted
  mid-migration; pin moved 5edf2ee → 0ec90d7.
- operator-go #616 + #632 (pin 0ec90d7 → e8a9495): the handler moved onto
  `RoleProvider.DeclareRoles` + `RoleGroupResolver` + `ImageResolution`,
  and workload RBAC moved into the framework. Rendered deltas are §6.1,
  §6.6 (non-breaking) and §6.7.

Blockers found: none — every gap had a supported workaround.

## 8. Upgrade impact & rollback

In-place operator upgrade over a live Gen 2 cluster: the STS update is
rejected (template labels no longer match the preserved immutable
selector). Manual migration: `kubectl delete sts <cluster>-node-<group>
--cascade=orphan`, then let the operator recreate it (pods are adopted; no
PVCs exist in Gen 2, so no data risk). The Gen 2 `<cluster>-nifi`
ServiceAccount/Role/RoleBinding are left behind and should be deleted.
Rollback = redeploy the previous operator image and delete the `-headless`
Services; the CRD spec schema is unchanged in both directions.

## 9. Verification

- Phase 0 baseline: green chainsaw run + a rendered-resource snapshot
  (`kubectl get sts,cm,svc,pdb,sa,secret -o yaml`) of a representative CR
  (the git-sync suite CR) on Gen 2.
- After migration: same snapshot, normalized diff (uids/resourceVersions/
  IPs stripped); every residual line maps to §6.
- Config byte parity is locked by unit tests against fixtures captured
  from the Gen 2 baseline (`internal/product/config_test.go`).
- Gates: `make lint && make test`, CRD sync (`make manifests
  helm-crd-sync`), full chainsaw on kind 1.35.0 with product 2.4.0, e2e
  directory unmodified.
