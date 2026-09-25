# incident-controller

Krateo incident controller: the `Incident` custom resource
(`incidents.observability.krateo.io`) and the controller, built on
[provider-runtime](https://github.com/krateo-platformops/provider-runtime),
that runs each incident's checks and moves it through its lifecycle.

## What is this

An Incident is one occurrence of an Alert's problem. alert-troubleshooter opens
it and writes the root-cause analysis with three bash scripts (precondition,
apply, verify); this controller runs precondition and verify in a read-only
sandbox and moves the incident from `Open` through `Verifying` to `Resolved`;
a human runs apply and can close the incident at any time.

This repo holds the API (Go types, the generated CRD and its chart), the
controller, its chart, and the check pod image.

## Install

Two charts, versioned from the git tag at release and published to
`oci://ghcr.io/krateo-platformops/charts`: `incident-controller-crds` (the
CRD) and `incident-controller` (the controller). Install the CRD chart first:

```sh
helm install incident-controller-crds oci://ghcr.io/krateo-platformops/charts/incident-controller-crds --version <tag>
helm install incident-controller oci://ghcr.io/krateo-platformops/charts/incident-controller --version <tag> -n krateo-system
```

## Configure

The CRD chart has no values. The controller's flags, chart values, and the
check pods' RBAC and network policy: [configuration](docs/configuration.md).

## Examples

- [`examples/incident`](examples/incident/README.md): an Incident its verify
  script resolved.

## Docs

- [overview](docs/overview.md): the lifecycle, who writes what, the
  provider-runtime mapping.
- [api](docs/api.md): the CRD field by field and the rules the apiserver
  enforces.
- [configuration](docs/configuration.md): flags, chart values, what the check
  pods and the controller may do.

## Develop & release

```sh
make generate        # deepcopy + the CRD, from apis/ into helm/incident-controller-crds/templates
make check-generate  # fail if the committed generated files are stale
make build lint test
make lint-charts     # helm lint and render both charts
make check           # all of the above, as CI runs it
make image image-check
```

Chart versions are the `CHART_VERSION` placeholder, which the release
substitutes with the git tag.
