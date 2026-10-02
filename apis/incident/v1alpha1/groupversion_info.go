// Package v1alpha1 contains the Incident API of the observability.krateo.io group.
// +kubebuilder:object:generate=true
// +groupName=observability.krateo.io
// +versionName=v1alpha1
package v1alpha1

import (
	"reflect"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

// Package type metadata.
const (
	Group   = "observability.krateo.io"
	Version = "v1alpha1"
)

var (
	// SchemeGroupVersion is group version used to register these objects.
	SchemeGroupVersion = schema.GroupVersion{Group: Group, Version: Version}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: SchemeGroupVersion}
)

var (
	IncidentKind             = reflect.TypeOf(Incident{}).Name()
	IncidentGroupKind        = schema.GroupKind{Group: Group, Kind: IncidentKind}.String()
	IncidentKindAPIVersion   = IncidentKind + "." + SchemeGroupVersion.String()
	IncidentGroupVersionKind = SchemeGroupVersion.WithKind(IncidentKind)
)

func init() {
	SchemeBuilder.Register(&Incident{}, &IncidentList{})
}
