---
type: Example
title: incident — a resolved Incident
description: A sample Incident that its verify script resolved, with every field of the schema that a writer or the controller sets.
resource: github.com/krateo-platformops/incident-controller
tags: [example, incident, crd]
timestamp: 2026-09-25T00:00:00Z
---

# incident

[`incident.yaml`](./incident.yaml) is one Incident of the Alert
`compositiondefinitions-not-ready`, from its opening firing to its resolution:

1. The alert fired at 14:00 and opened the incident; six more firings counted
   on it (`status.firings: 7`).
2. The root-cause analysis wrote the report, the evidence and the three
   `howToFix` scripts, and the incident became `Open`.
3. The precondition exited `1` twice (`Reproduced=True`): the incident held.
4. A human ran the apply script in a terminal and clicked "I applied it"
   (`spec.applied: true`, the `apply` check with no exit code).
5. The verify script exited `0`: the incident is `Resolved`, by `verify`.

The unit tests validate this file against the generated CRD and decode it into
the Go types and back, so it stays a valid, complete sample of the API.

Apply it to a cluster that has the CRD (status is a subresource, so a plain
apply keeps only `metadata` and `spec`):

```sh
kubectl apply -f helm/incident-controller-crds/templates/
kubectl apply -f examples/incident/incident.yaml
kubectl get incidents -n krateo-system
```
