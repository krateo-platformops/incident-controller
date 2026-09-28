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
| alert-provider (the writer) | creates the Incident with its label and `spec`; writes the analysis, `howToFix`, `firings` and `lastFiredAt`; sets `Analyzing`, then `Open` |
| incident-controller | runs precondition and verify; writes `checks`, every other state transition, `resolution` and the conditions |
| a human, through the portal | `spec.applied` ("I applied it", or Apply) and `spec.closed` (Close) |

Field by field: [api](./api.md).

## Many incidents per alert

The writer evaluates each firing alert about every 60 s. A firing counts on
the open incident (any state but `Resolved` and `Closed`) that describes the
same problem: it adds one to that incident's `status.firings`, sets
`status.lastFiredAt` and runs no analysis. An LLM compares the firing with each
open incident to decide. When none is the same problem, the firing opens a new
incident with its own analysis, so an alert can have several open incidents at
once. An incident still `Analyzing`, or whose analysis failed, takes the firing
without a comparison. [The writer's docs](https://github.com/krateo-platformops/alert-troubleshooter/blob/main/docs/overview.md)
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
| `Open` | `Verifying` | precondition exit `0`, or `spec.applied`, which the controller consumes: it appends an `apply` check and sets `applied` back to `false` |
| `Verifying` | `Resolved` | verify exit `0`; verify retries within a settle window (~5 min) before it counts as `1` |
| `Verifying` | `Open` | verify exit `1` |
| any | `Closed` | `spec.closed: true` |

`Resolved` means a script proved the problem gone; `Closed` means a person
decided. `Closed` is final, and a `Resolved` incident can only become `Closed`.

The first precondition run must exit `1`. An exit `0` there means the script
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
| `apply` | a human: their own terminal, or the portal's Apply | when they decide |
| `verify` | the controller, in a read-only sandbox | while `Verifying` |
| `rollback` | a human, to undo apply | when an applied fix must be reverted; it moves no state |

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
| `ExternalClient.Observe` | reads the current check pod's exit code into `status.checks` and `status.state` |
| `Create` | no check pod for the current step: starts one, precondition when `Open`, verify when `Verifying` |
| `Update` | the last check is older than the poll interval: starts the next one |
| `Delete` + finalizer | the Incident is deleted: removes its check pods |
| `WithPollInterval(1m)` | the 60 s check cadence |
| `krateo.io/paused` | pauses the checks of one incident |

## Layout

| Path | What |
|---|---|
| `apis/incident/v1alpha1` | the Go types, the source of truth |
| `helm/incident-controller-crds` | the CRD chart; its template is generated from the Go types |
| `examples/incident` | a sample Incident, validated by the unit tests |
