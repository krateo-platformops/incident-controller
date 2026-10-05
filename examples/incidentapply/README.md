---
type: Example
title: incidentapply — one Run apply click
description: A sample IncidentApply whose run, as the user who created it, exited 0.
resource: github.com/krateo-platformops/incident-controller
tags: [example, incidentapply, crd]
timestamp: 2026-10-05T00:00:00Z
---

# incidentapply

[`incidentapply.yaml`](./incidentapply.yaml) is one request to run the apply script of the
Incident in [`../incident`](../incident):

1. alice clicked **Run apply** on the incident page. The portal created this object as alice,
   naming only the incident.
2. The `incidentapply-requested-by` admission policy wrote `spec.requestedBy` from the user the
   apiserver authenticated.
3. incident-controller checked the incident was Open with an apply script, that alice may patch
   it, and that her login outlasts the run. It ran the script in a check pod with alice's own
   client certificate.
4. The script exited `0`: the run is `Succeeded`, the incident got an `apply` check with exit `0`
   and moved to `Verifying`, and the object got one `ApplyFinished` Event.

The controller deletes the object its TTL (`apply.ttl`, 30 days by default) after `finishedAt`.

The unit tests validate this file against the generated CRD.

Create one by hand (the policy fills `requestedBy`; status is the controller's):

```sh
kubectl create -f - <<YAML
apiVersion: observability.krateo.io/v1alpha1
kind: IncidentApply
metadata:
  generateName: compositiondefinitions-not-ready-20260925-140000-apply-
  namespace: krateo-system
spec:
  incidentRef:
    name: compositiondefinitions-not-ready-20260925-140000
YAML
```
