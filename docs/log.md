---
type: Log
title: incident-provider — log
description: Curated chronological history of incident-provider — notable changes and decisions, newest first.
resource: github.com/krateo-platformops/incident-provider
tags: [controller, history]
timestamp: 2026-11-01T00:00:00Z
---

# Log

Curated history — notable changes and decisions, newest first. Full release
notes live in GitHub Releases. Entries record what was true when written.

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
