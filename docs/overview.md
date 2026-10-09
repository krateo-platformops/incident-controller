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
alert-provider) opens Incidents; it never closes them. Each Incident carries
its own checks as bash scripts, and ends when a script proves the problem gone
or a human closes it.

## Who writes what

| Actor | Writes |
|---|---|
| alert-provider (the writer) | creates the Incident with its label and `spec`; writes the analysis and `howToFix`; sets `Analyzing`, then `Open` |
| incident-controller | runs precondition and verify; writes `lastChecks`, `verifyingSince`, every other state transition, `resolution` and the conditions |
| a human, through the portal | `spec.applied` ("I applied it", or Apply) and `spec.closed` (Close), with their own token and the `krateo-incident-responder` role; an IncidentApply (Run apply), which has the controller run the apply script with their own rights |

Field by field: [api](./api.md).

## Many incidents per alert

The writer evaluates each firing alert about every 60 s. A firing that an
open incident (any state but `Resolved` and `Closed`) describes opens nothing
and writes nothing. An LLM compares the firing with each open incident to
decide. When none is the same problem, the firing opens a new incident with its
own analysis, so an alert can have several open incidents at once. An incident
still `Analyzing`, or whose analysis failed, takes the firing without a
comparison. [The writer's docs](https://github.com/krateo-platformops/alert-troubleshooter/blob/main/docs/overview.md)
have the details.

## Lifecycle

```
Analyzing ──▶ Open ◀──▶ Verifying ──▶ Resolved
any state ──spec.closed──▶ Closed
```

| From | To | On |
|---|---|---|
| — | `Analyzing` | the writer's first status write |
| `Analyzing` | `Open` | the analysis finished, or failed with `error` set |
| `Open` | `Verifying` | precondition exit `0`; `spec.applied`, which the controller consumes: it records an `apply` result and sets `applied` back to `false`; or an IncidentApply run that exits `0` |
| `Verifying` | `Resolved` | verify exit `0`, at any time |
| `Verifying` | `Open` | verify exit `1` once the settle window has passed |
| any | `Closed` | `spec.closed: true` |

The settle window (5 minutes by default) gives a fix time to take effect. It
starts when the incident enters `Verifying` (`status.verifyingSince`): at the
precondition exit `0` or the apply that moved it. Inside the window a verify
exit `1` is recorded and verify runs again at the next poll; the first exit `1`
at or after the window's end moves the incident back to `Open`. An exit other
than `0` or `1`, or a timeout, never moves it, inside the window or after. A
human applying again while `Verifying`, or an IncidentApply run that exits `0`,
restarts the window; a failed IncidentApply run is recorded but does not.

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

`status.howToFix` holds bash scripts written by the analysis:

| Script | Run by | When |
|---|---|---|
| `precondition` | the controller, in a read-only sandbox | every poll interval while `Open` |
| `apply` | a human: their own terminal, or the portal's Apply; or the controller, as the user who asks with Run apply | when they decide |
| `verify` | the controller, in a read-only sandbox | while `Verifying` |
| `rollback` | a human, to undo apply | when an applied fix must be reverted; it moves no state |

Exit `0` means the incident is gone, `1` that it holds; any other exit, or a
timeout, is unknown and changes no state. The precondition tests the
root-cause object, never the alert's rows, so it still reports the right
answer when the alert fires for another reason.

## On provider-runtime

The Incident is a provider-runtime managed resource: it embeds provider-runtime's
`ConditionedStatus` and satisfies `resource.Managed`. It has no external
resource: Observe always reports it existing, and decides from the Incident
whether there is work to do. Check pods only carry the runs, and are where a
run's result is read.

| provider-runtime | incident-controller |
|---|---|
| `ExternalClient.Observe` | reads the incident's check pods; records a finished one in `status.lastChecks` and applies its transition; applies `spec.closed` and `spec.applied`. A status to persist, a check pod to delete or a check that is due make the Incident out of date. It changes nothing but the Incident held in memory |
| `Create` | never called |
| `Update` | persists the status when it changed, then sets `spec.applied` back to `false`, deletes finished and stale check pods, and starts the next check when it is due: precondition when `Open`, verify when `Verifying` |
| `Delete` + finalizer | the Incident is deleted: removes its check pods and ConfigMaps |
| `WithPollInterval(1m)` | the 60 s check cadence; a poll-interval hook requeues when the next check falls due |
| `krateo.io/paused` | pauses one incident: no checks, no transitions, `spec.closed` included |

A check is due a poll interval after the last one ended, or after the
incident entered `Verifying` when that is later. The controller holds the end
of each incident's last run in memory, so after a restart every check is due at
once. A check pod's watch events requeue its incident, so a finished check is
read at once. Update writes the status before anything else, so a result is
never lost with its pod and a consumed `spec.applied` is never lost with its
flag. A result whose pod outlives a failed delete is recorded again and changes
nothing. A running check of a script that is no longer current (the incident
was applied or closed meanwhile) is deleted and its result dropped.

### Writes

`status.lastChecks` keeps one result per script, with `since`: when the script
started giving it. A precondition or verify run that gives the same result as
the last leaves the status as it is, so the controller writes nothing. An Open
incident whose precondition keeps exiting `1` is not written to until the
result changes, a human acts, or it is closed; a watcher of Incidents sees no
event from it. A result that changes, a transition, and every apply run are
one status write each.

## Run apply

An **IncidentApply** asks the controller to run one Incident's apply script as
the user who creates it. The portal's Run apply creates one, as the clicking
user, through snowplow.

1. **Who asked.** The `incidentapply-requested-by` MutatingAdmissionPolicy, in
   the CRD chart, writes the user the apiserver authenticated for the create
   (`request.userInfo`) into `spec.requestedBy`, over anything the client sent.
   The CRD keeps `spec` immutable after that.
2. **Admission.** The controller runs the script only when the policy and its
   binding existed before the request, the incident is `Open` with an apply
   script, no other request for it is pending or running, the user may `patch`
   the incident (a SubjectAccessReview), and the user's login credentials
   exist, are theirs and outlast the run. Otherwise the request is `Rejected`
   with the reason.
3. **The run.** The controller reads the user's `<username>-clientconfig`
   Secret, the client certificate authn issued them at login, and writes a
   kubeconfig from it into a Secret owned by the apply pod. The apply pod is a
   check pod with no service account token: kubectl reaches the apiserver by
   IP as that user, so RBAC judges every command on the user's own rights.
4. **The result.** The exit code and the end of the output go into the
   request's status, and one `ApplyFinished` Event. The run is also an `apply`
   check on the incident with its exit; exit `0` moves an `Open` incident to
   `Verifying`, as `spec.applied` does. The apply pod, its Secret and its
   ConfigMap are deleted once the run ends.
5. **Retention.** The controller deletes a finished IncidentApply its TTL
   (`apply.ttl`, 30 days by default) after `finishedAt`.

## Check pods

Each run is a pod in a dedicated namespace (`krateo-incident-checks` by
default), holding only the check pods and their read-only service account:

- the script from a ConfigMap owned by the pod, run with bash; the image has
  bash, kubectl and jq;
- the service account `incident-check`, bound to `view` and to
  `incident-controller-check-reader`, which reads the listed Krateo API groups;
  no Secrets, no writes;
- no DNS, and a NetworkPolicy allowing egress to the apiserver only, and no
  ingress;
- `activeDeadlineSeconds: 60`, and the `restricted` Pod Security Standard.

Apply pods differ in one way: they mount no service account token, and
kubectl uses the requester's kubeconfig. Details: [configuration](./configuration.md).

## Layout

| Path | What |
|---|---|
| `apis/incident/v1alpha1` | the Go types, the source of truth |
| `helm/incident-controller-crds` | the CRD chart: the CRDs, generated from the Go types, and the `incidentapply-requested-by` admission policy |
| `helm/incident-controller` | the controller chart: Deployment, RBAC, the checks namespace, the check pod template, the NetworkPolicy, the unbound `krateo-incident-viewer` and `krateo-incident-responder` roles |
| `main.go`, `internal/controllers/incident` | the controller: `machine.go` is the state machine, `checkpod.go` the check and apply pods, `incident.go` the Incident's provider-runtime client, `apply.go` the IncidentApply's |
| `check/Dockerfile` | the check pod image |
| `examples/incident`, `examples/incidentapply` | a sample Incident and IncidentApply, validated by the unit tests |
