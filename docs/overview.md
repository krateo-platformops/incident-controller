---
type: Architecture
title: incident-controller — overview
description: What an Incident is, its lifecycle, who writes which part of it, and how the controller maps onto provider-runtime.
resource: github.com/krateo-platformops/incident-controller
tags: [crd, incident, observability, provider-runtime, architecture]
timestamp: 2026-09-25T00:00:00Z
---

# Overview

An **Incident** is one occurrence of an Alert's problem, from its root-cause
analysis to its end. An Alert (`alerts.observability.krateo.io`, owned by
alert-troubleshooter) opens Incidents; it never closes them. Each Incident
carries its own checks, three bash scripts, and ends when a script proves the
problem gone or a human closes it.

## Who writes what

| Actor | Writes |
|---|---|
| alert-troubleshooter (the writer) | creates the Incident with its label and `spec`; writes the analysis, `howToFix`, `firings` and `lastFiredAt`; sets `Analyzing`, then `Open` |
| incident-controller | runs precondition and verify; writes `checks`, every other state transition, `resolution` and the conditions |
| a human, through the portal | `spec.applied` ("I applied it", or Apply) and `spec.closed` (Close), with their own token and the `incident-responder` role |

Field by field: [api](./api.md).

## One open incident per alert

A firing on an alert with an open incident (any state but `Resolved` and
`Closed`) adds one to that incident's `status.firings` and sets
`status.lastFiredAt`; it runs no analysis. A firing opens a new incident only
when the alert has no open one. So a resolved incident whose alert still fires
is followed by a new incident with a fresh analysis at the next firing.

## Lifecycle

```
Analyzing ──▶ Open ◀──▶ Verifying ──▶ Resolved
any state ──spec.closed──▶ Closed
```

| From | To | On |
|---|---|---|
| — | `Analyzing` | the writer's first status write |
| `Analyzing` | `Open` | the analysis finished, or failed with `error` set |
| `Open` | `Verifying` | precondition exit `0`, or `spec.applied`, which the controller consumes: it appends an `apply` check and sets `applied` back to `false` |
| `Verifying` | `Resolved` | verify exit `0`, at any time |
| `Verifying` | `Open` | verify exit `1` once the settle window has passed |
| any | `Closed` | `spec.closed: true` |

The settle window (5 minutes by default) gives a fix time to take effect. It
starts when the incident enters `Verifying`: at the precondition exit `0` or
the `apply` check that moved it. Inside the window a verify exit `1` is
recorded and verify runs again at the next poll; the first exit `1` at or
after the window's end moves the incident back to `Open`. An exit other than
`0` or `1`, or a timeout, never moves it, inside the window or after. A human
applying again while `Verifying` appends another `apply` check, which restarts
the window.

`Resolved` means a script proved the problem gone; `Closed` means a person
decided. `Closed` is final, and a `Resolved` incident can only become `Closed`.

The first precondition run with a verdict must exit `1`; runs that end
without `0` or `1` do not count. An exit `0` there means the script
cannot see the problem, so the incident is flagged (`Reproduced=False`)
instead of moving on: it stays `Open`, its precondition no longer runs, and
only `spec.applied` (then verify) or `spec.closed` moves it.

A failed analysis leaves the incident `Open` with `error` set; a human can
close it. An incident with no `howToFix` scripts gets no checks run.

## The scripts

`status.howToFix` holds three bash scripts written by the analysis:

| Script | Run by | When |
|---|---|---|
| `precondition` | the controller, in a read-only sandbox | every poll interval while `Open` |
| `apply` | a human: their own terminal, or the portal's Apply | when they decide |
| `verify` | the controller, in a read-only sandbox | while `Verifying` |

Exit `0` means the incident is gone, `1` that it holds; any other exit, or a
timeout, is unknown and changes no state. The precondition tests the
root-cause object, never the alert's rows, so it still reports the right
answer when the alert fires for another reason.

## On provider-runtime

The Incident is a provider-runtime managed resource: it embeds provider-runtime's
`ConditionedStatus` and satisfies `resource.Managed`. Its external resource is
the check pod of the current step.

| provider-runtime | incident-controller |
|---|---|
| `ExternalClient.Observe` | reads the incident's check pods; records a finished one in `status.checks` and applies its transition; applies `spec.closed` and `spec.applied`. It changes nothing but the Incident held in memory |
| `Create` | no check pod for the current step and one is due: starts one, precondition when `Open`, verify when `Verifying` |
| `Update` | persists the status, then sets `spec.applied` back to `false`, deletes finished and stale check pods, and starts the next check when it is due |
| `Delete` + finalizer | the Incident is deleted: removes its check pods and ConfigMaps |
| `WithPollInterval(1m)` | the 60 s check cadence; a poll-interval hook requeues when the next check falls due |
| `krateo.io/paused` | pauses one incident: no checks, no transitions, `spec.closed` included |

A check is due a poll interval after the last one ended. A check pod's watch
events requeue its incident, so a finished check is read at once. Update writes
the status before anything else, so a result is never lost with its pod and a
consumed `spec.applied` is never lost with its flag; result times are
deterministic, so a result whose pod outlives a failed delete is not recorded
twice. A running check of a script that is no longer current (the incident was
applied or closed meanwhile) is deleted and its result dropped.

## Check pods

Each run is a pod in a dedicated namespace (`krateo-incident-checks` by
default), holding only the check pods and their read-only service account:

- the script from a ConfigMap owned by the pod, run with bash; the image has
  bash, kubectl and jq;
- the service account `incident-check`, bound to `view` and to
  `incident-controller-check-reader`, which reads the listed Krateo API groups;
  no Secrets, no writes;
- a NetworkPolicy allowing egress to cluster DNS and the apiserver only, and no
  ingress;
- `activeDeadlineSeconds: 60`, and the `restricted` Pod Security Standard.

The controller never runs an apply script. Details: [configuration](./configuration.md).

## Layout

| Path | What |
|---|---|
| `apis/incident/v1alpha1` | the Go types, the source of truth |
| `helm/incident-controller-crds` | the CRD chart; its template is generated from the Go types |
| `helm/incident-controller` | the controller chart: Deployment, RBAC, the checks namespace, the check pod template, the NetworkPolicy, the unbound `incident-viewer` and `incident-responder` roles |
| `main.go`, `internal/controllers/incident` | the controller: `machine.go` is the state machine, `checkpod.go` the check pods, `incident.go` the provider-runtime client |
| `check/Dockerfile` | the check pod image |
| `examples/incident` | a sample Incident, validated by the unit tests |
