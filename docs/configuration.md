---
type: Configuration
title: incident-controller — configuration
description: The controller's flags and environment variables, the incident-controller chart values, and the check pod's identity, RBAC and network policy.
resource: github.com/krateo-platformops/incident-controller
tags: [configuration, helm, rbac, networkpolicy]
timestamp: 2026-09-25T00:00:00Z
---

# Configuration

## Controller flags

Every flag has an environment variable, `INCIDENT_CONTROLLER_<NAME>`, which
sets its default. The chart sets them from its values.

| Flag | Environment | Default | Meaning |
|---|---|---|---|
| `-poll` | `POLL_INTERVAL` | `1m` | time from the end of one check of an incident to the start of the next |
| `-settle-window` | `SETTLE_WINDOW` | `5m` | how long, from entering `Verifying`, a verify exit `1` is retried before the incident goes back to `Open` |
| `-check-timeout` | `CHECK_TIMEOUT` | `1m` | deadline of one check run, the pod's `activeDeadlineSeconds` |
| `-checks-namespace` | `CHECKS_NAMESPACE` | none, required | namespace the check pods run in |
| `-check-pod-template` | `CHECK_POD_TEMPLATE` | `/etc/incident-controller/check-pod.yaml` | the check pod spec template |
| `-debug` | `DEBUG` | `false` | debug logging |
| `-sync` | `SYNC_PERIOD` | `1h` | informer resync period |
| `-max-reconcile-rate` | `MAX_RECONCILE_RATE` | `5` | concurrent reconciles |
| `-leader-election` | `LEADER_ELECTION` | `false` | leader election, Lease `incident-controller.observability.krateo.io` |
| `-timeout` | `TIMEOUT` | `1m` | timeout of one reconcile |
| `-min-error-retry-interval`, `-max-error-retry-interval` | `MIN_ERROR_RETRY_INTERVAL`, `MAX_ERROR_RETRY_INTERVAL` | `1s`, `30s` | backoff after an error |

A pod that has not finished `check-timeout` + 1 minute after its creation, for
example one never scheduled, counts as a timeout.

The controller serves metrics on `:8080` and `/healthz`, `/readyz` on `:8081`.
Logs are JSON in the OTel log model, `service.name: incident-controller`.

## The check pod template

The chart renders a pod spec into the ConfigMap `incident-controller-check-pod`,
which the controller reads at start. It must have one container and a
`serviceAccountName`. The template supplies the service account, image,
resources, pod security context, image pull secrets, node selector and
tolerations. The controller sets the rest, whatever the template says:

- the script, from a ConfigMap owned by the pod, run as `bash /check/check.sh`;
- `restartPolicy: Never`, `activeDeadlineSeconds` from `-check-timeout`;
- `runAsNonRoot`, the `RuntimeDefault` seccomp profile, no privilege
  escalation, all capabilities dropped, a read-only root filesystem, no host
  namespaces, no service links;
- a 16Mi `emptyDir` at `/tmp`, which is `HOME`;
- no DNS: `dnsPolicy: None`, with `127.0.0.1` as the only nameserver.

The pod passes the `restricted` Pod Security Standard, which the checks
namespace enforces.

## Chart values (`helm/incident-controller`)

| Value | Default | Meaning |
|---|---|---|
| `image.repository`, `image.tag`, `image.pullPolicy` | `ghcr.io/krateo-platformops/incident-controller`, appVersion, `IfNotPresent` | the controller image |
| `imagePullSecrets` | `[]` | for the controller pod |
| `serviceAccount.create`, `serviceAccount.name` | `true`, `incident-controller` | the controller's service account |
| `controller.pollInterval`, `controller.settleWindow` | `1m`, `5m` | see the flags |
| `controller.debug`, `syncPeriod`, `maxReconcileRate`, `leaderElection` | `false`, `1h`, `5`, `true` | see the flags |
| `resources`, `podSecurityContext`, `securityContext` | small, non-root, read-only | the controller pod |
| `checks.namespace` | `krateo-incident-checks` | where check pods run |
| `checks.createNamespace` | `true` | create it, with `pod-security.kubernetes.io/enforce: restricted` |
| `checks.timeoutSeconds` | `60` | the check deadline |
| `checks.serviceAccount` | `incident-check` | the check pods' identity |
| `checks.clusterRoles` | `[view]` | cluster roles bound to it |
| `checks.readApiGroups` | the 13 `*.krateo.io` groups kind-krateo serves | groups it reads through `incident-controller-check-reader` |
| `checks.image.*` | `ghcr.io/krateo-platformops/incident-controller-check`, appVersion | bash, kubectl and jq |
| `checks.imagePullSecrets`, `resources`, `podSecurityContext`, `nodeSelector`, `tolerations` | | the check pod template |
| `checks.networkPolicy.enabled` | `true` | the check NetworkPolicy |
| `checks.networkPolicy.apiServer` | `[]` | `[{cidr, ports}]`; empty looks the apiserver up at install |

## What the check pods can do

- **Read:** `view` (the built-in APIs, no Secrets) plus `get`, `list`, `watch`
  on every resource of the groups in `checks.readApiGroups`. The schema accepts
  only dotted group names, so neither `*` nor the core group can be listed,
  and both would include Secrets. Nothing grants writes.
- **Reach:** the apiserver, by IP, and nothing else; no ingress. kubectl
  finds it from `KUBERNETES_SERVICE_HOST`, so the pods need no DNS, and they
  get none: the controller sets `dnsPolicy: None` with `127.0.0.1` as the only
  nameserver, since a lookup that reaches cluster DNS is forwarded outside and
  would carry data out. With `checks.networkPolicy.apiServer` empty, the chart
  looks up the `kubernetes` Service and its EndpointSlice at install and allows
  their addresses and ports. A render without cluster access (`helm template`)
  finds neither: the policy then allows no egress, and the install notes say
  so.
  Cilium does not match node addresses with an ipBlock by default; where the
  apiserver runs on the nodes, allow it with a CiliumNetworkPolicy
  (`toEntities: [kube-apiserver]`).

## What the controller can do

| Scope | Resources | Verbs |
|---|---|---|
| cluster | `incidents.observability.krateo.io` | get, list, watch, update, patch |
| cluster | `incidents/status` | get, update, patch |
| cluster | `events` (core and `events.k8s.io`) | create, patch |
| checks namespace | `pods`, `configmaps` | get, list, watch, create, delete |
| its namespace | `leases` | get, create, update (leader election) |

The controller can create pods only in the checks namespace, where the only
service account with any rights is the read-only check one. It reads no
Secrets and runs no apply script.

## Roles for people

The portal's Close, "I applied it" and Discard act on the Incident with the
clicking user's own token. The chart ships two ClusterRoles for that and binds
neither; bind them to your groups, cluster-wide or per namespace with a
RoleBinding.

| ClusterRole | Verbs on `incidents.observability.krateo.io` | Allows |
|---|---|---|
| `krateo-incident-viewer` | get, list, watch | reading incidents |
| `krateo-incident-responder` | get, list, watch, patch, delete | Close (`spec.closed`), "I applied it" (`spec.applied`), Discard (delete) |

Neither grants `incidents/status`: only the writer and the controller change
an incident's status. Neither carries aggregation labels.

```sh
kubectl create clusterrolebinding sre-incident-responder --clusterrole=krateo-incident-responder --group=sre
```
