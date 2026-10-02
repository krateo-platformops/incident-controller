package incident

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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

func lastCheck(inc *v1alpha1.Incident) *v1alpha1.Check {
	if n := len(inc.Status.Checks); n > 0 {
		return &inc.Status.Checks[n-1]
	}
	return nil
}

// nextCheckAt is when the next check may start: a poll interval after the last one. It is the
// zero time when no check has run yet.
func nextCheckAt(inc *v1alpha1.Incident, poll time.Duration) time.Time {
	if c := lastCheck(inc); c != nil {
		return c.At.Add(poll)
	}
	return time.Time{}
}

// checkDue reports whether the current script should start at now.
func checkDue(inc *v1alpha1.Incident, cfg Config, now time.Time) bool {
	return currentScript(inc) != "" && !now.Before(nextCheckAt(inc, cfg.PollInterval))
}

// settleWindowStart is when the incident entered Verifying: the time of the newest check that is
// not a verify run, the precondition exit 0 or the apply check that moved it. When the history
// holds only verify runs, their oldest is used, which ends the window no later than the real one.
func settleWindowStart(inc *v1alpha1.Incident) time.Time {
	checks := inc.Status.Checks
	for i := len(checks) - 1; i >= 0; i-- {
		if checks[i].Script != v1alpha1.ScriptVerify {
			return checks[i].At.Time
		}
	}
	if len(checks) > 0 {
		return checks[0].At.Time
	}
	return time.Time{}
}

// recorded reports whether r is already in the check history. Results carry deterministic
// times, so a result recorded by an earlier reconcile whose pod was not yet deleted is not
// recorded twice.
func recorded(inc *v1alpha1.Incident, r result) bool {
	at := metav1.NewTime(r.at).Rfc3339Copy()
	for _, c := range inc.Status.Checks {
		if c.Script == r.script && c.At.Equal(&at) {
			return true
		}
	}
	return false
}

func appendCheck(inc *v1alpha1.Incident, c v1alpha1.Check) {
	c.At = c.At.Rfc3339Copy()
	checks := append(inc.Status.Checks, c)
	if n := len(checks); n > v1alpha1.MaxChecks {
		checks = append([]v1alpha1.Check(nil), checks[n-v1alpha1.MaxChecks:]...)
	}
	inc.Status.Checks = checks
}

func setState(inc *v1alpha1.Incident, to v1alpha1.State, why string) change {
	from := inc.Status.State
	inc.Status.State = to
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
		c := setState(inc, v1alpha1.StateClosed, "spec.closed")
		inc.Status.Resolution = &v1alpha1.Resolution{By: v1alpha1.ResolvedByUser, At: metav1.NewTime(now).Rfc3339Copy()}
		return []change{c}, false
	}
	if !inc.Spec.Applied {
		return nil, false
	}
	switch state {
	case v1alpha1.StateOpen:
		appendCheck(inc, v1alpha1.Check{Script: v1alpha1.ScriptApply, At: metav1.NewTime(now)})
		return []change{setState(inc, v1alpha1.StateVerifying, "spec.applied")}, true
	case v1alpha1.StateVerifying:
		// A human applied again: record it, which restarts the settle window. An apply check with
		// no verify run after it is this same apply, left unconsumed by a failed spec update.
		if c := lastCheck(inc); c != nil && c.Script == v1alpha1.ScriptApply {
			return nil, true
		}
		appendCheck(inc, v1alpha1.Check{Script: v1alpha1.ScriptApply, At: metav1.NewTime(now)})
		return nil, true
	}
	// Analyzing has no fix to apply yet; Resolved and Closed are over.
	return nil, false
}

// record appends a finished run of the current script and applies its transition. The exit
// codes: 0 the incident is gone, 1 it holds, anything else or none is unknown and moves nothing.
func record(inc *v1alpha1.Incident, cfg Config, r result, now time.Time) []change {
	appendCheck(inc, v1alpha1.Check{Script: r.script, Exit: r.exit, At: metav1.NewTime(r.at)})
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
			return []change{setState(inc, v1alpha1.StateVerifying, "precondition exited 0")}
		}
	case v1alpha1.ScriptVerify:
		if !holds {
			inc.Status.Resolution = &v1alpha1.Resolution{By: v1alpha1.ResolvedByVerify, At: metav1.NewTime(r.at).Rfc3339Copy()}
			return []change{setState(inc, v1alpha1.StateResolved, "verify exited 0")}
		}
		if r.at.Sub(settleWindowStart(inc)) >= cfg.SettleWindow {
			return []change{setState(inc, v1alpha1.StateOpen, "verify exited 1 after the settle window")}
		}
	}
	return nil
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
