---
type: API
title: incident-controller — api
description: The Incident custom resource (incidents.observability.krateo.io/v1alpha1) field by field, who writes each field, and the rules the apiserver enforces.
resource: github.com/krateo-platformops/incident-controller
tags: [crd, incident, observability, api]
timestamp: 2026-09-25T00:00:00Z
---

# API

The contract is the `Incident` custom resource, group `observability.krateo.io`,
version `v1alpha1`, plural `incidents`, short name `inc`, categories `krateo`
and `observability`, namespaced, with a status subresource.

The Go types in [`apis/incident/v1alpha1`](../apis/incident/v1alpha1/types.go)
are the source of truth. `make generate` writes the CRD from them into
[`helm/incident-controller-crds/templates/`](../helm/incident-controller-crds/templates/observability.krateo.io_incidents.yaml).
A sample with every field is [`examples/incident`](../examples/incident/README.md).

## Identity

| Item | Value |
|---|---|
| Name | `‹alert name›-‹yyyymmdd-hhmmss›` of the opening firing, one object per incident |
| Namespace | the Alert's namespace, `spec.alertRef.namespace` |
| Label | `observability.krateo.io/alert: ‹spec.alertRef.name›`, set by the writer at creation |

List one alert's incidents with the label, in the alert's namespace:

```sh
kubectl get incidents -n krateo-system -l observability.krateo.io/alert=compositiondefinitions-not-ready
```

`kubectl get incidents` prints `Alert` (`.spec.alertRef.name`), `State`,
`Firings` and `Age`.

## spec

| Field | Type | Written by | Meaning |
|---|---|---|---|
| `alertRef.name` | string, required, 1-63 chars | writer, at creation | the Alert that opened the incident; also the label value, hence 63 |
| `alertRef.namespace` | string, required | writer, at creation | the Alert's namespace |
| `trigger` | `alert` \| `composition-condition` \| `user-ask` | writer | what started the investigation |
| `prompt` | string | writer | the root-cause-analysis prompt sent to incident-agent |
| `triggeredAt` | date-time | writer | when the opening firing arrived |
| `applied` | bool | human: "I applied it", or the portal's Apply; reset by the controller | the apply script ran. The controller consumes it: it moves Open to Verifying, appends an `apply` check and sets `applied` back to `false` |
| `closed` | bool | human: the portal's Close | moves any state to Closed |

## status

Lifecycle and checks:

| Field | Type | Written by | Meaning |
|---|---|---|---|
| `state` | `Analyzing` \| `Open` \| `Verifying` \| `Resolved` \| `Closed` | writer: `Analyzing`, then `Open`; controller: every other transition | see [overview](./overview.md#lifecycle) |
| `firings` | int ≥ 0 | writer | alert firings this incident covers, the opening one included |
| `lastFiredAt` | date-time | writer | the last of those firings |
| `howToFix.precondition` | string (bash) | writer | exit `1` while the incident holds, `0` once it is gone; tests the root-cause object, never the alert's rows |
| `howToFix.apply` | string (bash) | writer | the fix; a human runs it |
| `howToFix.verify` | string (bash) | writer | exit `0` once the fix worked, `1` if it did not |
| `checks[]` | at most 20 | controller | script runs, oldest first; the controller keeps the newest 20 |
| `checks[].script` | `precondition` \| `apply` \| `verify`, required | controller | the script that ran |
| `checks[].exit` | int 0-255 | controller | its exit code; absent for a timeout, for a pod that never ran, and for the `apply` check that records a consumed `spec.applied` |
| `checks[].at` | date-time, required | controller | when the run finished |
| `resolution.by` | `verify` \| `user`, required | controller | `verify` with `Resolved`, `user` with `Closed` |
| `resolution.at` | date-time, required | controller | when the incident ended |
| `conditions[]` | provider-runtime `Condition` | controller | `Ready` and `Synced` from provider-runtime, plus `Reproduced` below |

Any other exit code, and a timeout, is unknown: it is recorded and changes no
state.

The root-cause analysis, written by the writer:

| Field | Type | Meaning |
|---|---|---|
| `report` | string (markdown) | the analysis |
| `rootCause.statement` | string | the root cause |
| `rootCause.confidence` | string | a decimal in [0, 1], e.g. `"0.85"` |
| `rootCause.category` | string | `config`, `capacity`, `image`, `network`, `dependency`, `other` |
| `sources[]` | `{type, ref, excerpt}` | the evidence; `type` is `logs` \| `events` \| `metrics` \| `object` |
| `reasoningTrace[]` | `{step, statement, evidenceRefs[]}` | the reasoning; `evidenceRefs` index `sources` from 0 |
| `analyzedResources[]` | `{gvr, name, namespace, whatWasRead}` | the cluster objects the analysis read |
| `missingContext[]` | string | what the analysis could not see |
| `assumptions[]` | string | what it assumed for lack of it |
| `error` | string | why the analysis failed |
| `completedAt` | date-time | when the analysis finished |

## The Reproduced condition

The controller sets it on the first precondition run:

| Status | Reason | Meaning |
|---|---|---|
| `True` | `PreconditionHolds` | the first run exited `1`: the script sees the incident |
| `False` | `PreconditionPassed` | the first run exited `0`: the script cannot see the incident. The incident is flagged: it stays `Open`, its precondition no longer runs, and only `spec.applied` or `spec.closed` moves it |

It is absent until the first run.

## Rules the apiserver enforces

| Rule | Message |
|---|---|
| `spec.alertRef` does not change | `alertRef is immutable` |
| `spec.closed` stays `true` once set | `closed cannot be unset` |
| `status.resolution` is set exactly when `state` is `Resolved` or `Closed` | `resolution is set exactly when state is Resolved or Closed` |
| `Resolved` goes with `by: verify`, `Closed` with `by: user` | `Resolved goes with resolution.by verify, Closed with resolution.by user` |
| a `Resolved` incident only becomes `Closed`; a `Closed` one stays `Closed` | `Resolved can only become Closed, and Closed is final` |
| `status.checks` holds at most 20 entries | `Too many` |

A writer's status patch that breaks a rule is rejected whole. In particular, a
writer finishing an analysis on an incident a human already closed cannot move
it to `Open`.

## Go

- `v1alpha1.Incident` satisfies provider-runtime's `resource.Managed`,
  `v1alpha1.IncidentList` its `resource.ManagedList`.
- Constants: `LabelAlert`, `MaxChecks`, the `State*`, `Script*`, `ResolvedBy*`,
  `Trigger*` and `Source*` values, `TypeReproduced` and its reasons.
- `apis.AddToScheme` registers the group.
