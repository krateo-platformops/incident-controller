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
| `-apply-timeout` | `APPLY_TIMEOUT` | `2m` | deadline of one IncidentApply run, the apply pod's `activeDeadlineSeconds` |
| `-apply-ttl` | `APPLY_TTL` | `720h` | how long a finished IncidentApply is kept before the controller deletes it |
| `-credentials-namespace` | `CREDENTIALS_NAMESPACE` | none, required | namespace of the `<username>-clientconfig` Secrets authn writes at login |
| `-check-pod-template` | `CHECK_POD_TEMPLATE` | `/etc/incident-controller/check-pod.yaml` | the check pod spec template |
| `-debug` | `DEBUG` | `false` | debug logging |
| `-sync` | `SYNC_PERIOD` | `1h` | informer resync period |
| `-max-reconcile-rate` | `MAX_RECONCILE_RATE` | `5` | concurrent reconciles |
| `-leader-election` | `LEADER_ELECTION` | `false` | leader election, Lease `incident-controller.observability.krateo.io` |
| `-timeout` | `TIMEOUT` | `1m` | timeout of one reconcile |
| `-global-reconcile-rate` | `GLOBAL_RECONCILE_RATE` | `20` | reconciles per second across all incidents (a token bucket, bursts of 10×); a failed reconcile backs off per incident, 1 s to 60 s |

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

An apply pod is the same pod with `automountServiceAccountToken: false`, the
requester's kubeconfig mounted read-only at `/kube` from a Secret owned by the
pod, and `KUBECONFIG=/kube/config`; its deadline is `-apply-timeout`.

## Chart values (`helm/incident-controller`)

| Value | Default | Meaning |
|---|---|---|
| `image.repository`, `image.tag`, `image.pullPolicy` | `ghcr.io/krateo-platformops/incident-controller`, appVersion, `IfNotPresent` | the controller image |
| `imagePullSecrets` | `[]` | for the controller pod |
| `serviceAccount.create`, `serviceAccount.name` | `true`, `incident-controller` | the controller's service account |
| `controller.pollInterval`, `controller.settleWindow` | `1m`, `5m` | see the flags |
| `controller.debug`, `syncPeriod`, `maxReconcileRate`, `leaderElection` | `false`, `1h`, `5`, `true` | see the flags |
| `resources`, `podSecurityContext`, `securityContext` | small, non-root, read-only | the controller pod |
| `apply.timeoutSeconds` | `120` | the apply deadline |
| `apply.ttl` | `720h` | how long a finished IncidentApply is kept |
| `apply.credentialsNamespace` | `""` (the release namespace) | where authn writes `<username>-clientconfig` |
| `checks.namespace` | `krateo-incident-checks` | where check and apply pods run |
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
| cluster | `incidentapplies.observability.krateo.io` | get, list, watch, update, patch, delete |
| cluster | `incidentapplies/status` | get, update, patch |
| cluster | `subjectaccessreviews` | create |
| cluster | `mutatingadmissionpolicies`, `mutatingadmissionpolicybindings` named `incidentapply-requested-by` | get |
| checks namespace | `pods`, `configmaps` | get, list, watch, create, delete |
| checks namespace | `pods/log` | get |
| checks namespace | `secrets` | create |
| credentials namespace | `secrets` | get |
| its namespace | `leases` | get, create, update (leader election) |

The controller can create pods only in the checks namespace, where the only
service account with any rights is the read-only check one. It reads Secrets
only in the credentials namespace: every user's login credentials, as snowplow
does, which lets it act as any user who has logged in. It uses them only to run
an apply script someone asked for as that same user, and never as itself.

## Roles for people

The portal's Close, "I applied it", Discard and Run apply act with the
clicking user's own token. The chart ships two ClusterRoles for that and binds
neither; bind them to your groups, cluster-wide or per namespace with a
RoleBinding.

| ClusterRole | Verbs | Allows |
|---|---|---|
| `krateo-incident-viewer` | get, list, watch on incidents and incidentapplies | reading incidents and their runs |
| `krateo-incident-responder` | get, list, watch, patch, delete on incidents; get, list, watch, create on incidentapplies | Close (`spec.closed`), "I applied it" (`spec.applied`), Discard (delete), Run apply (create an IncidentApply) |

Run apply also needs `patch` on the incident, which the controller checks, and
the script can do only what the user's own roles allow.

Neither grants `incidents/status`: only the writer and the controller change
an incident's status. Neither carries aggregation labels.

```sh
kubectl create clusterrolebinding sre-incident-responder --clusterrole=krateo-incident-responder --group=sre
```
