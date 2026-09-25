package incident

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	prv1 "github.com/krateo-platformops/provider-runtime/apis/common/v1"
	"github.com/krateo-platformops/provider-runtime/pkg/test"

	"github.com/krateo-platformops/incident-controller/apis/incident/v1alpha1"
)

var (
	t0  = time.Date(2026, 9, 25, 14, 0, 0, 0, time.UTC)
	cfg = Config{PollInterval: time.Minute, SettleWindow: 5 * time.Minute, CheckTimeout: time.Minute}
)

func at(d time.Duration) metav1.Time { return metav1.NewTime(t0.Add(d)) }

func exit(code int32) *int32 { return ptr.To(code) }

type incOpt func(*v1alpha1.Incident)

func newIncident(state v1alpha1.State, opts ...incOpt) *v1alpha1.Incident {
	inc := &v1alpha1.Incident{
		ObjectMeta: metav1.ObjectMeta{Name: "cd-not-ready-20260925-140000", Namespace: "krateo-system", UID: "0b7c3a3e-1d2f-4c56-9a8b-7e6f5d4c3b2a"},
		Spec:       v1alpha1.IncidentSpec{AlertRef: v1alpha1.AlertRef{Name: "cd-not-ready", Namespace: "krateo-system"}},
		Status: v1alpha1.IncidentStatus{
			State:    state,
			HowToFix: &v1alpha1.HowToFix{Precondition: "exit 1", Apply: "true", Verify: "exit 0"},
		},
	}
	for _, o := range opts {
		o(inc)
	}
	return inc
}

func withChecks(c ...v1alpha1.Check) incOpt {
	return func(inc *v1alpha1.Incident) { inc.Status.Checks = c }
}

func withReproduced(s metav1.ConditionStatus) incOpt {
	return func(inc *v1alpha1.Incident) {
		inc.SetConditions(prv1.Condition{Type: v1alpha1.TypeReproduced, Status: s, Reason: "test", LastTransitionTime: at(0)})
	}
}

func withSpec(applied, closed bool) incOpt {
	return func(inc *v1alpha1.Incident) { inc.Spec.Applied, inc.Spec.Closed = applied, closed }
}

func withoutHowToFix(inc *v1alpha1.Incident) { inc.Status.HowToFix = nil }

func check(s v1alpha1.Script, code *int32, d time.Duration) v1alpha1.Check {
	return v1alpha1.Check{Script: s, Exit: code, At: at(d)}
}

// want is the part of the status a transition decides.
type want struct {
	state      v1alpha1.State
	checks     []v1alpha1.Check
	resolution *v1alpha1.Resolution
	reproduced metav1.ConditionStatus
}

func got(inc *v1alpha1.Incident) want {
	return want{
		state:      inc.Status.State,
		checks:     inc.Status.Checks,
		resolution: inc.Status.Resolution,
		reproduced: inc.GetCondition(v1alpha1.TypeReproduced).Status,
	}
}

func diff(t *testing.T, w, g want) {
	t.Helper()
	if d := cmp.Diff(w, g, cmp.AllowUnexported(want{}), test.EquateConditions()); d != "" {
		t.Errorf("status (-want +got):\n%s", d)
	}
}

func TestApplySpec(t *testing.T) {
	now := t0.Add(10 * time.Minute)
	closedBy := &v1alpha1.Resolution{By: v1alpha1.ResolvedByUser, At: metav1.NewTime(now)}
	pre1 := check(v1alpha1.ScriptPrecondition, exit(1), 0)
	ver1 := check(v1alpha1.ScriptVerify, exit(1), 4*time.Minute)
	apply := check(v1alpha1.ScriptApply, nil, 3*time.Minute)
	applyNow := v1alpha1.Check{Script: v1alpha1.ScriptApply, At: metav1.NewTime(now)}

	cases := []struct {
		name         string
		inc          *v1alpha1.Incident
		want         want
		wantConsumed bool
	}{
		{name: "closed before the first status write", inc: newIncident("", withSpec(false, true)),
			want: want{state: v1alpha1.StateClosed, resolution: closedBy, reproduced: metav1.ConditionUnknown}},
		{name: "closed while Analyzing", inc: newIncident(v1alpha1.StateAnalyzing, withSpec(false, true)),
			want: want{state: v1alpha1.StateClosed, resolution: closedBy, reproduced: metav1.ConditionUnknown}},
		{name: "closed while Open", inc: newIncident(v1alpha1.StateOpen, withSpec(false, true), withChecks(pre1)),
			want: want{state: v1alpha1.StateClosed, checks: []v1alpha1.Check{pre1}, resolution: closedBy, reproduced: metav1.ConditionUnknown}},
		{name: "closed while Verifying", inc: newIncident(v1alpha1.StateVerifying, withSpec(false, true)),
			want: want{state: v1alpha1.StateClosed, resolution: closedBy, reproduced: metav1.ConditionUnknown}},
		{name: "closed after Resolved", inc: newIncident(v1alpha1.StateResolved, withSpec(false, true), func(i *v1alpha1.Incident) {
			i.Status.Resolution = &v1alpha1.Resolution{By: v1alpha1.ResolvedByVerify, At: at(0)}
		}), want: want{state: v1alpha1.StateClosed, resolution: closedBy, reproduced: metav1.ConditionUnknown}},
		{name: "closed wins over applied", inc: newIncident(v1alpha1.StateOpen, withSpec(true, true)),
			want: want{state: v1alpha1.StateClosed, resolution: closedBy, reproduced: metav1.ConditionUnknown}},
		{name: "applied moves Open to Verifying and records an apply check", inc: newIncident(v1alpha1.StateOpen, withSpec(true, false), withChecks(pre1)),
			want: want{state: v1alpha1.StateVerifying, checks: []v1alpha1.Check{pre1, applyNow}, reproduced: metav1.ConditionUnknown}, wantConsumed: true},
		{name: "applied moves a flagged incident to Verifying", inc: newIncident(v1alpha1.StateOpen, withSpec(true, false), withReproduced(metav1.ConditionFalse)),
			want: want{state: v1alpha1.StateVerifying, checks: []v1alpha1.Check{applyNow}, reproduced: metav1.ConditionFalse}, wantConsumed: true},
		{name: "applied again while Verifying restarts the settle window", inc: newIncident(v1alpha1.StateVerifying, withSpec(true, false), withChecks(apply, ver1)),
			want: want{state: v1alpha1.StateVerifying, checks: []v1alpha1.Check{apply, ver1, applyNow}, reproduced: metav1.ConditionUnknown}, wantConsumed: true},
		{name: "an apply left unconsumed is not recorded twice", inc: newIncident(v1alpha1.StateVerifying, withSpec(true, false), withChecks(pre1, apply)),
			want: want{state: v1alpha1.StateVerifying, checks: []v1alpha1.Check{pre1, apply}, reproduced: metav1.ConditionUnknown}, wantConsumed: true},
		{name: "applied waits while Analyzing", inc: newIncident(v1alpha1.StateAnalyzing, withSpec(true, false)),
			want: want{state: v1alpha1.StateAnalyzing, reproduced: metav1.ConditionUnknown}},
		{name: "applied is ignored once Resolved", inc: newIncident(v1alpha1.StateResolved, withSpec(true, false), func(i *v1alpha1.Incident) {
			i.Status.Resolution = &v1alpha1.Resolution{By: v1alpha1.ResolvedByVerify, At: at(0)}
		}), want: want{state: v1alpha1.StateResolved, resolution: &v1alpha1.Resolution{By: v1alpha1.ResolvedByVerify, At: at(0)}, reproduced: metav1.ConditionUnknown}},
		{name: "nothing to apply", inc: newIncident(v1alpha1.StateOpen),
			want: want{state: v1alpha1.StateOpen, reproduced: metav1.ConditionUnknown}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, consumed := applySpec(tc.inc, now)
			if consumed != tc.wantConsumed {
				t.Errorf("consumed = %v, want %v", consumed, tc.wantConsumed)
			}
			diff(t, tc.want, got(tc.inc))
		})
	}
}

func TestRecordPrecondition(t *testing.T) {
	now := t0.Add(time.Minute)
	run := func(code *int32) result {
		return result{script: v1alpha1.ScriptPrecondition, exit: code, at: t0.Add(time.Minute)}
	}
	ran := func(code *int32) v1alpha1.Check { return check(v1alpha1.ScriptPrecondition, code, time.Minute) }

	cases := []struct {
		name string
		inc  *v1alpha1.Incident
		r    result
		want want
	}{
		{name: "the first run exits 1: reproduced", inc: newIncident(v1alpha1.StateOpen), r: run(exit(1)),
			want: want{state: v1alpha1.StateOpen, checks: []v1alpha1.Check{ran(exit(1))}, reproduced: metav1.ConditionTrue}},
		{name: "the first run exits 0: flagged, not Verifying", inc: newIncident(v1alpha1.StateOpen), r: run(exit(0)),
			want: want{state: v1alpha1.StateOpen, checks: []v1alpha1.Check{ran(exit(0))}, reproduced: metav1.ConditionFalse}},
		{name: "a first run with another exit is not the first verdict", inc: newIncident(v1alpha1.StateOpen), r: run(exit(2)),
			want: want{state: v1alpha1.StateOpen, checks: []v1alpha1.Check{ran(exit(2))}, reproduced: metav1.ConditionUnknown}},
		{name: "a first run that timed out is not the first verdict", inc: newIncident(v1alpha1.StateOpen), r: run(nil),
			want: want{state: v1alpha1.StateOpen, checks: []v1alpha1.Check{ran(nil)}, reproduced: metav1.ConditionUnknown}},
		{name: "exit 0 once reproduced: Verifying", inc: newIncident(v1alpha1.StateOpen, withReproduced(metav1.ConditionTrue)), r: run(exit(0)),
			want: want{state: v1alpha1.StateVerifying, checks: []v1alpha1.Check{ran(exit(0))}, reproduced: metav1.ConditionTrue}},
		{name: "exit 1 once reproduced: stays Open", inc: newIncident(v1alpha1.StateOpen, withReproduced(metav1.ConditionTrue)), r: run(exit(1)),
			want: want{state: v1alpha1.StateOpen, checks: []v1alpha1.Check{ran(exit(1))}, reproduced: metav1.ConditionTrue}},
		{name: "another exit once reproduced: unknown, stays Open", inc: newIncident(v1alpha1.StateOpen, withReproduced(metav1.ConditionTrue)), r: run(exit(127)),
			want: want{state: v1alpha1.StateOpen, checks: []v1alpha1.Check{ran(exit(127))}, reproduced: metav1.ConditionTrue}},
		{name: "a timeout once reproduced: unknown, stays Open", inc: newIncident(v1alpha1.StateOpen, withReproduced(metav1.ConditionTrue)), r: run(nil),
			want: want{state: v1alpha1.StateOpen, checks: []v1alpha1.Check{ran(nil)}, reproduced: metav1.ConditionTrue}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record(tc.inc, cfg, tc.r, now)
			diff(t, tc.want, got(tc.inc))
		})
	}
}

func TestRecordVerify(t *testing.T) {
	// The incident entered Verifying at t0 with a precondition exit 0.
	entered := check(v1alpha1.ScriptPrecondition, exit(0), 0)
	run := func(code *int32, d time.Duration) result {
		return result{script: v1alpha1.ScriptVerify, exit: code, at: t0.Add(d)}
	}
	ran := func(code *int32, d time.Duration) v1alpha1.Check { return check(v1alpha1.ScriptVerify, code, d) }
	verifying := func(c ...v1alpha1.Check) *v1alpha1.Incident {
		return newIncident(v1alpha1.StateVerifying, withReproduced(metav1.ConditionTrue), withChecks(c...))
	}

	cases := []struct {
		name string
		inc  *v1alpha1.Incident
		r    result
		want want
	}{
		{name: "exit 0: Resolved by verify", inc: verifying(entered), r: run(exit(0), time.Minute),
			want: want{state: v1alpha1.StateResolved, checks: []v1alpha1.Check{entered, ran(exit(0), time.Minute)},
				resolution: &v1alpha1.Resolution{By: v1alpha1.ResolvedByVerify, At: at(time.Minute)}, reproduced: metav1.ConditionTrue}},
		{name: "exit 1 inside the settle window: retried", inc: verifying(entered), r: run(exit(1), time.Minute),
			want: want{state: v1alpha1.StateVerifying, checks: []v1alpha1.Check{entered, ran(exit(1), time.Minute)}, reproduced: metav1.ConditionTrue}},
		{name: "exit 1 just before the window ends: retried", inc: verifying(entered), r: run(exit(1), 5*time.Minute-time.Second),
			want: want{state: v1alpha1.StateVerifying, checks: []v1alpha1.Check{entered, ran(exit(1), 5*time.Minute-time.Second)}, reproduced: metav1.ConditionTrue}},
		{name: "exit 1 when the window ends: back to Open", inc: verifying(entered), r: run(exit(1), 5*time.Minute),
			want: want{state: v1alpha1.StateOpen, checks: []v1alpha1.Check{entered, ran(exit(1), 5*time.Minute)}, reproduced: metav1.ConditionTrue}},
		{name: "exit 0 after the window: Resolved", inc: verifying(entered), r: run(exit(0), 9*time.Minute),
			want: want{state: v1alpha1.StateResolved, checks: []v1alpha1.Check{entered, ran(exit(0), 9*time.Minute)},
				resolution: &v1alpha1.Resolution{By: v1alpha1.ResolvedByVerify, At: at(9 * time.Minute)}, reproduced: metav1.ConditionTrue}},
		{name: "another exit after the window: unknown, stays Verifying", inc: verifying(entered), r: run(exit(2), 9*time.Minute),
			want: want{state: v1alpha1.StateVerifying, checks: []v1alpha1.Check{entered, ran(exit(2), 9*time.Minute)}, reproduced: metav1.ConditionTrue}},
		{name: "a timeout after the window: unknown, stays Verifying", inc: verifying(entered), r: run(nil, 9*time.Minute),
			want: want{state: v1alpha1.StateVerifying, checks: []v1alpha1.Check{entered, ran(nil, 9*time.Minute)}, reproduced: metav1.ConditionTrue}},
		{name: "the window starts at the apply check", inc: verifying(check(v1alpha1.ScriptApply, nil, 4*time.Minute)), r: run(exit(1), 6*time.Minute),
			want: want{state: v1alpha1.StateVerifying, checks: []v1alpha1.Check{check(v1alpha1.ScriptApply, nil, 4*time.Minute), ran(exit(1), 6*time.Minute)}, reproduced: metav1.ConditionTrue}},
		{name: "with only verify runs left, the window starts at the oldest", inc: verifying(ran(exit(1), time.Minute)), r: run(exit(1), 6*time.Minute),
			want: want{state: v1alpha1.StateOpen, checks: []v1alpha1.Check{ran(exit(1), time.Minute), ran(exit(1), 6*time.Minute)}, reproduced: metav1.ConditionTrue}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record(tc.inc, cfg, tc.r, tc.r.at)
			diff(t, tc.want, got(tc.inc))
		})
	}
}

func TestCurrentScript(t *testing.T) {
	cases := []struct {
		name string
		inc  *v1alpha1.Incident
		want v1alpha1.Script
	}{
		{name: "no state yet", inc: newIncident(""), want: ""},
		{name: "Analyzing", inc: newIncident(v1alpha1.StateAnalyzing), want: ""},
		{name: "Open", inc: newIncident(v1alpha1.StateOpen), want: v1alpha1.ScriptPrecondition},
		{name: "Open and reproduced", inc: newIncident(v1alpha1.StateOpen, withReproduced(metav1.ConditionTrue)), want: v1alpha1.ScriptPrecondition},
		{name: "Open and flagged", inc: newIncident(v1alpha1.StateOpen, withReproduced(metav1.ConditionFalse)), want: ""},
		{name: "Open without scripts", inc: newIncident(v1alpha1.StateOpen, withoutHowToFix), want: ""},
		{name: "Open without a precondition", inc: newIncident(v1alpha1.StateOpen, func(i *v1alpha1.Incident) { i.Status.HowToFix.Precondition = "" }), want: ""},
		{name: "Verifying", inc: newIncident(v1alpha1.StateVerifying), want: v1alpha1.ScriptVerify},
		{name: "Verifying and flagged", inc: newIncident(v1alpha1.StateVerifying, withReproduced(metav1.ConditionFalse)), want: v1alpha1.ScriptVerify},
		{name: "Verifying without a verify script", inc: newIncident(v1alpha1.StateVerifying, func(i *v1alpha1.Incident) { i.Status.HowToFix.Verify = "" }), want: ""},
		{name: "Resolved", inc: newIncident(v1alpha1.StateResolved), want: ""},
		{name: "Closed", inc: newIncident(v1alpha1.StateClosed), want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if g := currentScript(tc.inc); g != tc.want {
				t.Errorf("currentScript = %q, want %q", g, tc.want)
			}
		})
	}
}

func TestCheckDue(t *testing.T) {
	last := withChecks(check(v1alpha1.ScriptPrecondition, exit(1), 0))
	cases := []struct {
		name string
		inc  *v1alpha1.Incident
		now  time.Time
		want bool
	}{
		{name: "no check has run", inc: newIncident(v1alpha1.StateOpen), now: t0, want: true},
		{name: "inside the poll interval", inc: newIncident(v1alpha1.StateOpen, last), now: t0.Add(59 * time.Second), want: false},
		{name: "a poll interval after the last check", inc: newIncident(v1alpha1.StateOpen, last), now: t0.Add(time.Minute), want: true},
		{name: "no script to run", inc: newIncident(v1alpha1.StateAnalyzing), now: t0.Add(time.Hour), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if g := checkDue(tc.inc, cfg, tc.now); g != tc.want {
				t.Errorf("checkDue = %v, want %v", g, tc.want)
			}
		})
	}
}

func TestChecksKeepTheNewest(t *testing.T) {
	inc := newIncident(v1alpha1.StateOpen, withReproduced(metav1.ConditionTrue))
	for i := range 25 {
		record(inc, cfg, result{script: v1alpha1.ScriptPrecondition, exit: exit(1), at: t0.Add(time.Duration(i) * time.Minute)}, t0)
	}
	if n := len(inc.Status.Checks); n != v1alpha1.MaxChecks {
		t.Fatalf("len(checks) = %d, want %d", n, v1alpha1.MaxChecks)
	}
	if first := inc.Status.Checks[0].At; !first.Equal(ptr.To(at(5 * time.Minute))) {
		t.Errorf("oldest kept check at %v, want %v", first, at(5*time.Minute))
	}
}

func TestRecorded(t *testing.T) {
	inc := newIncident(v1alpha1.StateOpen, withChecks(check(v1alpha1.ScriptPrecondition, exit(1), time.Minute)))
	sub := t0.Add(time.Minute + 300*time.Millisecond)
	if !recorded(inc, result{script: v1alpha1.ScriptPrecondition, at: sub}) {
		t.Error("a result at the same second is recorded")
	}
	if recorded(inc, result{script: v1alpha1.ScriptVerify, at: sub}) {
		t.Error("a result of another script is not recorded")
	}
	if recorded(inc, result{script: v1alpha1.ScriptPrecondition, at: t0.Add(2 * time.Minute)}) {
		t.Error("a result at another time is not recorded")
	}
}

// TestLifecycle walks one incident through every script-driven edge.
func TestLifecycle(t *testing.T) {
	inc := newIncident(v1alpha1.StateOpen)
	step := func(r result, wantState v1alpha1.State) {
		t.Helper()
		record(inc, cfg, r, r.at)
		if inc.Status.State != wantState {
			t.Fatalf("after %s exit %v at %v: state %s, want %s", r.script, r.exit, r.at.Sub(t0), inc.Status.State, wantState)
		}
	}
	pre := func(code int32, d time.Duration) result {
		return result{script: v1alpha1.ScriptPrecondition, exit: exit(code), at: t0.Add(d)}
	}
	ver := func(code int32, d time.Duration) result {
		return result{script: v1alpha1.ScriptVerify, exit: exit(code), at: t0.Add(d)}
	}

	step(pre(1, 0), v1alpha1.StateOpen) // first run: reproduced
	step(pre(1, time.Minute), v1alpha1.StateOpen)
	step(pre(0, 2*time.Minute), v1alpha1.StateVerifying)
	step(ver(1, 3*time.Minute), v1alpha1.StateVerifying) // inside the settle window
	step(ver(1, 7*time.Minute), v1alpha1.StateOpen)      // window over: the fix did not hold

	inc.Spec.Applied = true
	if _, consumed := applySpec(inc, t0.Add(8*time.Minute)); !consumed || inc.Status.State != v1alpha1.StateVerifying {
		t.Fatalf("spec.applied: consumed %v, state %s", consumed, inc.Status.State)
	}
	step(ver(0, 9*time.Minute), v1alpha1.StateResolved)
	if r := inc.Status.Resolution; r == nil || r.By != v1alpha1.ResolvedByVerify {
		t.Fatalf("resolution = %+v, want by verify", r)
	}
}
