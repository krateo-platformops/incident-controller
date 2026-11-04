---
type: Log
title: incident-provider — log
description: Curated chronological history of incident-provider — notable changes and decisions, newest first.
resource: github.com/krateo-platformops/incident-provider
tags: [controller, history]
timestamp: 2026-11-04T00:00:00Z
---

# Log

Curated history — notable changes and decisions, newest first. Full release
notes live in GitHub Releases. Entries record what was true when written.

## 2026-11-04 — provider-runtime v1.8.0: the Ready contract (unreleased)

provider-runtime v1.8.0 sets `Ready` to `Unknown/NotObserved` when Connect or
Observe fails, and expects every Observe that succeeds to set `Ready` again;
one that doesn't gets a `ReadyNotSetAfterObserve` Warning. Both controllers
already set `Available` on their main paths. Changed:
- The deletion paths of both Observes set `Deleting`. They set nothing
  before, so a deleted Incident or IncidentApply would have kept
  `NotObserved` after a failed Observe.
- The IncidentApply's errors after it set `Available` (reading the run's
  ConfigMap, admission's reads) are wrapped with `reconciler.ReadyAssessed`,
  so the runtime keeps `Available` next to the error.
- The example precondition treats only `Ready=True` as gone. It held only
  on `Ready=False`, so a `NotObserved` CompositionDefinition read as fixed.

Not to be released before the core-provider release whose rollup maps
`NotObserved` (core-provider#310).

## 2026-11-03 — 0.6.1: keep the requested-by admission policy

The CRD chart's MutatingAdmissionPolicy `incidentapply-requested-by` and its
binding now carry `helm.sh/resource-policy: keep`, like the two CRDs. Without
it, uninstalling the CRD chart, or swapping it for another chart, deleted them
with no ownership check. Recreated, they are newer than every IncidentApply
already in the cluster, and the controller's `vouched` check refuses each of
those requests. The 0.5.x line gets the same change.

## 2026-11-01 — renamed from incident-controller (unreleased)

The component is now incident-provider, the name the other Krateo providers
use. Releases up to 0.5.2 were published as incident-controller.

Renamed:
- Go module `github.com/krateo-platformops/incident-controller` →
  `github.com/krateo-platformops/incident-provider`. Nothing else imports it.
- Images `ghcr.io/krateo-platformops/incident-controller` and
  `…/incident-controller-check` → `…/incident-provider` and
  `…/incident-provider-check`. The old packages stay in ghcr with their
  history; the new ones start empty.
- Charts `helm/incident-controller` and `helm/incident-controller-crds` →
  `helm/incident-provider` and `helm/incident-provider-crds`: directories,
  Chart `name`, `description` and `sources`, the `_helpers.tpl` template
  names, schema titles, NOTES.txt. Every object name the controller chart
  derives from the chart name follows: the Deployment, the ServiceAccount
  default, the ClusterRoles and bindings (`incident-provider`,
  `-checks`, `-check-reader`, `-credentials`, `-leader-election`) and the
  ConfigMap `incident-provider-check-pod`.
- `service.name` `incident-controller` → `incident-provider`. The log alert
  exclusions in observability and alert-provider match on it and must move
  in the same release, or the "Reconciler error" alert loop returns.
- Env var prefix `INCIDENT_CONTROLLER_` → `INCIDENT_PROVIDER_`. The chart sets
  the new names; a deployment that sets the old ones by hand loses them.
- Leader election Lease `incident-controller.observability.krateo.io` →
  `incident-provider.observability.krateo.io`. The old and new controllers
  don't see each other's lease, so the old one must be stopped before the
  new one starts.
- Check pod template path `/etc/incident-controller/check-pod.yaml` →
  `/etc/incident-provider/check-pod.yaml`, in the flag default and the chart
  mount.
- The `app.kubernetes.io/managed-by` label on check and apply pods
  `incident-controller` → `incident-provider`. Nothing selects on it: the
  controller finds pods by incident or apply UID, the NetworkPolicy by
  `app.kubernetes.io/component: incident-check`, so pods started by the old
  controller are still picked up.
- README, docs and examples.

Kept, deliberately:
- The API: group `observability.krateo.io`, version `v1alpha1`, kinds
  `Incident` and `IncidentApply`. The generated CRDs are byte-identical to
  before; only their directory moved. Clusters hold live Incidents (633 across
  two clusters on 2026-11-01), and deleting a CRD deletes every object of it.
  Both CRDs and both user ClusterRoles still carry `helm.sh/resource-policy: keep`
  (since 0.5.2) in their new location.
- The CRD side's installer identity (composition `incident-controller-crd`,
  kind `IncidentControllerCrds`) is outside this repo. Installer keeps it so
  the CRD release is upgraded in place to the renamed chart, never
  uninstalled. The charts derive names from the chart name, not the release
  name, so any release name works.
- The ClusterRoles `krateo-incident-viewer` and `krateo-incident-responder`:
  portal's `sre-incident-responder` binding points at the responder role by
  name.
- Label and annotation keys under `observability.krateo.io/`, the component
  label `incident-check`, and the admission policy `incidentapply-requested-by`.
- Past release notes and tags (0.2.0 … 0.5.2) as published.

Corrected: the 0.5.2 README said that deleting a Krateo composition that
installed the CRD chart still deleted the CRDs, because core-provider deleted
every release object with finalizers before uninstalling. It doesn't:
core-provider's teardown reads each object and skips it when it has no
finalizers, and the installed CRDs have none at rest. So keep protects the
CRDs and the user ClusterRoles on composition deletion as well as on
`helm uninstall`.
