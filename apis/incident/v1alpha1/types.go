package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	prv1 "github.com/krateo-platformops/provider-runtime/apis/common/v1"
	"github.com/krateo-platformops/provider-runtime/pkg/resource"
)

// LabelAlert carries the name of the Alert that opened an Incident. Writers set it at creation,
// with the value of spec.alertRef.name, so one alert's incidents list with a label selector in the
// alert's namespace.
const LabelAlert = "observability.krateo.io/alert"

// MaxChecks is the length of the check history: writers keep the newest MaxChecks entries of
// status.checks. It matches the MaxItems marker on IncidentStatus.Checks.
const MaxChecks = 20

// State is the lifecycle state of an Incident.
// +kubebuilder:validation:Enum=Analyzing;Open;Verifying;Resolved;Closed
type State string

const (
	// StateAnalyzing: the root-cause analysis runs. No check runs.
	StateAnalyzing State = "Analyzing"
	// StateOpen: the incident holds. The precondition runs every poll interval.
	StateOpen State = "Open"
	// StateVerifying: the precondition exited 0, or a human applied the fix. The verify script runs.
	StateVerifying State = "Verifying"
	// StateResolved: the verify script exited 0.
	StateResolved State = "Resolved"
	// StateClosed: a human closed the incident.
	StateClosed State = "Closed"
)

// Trigger is what started the investigation.
// +kubebuilder:validation:Enum=alert;composition-condition;user-ask
type Trigger string

const (
	TriggerAlert                Trigger = "alert"
	TriggerCompositionCondition Trigger = "composition-condition"
	TriggerUserAsk              Trigger = "user-ask"
)

// Script names one of the three how-to-fix scripts.
// +kubebuilder:validation:Enum=precondition;apply;verify
type Script string

const (
	ScriptPrecondition Script = "precondition"
	ScriptApply        Script = "apply"
	ScriptVerify       Script = "verify"
)

// ResolvedBy is what ended an incident.
// +kubebuilder:validation:Enum=verify;user
type ResolvedBy string

const (
	// ResolvedByVerify: the verify script exited 0. Goes with state Resolved.
	ResolvedByVerify ResolvedBy = "verify"
	// ResolvedByUser: a human closed the incident. Goes with state Closed.
	ResolvedByUser ResolvedBy = "user"
)

// SourceType is the kind of a piece of evidence.
// +kubebuilder:validation:Enum=logs;events;metrics;object
type SourceType string

const (
	SourceLogs    SourceType = "logs"
	SourceEvents  SourceType = "events"
	SourceMetrics SourceType = "metrics"
	SourceObject  SourceType = "object"
)

// TypeReproduced is a condition on the first precondition run. True: it exited 1, so the script
// sees the incident. False: it exited 0, so the script cannot see the incident. Such an incident is
// flagged: it stays Open, its precondition no longer runs, and only spec.applied or spec.closed
// moves it. Absent until the first run.
const TypeReproduced prv1.ConditionType = "Reproduced"

// Reasons of the Reproduced condition.
const (
	ReasonPreconditionHolds  prv1.ConditionReason = "PreconditionHolds"
	ReasonPreconditionPassed prv1.ConditionReason = "PreconditionPassed"
)

// AlertRef locates the Alert that opened an Incident.
type AlertRef struct {
	// Name of the Alert. It is also the value of the observability.krateo.io/alert label, so it is
	// at most 63 characters.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	Name string `json:"name"`

	// Namespace of the Alert. Writers create the Incident in this namespace.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Namespace string `json:"namespace"`
}

// IncidentSpec is what opened the incident and what humans decided about it.
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.closed) || !oldSelf.closed || (has(self.closed) && self.closed)",message="closed cannot be unset"
type IncidentSpec struct {
	// AlertRef is the Alert that opened the incident. It is immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="alertRef is immutable"
	AlertRef AlertRef `json:"alertRef"`

	// Trigger is what started the investigation.
	// +optional
	Trigger Trigger `json:"trigger,omitempty"`

	// Prompt is the root-cause-analysis prompt sent to incident-agent.
	// +optional
	Prompt string `json:"prompt,omitempty"`

	// TriggeredAt is when the firing that opened the incident arrived.
	// +optional
	TriggeredAt *metav1.Time `json:"triggeredAt,omitempty"`

	// Applied says a human ran the apply script: in a terminal followed by "I applied it", or with
	// the portal's Apply. The controller consumes it: it moves an Open incident to Verifying,
	// appends an apply check and sets applied back to false.
	// +optional
	Applied bool `json:"applied,omitempty"`

	// Closed says a human closed the incident. It moves any state to Closed and cannot be unset.
	// +optional
	Closed bool `json:"closed,omitempty"`
}

// HowToFix is the fix as three bash scripts, written by the root-cause analysis. The controller runs
// precondition and verify in a read-only sandbox; a human runs apply. For precondition and verify,
// exit 0 means the incident is gone and exit 1 that it holds; any other exit, or a timeout, is
// unknown and changes nothing.
type HowToFix struct {
	// Precondition tests whether the incident still holds. It tests the root-cause object, never
	// the alert's rows.
	// +optional
	Precondition string `json:"precondition,omitempty"`

	// Apply is the fix. A human reviews and runs it.
	// +optional
	Apply string `json:"apply,omitempty"`

	// Verify tests whether the fix worked.
	// +optional
	Verify string `json:"verify,omitempty"`
}

// Check is one run of a how-to-fix script.
type Check struct {
	// Script is the script that ran.
	Script Script `json:"script"`

	// Exit is the script's exit code. It is absent when the run produced none (a timeout, a pod
	// that never ran) and on the apply check that records a consumed spec.applied.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=255
	// +optional
	Exit *int32 `json:"exit,omitempty"`

	// At is when the run finished.
	At metav1.Time `json:"at"`
}

// Resolution records how an incident ended.
type Resolution struct {
	// By is what ended it: verify (state Resolved) or user (state Closed).
	By ResolvedBy `json:"by"`

	// At is when it ended.
	At metav1.Time `json:"at"`
}

// RootCause is the conclusion of the root-cause analysis.
type RootCause struct {
	// Statement is the root cause.
	// +optional
	Statement string `json:"statement,omitempty"`

	// Confidence is a decimal string in [0, 1], e.g. "0.85".
	// +optional
	Confidence string `json:"confidence,omitempty"`

	// Category is the failure class: config, capacity, image, network, dependency, other.
	// +optional
	Category string `json:"category,omitempty"`
}

// Source is one piece of evidence the analysis rests on.
type Source struct {
	// Type is the kind of evidence.
	// +optional
	Type SourceType `json:"type,omitempty"`

	// Ref is where the evidence came from: a query, an object reference, a metric name.
	// +optional
	Ref string `json:"ref,omitempty"`

	// Excerpt is the relevant part of the evidence, verbatim.
	// +optional
	Excerpt string `json:"excerpt,omitempty"`
}

// ReasoningStep is one step of the reasoning trace.
type ReasoningStep struct {
	// Step is the 1-based position of this step.
	// +optional
	Step int32 `json:"step,omitempty"`

	// Statement is what this step concludes.
	// +optional
	Statement string `json:"statement,omitempty"`

	// EvidenceRefs are the 0-based indices into status.sources that back this step.
	// +optional
	EvidenceRefs []int32 `json:"evidenceRefs,omitempty"`
}

// AnalyzedResource is a cluster object the analysis read.
type AnalyzedResource struct {
	// GVR is the object's group/version/resource, e.g. apps/v1/deployments.
	// +optional
	GVR string `json:"gvr,omitempty"`

	// +optional
	Name string `json:"name,omitempty"`

	// +optional
	Namespace string `json:"namespace,omitempty"`

	// WhatWasRead is what of the object the analysis inspected: status, events, spec, ...
	// +optional
	WhatWasRead string `json:"whatWasRead,omitempty"`
}

// IncidentStatus is the incident's lifecycle, its checks and its root-cause analysis.
// +kubebuilder:validation:XValidation:rule="has(self.resolution) == (has(self.state) && self.state in ['Resolved', 'Closed'])",message="resolution is set exactly when state is Resolved or Closed"
// +kubebuilder:validation:XValidation:rule="!has(self.resolution) || (has(self.state) && ((self.resolution.by == 'verify') == (self.state == 'Resolved')))",message="Resolved goes with resolution.by verify, Closed with resolution.by user"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.state) || !(oldSelf.state in ['Resolved', 'Closed']) || (has(self.state) && (self.state == oldSelf.state || self.state == 'Closed'))",message="Resolved can only become Closed, and Closed is final"
type IncidentStatus struct {
	prv1.ConditionedStatus `json:",inline"`

	// State is the lifecycle state. Writers set Analyzing in the first status write.
	// +optional
	State State `json:"state,omitempty"`

	// Firings counts the alert firings this incident covers, the one that opened it included.
	// +kubebuilder:validation:Minimum=0
	// +optional
	Firings int32 `json:"firings,omitempty"`

	// LastFiredAt is when the alert last fired for this incident.
	// +optional
	LastFiredAt *metav1.Time `json:"lastFiredAt,omitempty"`

	// HowToFix is the fix the root-cause analysis wrote.
	// +optional
	HowToFix *HowToFix `json:"howToFix,omitempty"`

	// Checks is the history of script runs, oldest first. Writers keep the newest 20.
	// +kubebuilder:validation:MaxItems=20
	// +optional
	Checks []Check `json:"checks,omitempty"`

	// Resolution records how the incident ended. It is set exactly when state is Resolved or Closed.
	// +optional
	Resolution *Resolution `json:"resolution,omitempty"`

	// Report is the root-cause analysis in markdown.
	// +optional
	Report string `json:"report,omitempty"`

	// RootCause is the analysis' conclusion.
	// +optional
	RootCause *RootCause `json:"rootCause,omitempty"`

	// Sources is the evidence the analysis rests on. reasoningTrace steps cite it by 0-based index.
	// +optional
	Sources []Source `json:"sources,omitempty"`

	// ReasoningTrace is the ordered reasoning, each step citing its sources.
	// +optional
	ReasoningTrace []ReasoningStep `json:"reasoningTrace,omitempty"`

	// AnalyzedResources are the cluster objects the analysis read.
	// +optional
	AnalyzedResources []AnalyzedResource `json:"analyzedResources,omitempty"`

	// MissingContext is what the analysis could not see but needed.
	// +optional
	MissingContext []string `json:"missingContext,omitempty"`

	// Assumptions are what the analysis assumed for lack of context.
	// +optional
	Assumptions []string `json:"assumptions,omitempty"`

	// Error is why the root-cause analysis failed.
	// +optional
	Error string `json:"error,omitempty"`

	// CompletedAt is when the root-cause analysis finished.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`
}

// +kubebuilder:object:root=true

// An Incident is one occurrence of an alert's problem, from its root-cause analysis to its end. An
// Alert opens at most one Incident at a time; firings while it is open count on it. Its own checks
// resolve it, or a human closes it; the Alert returning to OK does neither.
// +kubebuilder:printcolumn:name="Alert",type="string",JSONPath=".spec.alertRef.name"
// +kubebuilder:printcolumn:name="State",type="string",JSONPath=".status.state"
// +kubebuilder:printcolumn:name="Firings",type="integer",JSONPath=".status.firings"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=inc,categories={krateo,observability}
type Incident struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   IncidentSpec   `json:"spec"`
	Status IncidentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// IncidentList contains a list of Incident.
type IncidentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Incident `json:"items"`
}

var (
	_ resource.Managed     = &Incident{}
	_ resource.ManagedList = &IncidentList{}
)

// GetCondition of this Incident.
func (mg *Incident) GetCondition(ct prv1.ConditionType) prv1.Condition {
	return mg.Status.GetCondition(ct)
}

// SetConditions of this Incident.
func (mg *Incident) SetConditions(c ...prv1.Condition) {
	mg.Status.SetConditions(c...)
}

// GetItems of this IncidentList.
func (l *IncidentList) GetItems() []resource.Managed {
	items := make([]resource.Managed, len(l.Items))
	for i := range l.Items {
		items[i] = &l.Items[i]
	}
	return items
}
