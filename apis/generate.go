//go:build generate
// +build generate

// Generate deepcopy methodsets and the CRD manifests, straight into the CRD chart.
//go:generate go run -tags generate sigs.k8s.io/controller-tools/cmd/controller-gen object:headerFile=../hack/boilerplate.go.txt paths=./... crd:crdVersions=v1 output:artifacts:config=../helm/incident-controller-crds/templates

package apis

import (
	_ "sigs.k8s.io/controller-tools/cmd/controller-gen" //nolint:typecheck
)
