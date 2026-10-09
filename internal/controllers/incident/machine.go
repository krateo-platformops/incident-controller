package incident

import (
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	prv1 "github.com/krateo-platformops/provider-runtime/apis/common/v1"

	"github.com/krateo-platformops/incident-controller/apis/incident/v1alpha1"
)

// Config is the check cadence and its limits.
type Config struct {
	// PollInterval is the time from the end of one check to the start of the next.
	PollInterval time.Duration

	// SettleWindow is how long, from entering Verifying, a verify exit 1 is retried instead of
	// moving the incident back to Open.
	SettleWindow time.Duration

	// CheckTimeout bounds one check run. It is the check pod's activeDeadlineSeconds.
	CheckTimeout time.Duration
}

// pendingGrace is how long past CheckTimeout a check pod that has not finished counts as timed
// out. It covers the pods activeDeadlineSeconds cannot stop: those that never start.
const pendingGrace = time.Minute

// result is one finished run of a script.
type result struct {
	script v1alpha1.Script
	// exit is nil when the run produced no exit code: a timeout, or a pod that never ran.
	exit *int32
	at   time.Time
}

// change is a state change worth an event.
type change struct {
	reason  string
	message string
}

// currentScript is the script the incident runs in its current state, or "" when it runs none.
func currentScript(inc *v1alpha1.Incident) v1alpha1.Script {
	h := inc.Status.HowToFix
	if h == nil {
		return ""
	}
	switch inc.Status.State {
	case v1alpha1.StateOpen:
		if h.Precondition != "" && !flagged(inc) {
			return v1alpha1.ScriptPrecondition
		}
	case v1alpha1.StateVerifying:
		if h.Verify != "" {
			return v1alpha1.ScriptVerify
		}
	}
	return ""
}

// scriptBody is the bash source of a script.
func scriptBody(inc *v1alpha1.Incident, s v1alpha1.Script) string {
	h := inc.Status.HowToFix
	if h == nil {
		return ""
	}
	switch s {
	case v1alpha1.ScriptPrecondition:
		return h.Precondition
	case v1alpha1.ScriptVerify:
		return h.Verify
	}
	return ""
}

// flagged reports whether the first precondition run exited 0.
func flagged(inc *v1alpha1.Incident) bool {
	return inc.GetCondition(v1alpha1.TypeReproduced).Status == metav1.ConditionFalse
}

// reproduced reports whether the first precondition run has a verdict, 0 or 1.
func reproduced(inc *v1alpha1.Incident) bool {
	return inc.GetCondition(v1alpha1.TypeReproduced).Status != metav1.ConditionUnknown
}

// slot is the lastChecks entry of s.
func slot(lc *v1alpha1.LastChecks, s v1alpha1.Script) **v1alpha1.LastCheck {
	switch s {
	case v1alpha1.ScriptPrecondition:
		return &lc.Precondition
	case v1alpha1.ScriptApply:
		return &lc.Apply
	}
	return &lc.Verify
}

// setLastCheck records a result of s at at. A precondition or verify result equal to the one
// recorded keeps its since, so a check that keeps giving the same result changes nothing. Every
// apply run replaces the entry.
func setLastCheck(inc *v1alpha1.Incident, s v1alpha1.Script, exit *int32, at time.Time) {
	if inc.Status.LastChecks == nil {
		inc.Status.LastChecks = &v1alpha1.LastChecks{}
	}
	p := slot(inc.Status.LastChecks, s)
	if s != v1alpha1.ScriptApply && *p != nil && ptr.Equal((*p).Exit, exit) {
		return
	}
	*p = &v1alpha1.LastCheck{Exit: exit, Since: metav1.NewTime(at).Rfc3339Copy()}
}

func clearLastCheck(inc *v1alpha1.Incident, s v1alpha1.Script) {
	if inc.Status.LastChecks != nil {
		*slot(inc.Status.LastChecks, s) = nil
	}
}

// checkFrom is when the wait for the next check started: the end of the last run, or the start of
// the settle window when that is later. It is the zero time when neither is known.
func checkFrom(inc *v1alpha1.Incident, lastRun time.Time) time.Time {
	if v := inc.Status.VerifyingSince; v != nil && v.After(lastRun) {
		return v.Time
	}
	return lastRun
}

// nextCheckAt is when the next check may start: a poll interval after checkFrom. It is the zero
// time when no check has run.
func nextCheckAt(inc *v1alpha1.Incident, lastRun time.Time, poll time.Duration) time.Time {
	from := checkFrom(inc, lastRun)
	if from.IsZero() {
		return from
	}
	return from.Add(poll)
}

// checkDue reports whether the current script should start at now.
func checkDue(inc *v1alpha1.Incident, cfg Config, lastRun, now time.Time) bool {
	return currentScript(inc) != "" && !now.Before(nextCheckAt(inc, lastRun, cfg.PollInterval))
}

// enterVerifying starts the settle window at at. The verify result of an earlier window is
// dropped: it describes a fix that no longer applies.
func enterVerifying(inc *v1alpha1.Incident, at time.Time) {
	inc.Status.VerifyingSince = ptr.To(metav1.NewTime(at).Rfc3339Copy())
	clearLastCheck(inc, v1alpha1.ScriptVerify)
}

func setState(inc *v1alpha1.Incident, to v1alpha1.State, why string, at time.Time) change {
	from := inc.Status.State
	inc.Status.State = to
	switch to {
	case v1alpha1.StateVerifying:
		enterVerifying(inc, at)
	case v1alpha1.StateOpen:
		// The precondition result that left Open describes a state that has moved on.
		clearLastCheck(inc, v1alpha1.ScriptPrecondition)
	}
	if to != v1alpha1.StateVerifying {
		inc.Status.VerifyingSince = nil
	}
	if from == "" {
		from = "none"
	}
	return change{reason: "StateChanged", message: fmt.Sprintf("%s -> %s: %s", from, to, why)}
}

// applySpec applies the human decisions in spec to the status. consumed reports that
// spec.applied was acted on and must be set back to false.
func applySpec(inc *v1alpha1.Incident, now time.Time) (changes []change, consumed bool) {
	state := inc.Status.State
	if inc.Spec.Closed {
		if state == v1alpha1.StateClosed {
			return nil, false
		}
		c := setState(inc, v1alpha1.StateClosed, "spec.closed", now)
		inc.Status.Resolution = &v1alpha1.Resolution{By: v1alpha1.ResolvedByUser, At: metav1.NewTime(now).Rfc3339Copy()}
		return []change{c}, false
	}
	if !inc.Spec.Applied {
		return nil, false
	}
	switch state {
	case v1alpha1.StateOpen:
		setLastCheck(inc, v1alpha1.ScriptApply, nil, now)
		return []change{setState(inc, v1alpha1.StateVerifying, "spec.applied", now)}, true
	case v1alpha1.StateVerifying:
		// A human applied again: record it and restart the settle window.
		setLastCheck(inc, v1alpha1.ScriptApply, nil, now)
		enterVerifying(inc, now)
		return nil, true
	}
	// Analyzing has no fix to apply yet; Resolved and Closed are over.
	return nil, false
}

// record sets a finished run of the current script as its latest result and applies its
// transition. The exit codes: 0 the incident is gone, 1 it holds, anything else or none is
// unknown and moves nothing. Recording the same result twice changes nothing.
func record(inc *v1alpha1.Incident, cfg Config, r result, now time.Time) []change {
	setLastCheck(inc, r.script, r.exit, r.at)
	if r.exit == nil || (*r.exit != 0 && *r.exit != 1) {
		return nil
	}
	holds := *r.exit == 1

	switch r.script {
	case v1alpha1.ScriptPrecondition:
		if !reproduced(inc) {
			if holds {
				inc.SetConditions(reproducedCondition(metav1.ConditionTrue, v1alpha1.ReasonPreconditionHolds,
					"the first precondition run exited 1", now))
				return nil
			}
			inc.SetConditions(reproducedCondition(metav1.ConditionFalse, v1alpha1.ReasonPreconditionPassed,
				"the first precondition run exited 0: it cannot see the incident, so it no longer runs", now))
			return []change{{reason: "Flagged", message: "the first precondition run exited 0; only spec.applied or spec.closed moves this incident"}}
		}
		if !holds {
			return []change{setState(inc, v1alpha1.StateVerifying, "precondition exited 0", r.at)}
		}
	case v1alpha1.ScriptVerify:
		if !holds {
			inc.Status.Resolution = &v1alpha1.Resolution{By: v1alpha1.ResolvedByVerify, At: metav1.NewTime(r.at).Rfc3339Copy()}
			return []change{setState(inc, v1alpha1.StateResolved, "verify exited 0", r.at)}
		}
		since := inc.Status.VerifyingSince
		if since == nil {
			// A Verifying incident without a window starts it at this run.
			inc.Status.VerifyingSince = ptr.To(metav1.NewTime(r.at).Rfc3339Copy())
			return nil
		}
		if r.at.Sub(since.Time) >= cfg.SettleWindow {
			return []change{setState(inc, v1alpha1.StateOpen, "verify exited 1 after the settle window", r.at)}
		}
	}
	return nil
}

// recordApply records a finished IncidentApply run on its incident as the latest apply result.
// Exit 0 moves an Open incident to Verifying, as spec.applied does, and restarts the settle window
// of a Verifying one; any other result moves nothing. Only Open and Verifying incidents record a
// run; the others are left alone. changed reports whether inc changed: recording the same run
// twice does not.
func recordApply(inc *v1alpha1.Incident, r result, by string) (changes []change, changed bool) {
	state := inc.Status.State
	if state != v1alpha1.StateOpen && state != v1alpha1.StateVerifying {
		return nil, false
	}
	before := inc.Status.DeepCopy()
	setLastCheck(inc, v1alpha1.ScriptApply, r.exit, r.at)
	if r.exit != nil && *r.exit == 0 {
		if state == v1alpha1.StateOpen {
			changes = append(changes, setState(inc, v1alpha1.StateVerifying, "apply exited 0, run by "+by, r.at))
		} else if v := inc.Status.VerifyingSince; v == nil || v.Before(ptr.To(metav1.NewTime(r.at).Rfc3339Copy())) {
			enterVerifying(inc, r.at)
		}
	}
	return changes, !equality.Semantic.DeepEqual(before, &inc.Status)
}

func reproducedCondition(s metav1.ConditionStatus, reason prv1.ConditionReason, msg string, now time.Time) prv1.Condition {
	return prv1.Condition{
		Type:               v1alpha1.TypeReproduced,
		Status:             s,
		Reason:             reason,
		Message:            msg,
		LastTransitionTime: metav1.NewTime(now).Rfc3339Copy(),
	}
}
