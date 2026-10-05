package v1alpha1_test

import (
	"strings"
	"testing"

	"github.com/krateo-platformops/incident-controller/apis/incident/v1alpha1"
)

const (
	applyCRDPath     = "../../../helm/incident-controller-crds/templates/observability.krateo.io_incidentapplies.yaml"
	applyExamplePath = "../../../examples/incidentapply/incidentapply.yaml"
)

func applyExample(t *testing.T) map[string]any {
	t.Helper()
	var obj map[string]any
	readYAML(t, applyExamplePath, &obj)
	return obj
}

func TestIncidentApplyValidation(t *testing.T) {
	v := newValidatorOf(t, applyCRDPath)

	cases := []validationCase{
		{name: "the example is valid", obj: applyExample},
		{
			name: "requestedBy is required",
			obj: func(t *testing.T) map[string]any {
				obj := applyExample(t)
				delete(spec(obj), "requestedBy")
				return obj
			},
			wantErr: "requestedBy",
		},
		{
			name: "the incident is required",
			obj: func(t *testing.T) map[string]any {
				obj := applyExample(t)
				delete(spec(obj), "incidentRef")
				return obj
			},
			wantErr: "incidentRef",
		},
		{
			name: "a phase outside the enum",
			obj: func(t *testing.T) map[string]any {
				obj := applyExample(t)
				status(obj)["phase"] = "Done"
				return obj
			},
			wantErr: "phase",
		},
		{
			name: "requestedBy cannot change",
			old:  applyExample,
			obj: func(t *testing.T) map[string]any {
				obj := applyExample(t)
				spec(obj)["requestedBy"] = map[string]any{"username": "cluster-admin"}
				return obj
			},
			wantErr: "spec is immutable",
		},
		{
			name: "the incident cannot change",
			old:  applyExample,
			obj: func(t *testing.T) map[string]any {
				obj := applyExample(t)
				spec(obj)["incidentRef"] = map[string]any{"name": "another"}
				return obj
			},
			wantErr: "spec is immutable",
		},
		{
			name: "status can change",
			old:  applyExample,
			obj: func(t *testing.T) map[string]any {
				obj := applyExample(t)
				status(obj)["phase"] = "Failed"
				return obj
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var old map[string]any
			if tc.old != nil {
				old = tc.old(t)
			}
			errs := v.validate(tc.obj(t), old)
			if tc.wantErr == "" {
				if len(errs) > 0 {
					t.Fatalf("want valid, got %v", errs)
				}
				return
			}
			if !strings.Contains(errs.ToAggregate().Error(), tc.wantErr) {
				t.Fatalf("want an error naming %q, got %v", tc.wantErr, errs)
			}
		})
	}
}

func TestIncidentApplyConstantsMatchCRD(t *testing.T) {
	s := openAPISchemaOf(t, applyCRDPath)
	phase := s.Properties["status"].Properties["phase"]
	var enum []string
	for _, e := range phase.Enum {
		enum = append(enum, e.(string))
	}
	want := []string{string(v1alpha1.ApplyRunning), string(v1alpha1.ApplySucceeded), string(v1alpha1.ApplyFailed), string(v1alpha1.ApplyRejected)}
	if strings.Join(enum, ",") != strings.Join(want, ",") {
		t.Errorf("phase enum: want %v, got %v", want, enum)
	}
	if s.Properties["status"].Properties["output"].Description == "" || v1alpha1.MaxApplyOutput != 4096 {
		t.Error("output is documented and capped at 4096 bytes")
	}
}
