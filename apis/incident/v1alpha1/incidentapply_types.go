package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	prv1 "github.com/krateo-platformops/provider-runtime/apis/common/v1"
	"github.com/krateo-platformops/provider-runtime/pkg/resource"
)

// RequesterPolicy names the MutatingAdmissionPolicy, and its binding, that writes
// spec.requestedBy of every IncidentApply at creation. The controller runs no IncidentApply
// created before both existed.
const RequesterPolicy = "incidentapply-requested-by"

// MaxApplyOutput is the most output, in bytes, an IncidentApply keeps: the end of the run's
// stdout and stderr.
const MaxApplyOutput = 4096

// ReasonApplyFinished is the reason of the one Event an IncidentApply gets when it reaches a
// terminal phase. Its message starts with the phase.
const ReasonApplyFinished = "ApplyFinished"

// ApplyPhase is where an IncidentApply's run is.
// +kubebuilder:validation:Enum=Running;Succeeded;Failed;Rejected
type ApplyPhase string

const (
	// ApplyRunning: the apply pod exists. Absent phase: the controller has not acted yet.
	ApplyRunning ApplyPhase = "Running"
	// ApplySucceeded: the script exited 0.
	ApplySucceeded ApplyPhase = "Succeeded"
	// ApplyFailed: the script ran and exited non-zero, or did not finish in time.
	ApplyFailed ApplyPhase = "Failed"
	// ApplyRejected: the script never ran; message says why.
	ApplyRejected ApplyPhase = "Rejected"
)

// Terminal reports whether the phase is final.
func (p ApplyPhase) Terminal() bool {
	return p == ApplySucceeded || p == ApplyFailed || p == ApplyRejected
}

// IncidentRef locates an Incident in the referrer's namespace.
type IncidentRef struct {
	// Name of the Incident.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

// Requester is the user the apiserver authenticated for a request.
type Requester struct {
	// Username is the authenticated user's name.
	// +kubebuilder:validation:MinLength=1
	Username string `json:"username"`

	// Groups are the authenticated user's groups.
	// +optional
	Groups []string `json:"groups,omitempty"`
}

// IncidentApplySpec names the incident whose apply script runs, and who asked.
type IncidentApplySpec struct {
	// IncidentRef is the Incident whose apply script runs, in this object's namespace.
	IncidentRef IncidentRef `json:"incidentRef"`

	// RequestedBy is the user who created this object. The incidentapply-requested-by admission
	// policy writes it from the apiserver's authenticated user, over anything the client sent.
	// The script runs with this user's credentials.
	RequestedBy Requester `json:"requestedBy"`
}

// IncidentApplyStatus is the outcome of the run.
type IncidentApplyStatus struct {
	prv1.ConditionedStatus `json:",inline"`

	// Phase is where the run is. It is absent until the controller acts.
	// +optional
	Phase ApplyPhase `json:"phase,omitempty"`

	// Message is why the run was rejected, or how it ended.
	// +optional
	Message string `json:"message,omitempty"`

	// Script is the apply script as it ran.
	// +optional
	Script string `json:"script,omitempty"`

	// ExitCode is the script's exit code. It is absent when the run produced none.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=255
	// +optional
	ExitCode *int32 `json:"exitCode,omitempty"`

	// Output is the end of the run's stdout and stderr, at most 4096 bytes.
	// +optional
	Output string `json:"output,omitempty"`

	// StartedAt is when the apply pod was created.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// FinishedAt is when the run ended, or when the request was rejected. The controller deletes
	// the IncidentApply its TTL after this.
	// +optional
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
}

// +kubebuilder:object:root=true

// An IncidentApply is one request to run an Incident's apply script as the user who creates it.
// The controller runs the script in a check pod with that user's own credentials, records the
// result here and on the Incident, and deletes the IncidentApply a TTL after it finishes.
// +kubebuilder:printcolumn:name="Incident",type="string",JSONPath=".spec.incidentRef.name"
// +kubebuilder:printcolumn:name="User",type="string",JSONPath=".spec.requestedBy.username"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Exit",type="integer",JSONPath=".status.exitCode"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=incapply,categories={krateo,observability}
type IncidentApply struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable"
	Spec   IncidentApplySpec   `json:"spec"`
	Status IncidentApplyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// IncidentApplyList contains a list of IncidentApply.
type IncidentApplyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []IncidentApply `json:"items"`
}

var (
	_ resource.Managed     = &IncidentApply{}
	_ resource.ManagedList = &IncidentApplyList{}
)

// GetCondition of this IncidentApply.
func (mg *IncidentApply) GetCondition(ct prv1.ConditionType) prv1.Condition {
	return mg.Status.GetCondition(ct)
}

// SetConditions of this IncidentApply.
func (mg *IncidentApply) SetConditions(c ...prv1.Condition) {
	mg.Status.SetConditions(c...)
}

// GetItems of this IncidentApplyList.
func (l *IncidentApplyList) GetItems() []resource.Managed {
	items := make([]resource.Managed, len(l.Items))
	for i := range l.Items {
		items[i] = &l.Items[i]
	}
	return items
}
