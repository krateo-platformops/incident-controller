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

func withLastChecks(lc v1alpha1.LastChecks) incOpt {
	return func(inc *v1alpha1.Incident) { inc.Status.LastChecks = &lc }
}

func withVerifyingSince(d time.Duration) incOpt {
	return func(inc *v1alpha1.Incident) { inc.Status.VerifyingSince = ptr.To(at(d)) }
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

// lc is a result with exit code, given since d after t0.
func lc(code *int32, d time.Duration) *v1alpha1.LastCheck {
	return &v1alpha1.LastCheck{Exit: code, Since: at(d)}
}

// want is the part of the status a transition decides.
type want struct {
	state          v1alpha1.State
	lastChecks     *v1alpha1.LastChecks
	verifyingSince *metav1.Time
	resolution     *v1alpha1.Resolution
	reproduced     metav1.ConditionStatus
}

func got(inc *v1alpha1.Incident) want {
	return want{
		state:          inc.Status.State,
		lastChecks:     inc.Status.LastChecks,
		verifyingSince: inc.Status.VerifyingSince,
		resolution:     inc.Status.Resolution,
		reproduced:     inc.GetCondition(v1alpha1.TypeReproduced).Status,
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
	nowT := ptr.To(metav1.NewTime(now))
	closedBy := &v1alpha1.Resolution{By: v1alpha1.ResolvedByUser, At: metav1.NewTime(now)}
	pre1 := lc(exit(1), 0)
	applyNow := lc(nil, 10*time.Minute)

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
		{name: "closed while Open", inc: newIncident(v1alpha1.StateOpen, withSpec(false, true), withLastChecks(v1alpha1.LastChecks{Precondition: pre1})),
			want: want{state: v1alpha1.StateClosed, lastChecks: &v1alpha1.LastChecks{Precondition: pre1}, resolution: closedBy, reproduced: metav1.ConditionUnknown}},
		{name: "closed while Verifying ends the settle window", inc: newIncident(v1alpha1.StateVerifying, withSpec(false, true), withVerifyingSince(0)),
			want: want{state: v1alpha1.StateClosed, resolution: closedBy, reproduced: metav1.ConditionUnknown}},
		{name: "closed after Resolved", inc: newIncident(v1alpha1.StateResolved, withSpec(false, true), func(i *v1alpha1.Incident) {
			i.Status.Resolution = &v1alpha1.Resolution{By: v1alpha1.ResolvedByVerify, At: at(0)}
		}), want: want{state: v1alpha1.StateClosed, resolution: closedBy, reproduced: metav1.ConditionUnknown}},
		{name: "closed wins over applied", inc: newIncident(v1alpha1.StateOpen, withSpec(true, true)),
			want: want{state: v1alpha1.StateClosed, resolution: closedBy, reproduced: metav1.ConditionUnknown}},
		{name: "applied moves Open to Verifying and records an apply result", inc: newIncident(v1alpha1.StateOpen, withSpec(true, false), withLastChecks(v1alpha1.LastChecks{Precondition: pre1})),
			want: want{state: v1alpha1.StateVerifying, lastChecks: &v1alpha1.LastChecks{Precondition: pre1, Apply: applyNow},
				verifyingSince: nowT, reproduced: metav1.ConditionUnknown}, wantConsumed: true},
		{name: "applied moves a flagged incident to Verifying", inc: newIncident(v1alpha1.StateOpen, withSpec(true, false), withReproduced(metav1.ConditionFalse)),
			want: want{state: v1alpha1.StateVerifying, lastChecks: &v1alpha1.LastChecks{Apply: applyNow},
				verifyingSince: nowT, reproduced: metav1.ConditionFalse}, wantConsumed: true},
		{name: "applied again while Verifying restarts the settle window", inc: newIncident(v1alpha1.StateVerifying, withSpec(true, false), withVerifyingSince(3*time.Minute),
			withLastChecks(v1alpha1.LastChecks{Apply: lc(nil, 3*time.Minute), Verify: lc(exit(1), 4*time.Minute)})),
			want: want{state: v1alpha1.StateVerifying, lastChecks: &v1alpha1.LastChecks{Apply: applyNow},
				verifyingSince: nowT, reproduced: metav1.ConditionUnknown}, wantConsumed: true},
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
	ran := func(code *int32) *v1alpha1.LastChecks {
		return &v1alpha1.LastChecks{Precondition: lc(code, time.Minute)}
	}
	reproducedOpen := func(opts ...incOpt) *v1alpha1.Incident {
		return newIncident(v1alpha1.StateOpen, append([]incOpt{withReproduced(metav1.ConditionTrue)}, opts...)...)
	}

	cases := []struct {
		name string
		inc  *v1alpha1.Incident
		r    result
		want want
	}{
		{name: "the first run exits 1: reproduced", inc: newIncident(v1alpha1.StateOpen), r: run(exit(1)),
			want: want{state: v1alpha1.StateOpen, lastChecks: ran(exit(1)), reproduced: metav1.ConditionTrue}},
		{name: "the first run exits 0: flagged, not Verifying", inc: newIncident(v1alpha1.StateOpen), r: run(exit(0)),
			want: want{state: v1alpha1.StateOpen, lastChecks: ran(exit(0)), reproduced: metav1.ConditionFalse}},
		{name: "a first run with another exit is not the first verdict", inc: newIncident(v1alpha1.StateOpen), r: run(exit(2)),
			want: want{state: v1alpha1.StateOpen, lastChecks: ran(exit(2)), reproduced: metav1.ConditionUnknown}},
		{name: "a first run that timed out is not the first verdict", inc: newIncident(v1alpha1.StateOpen), r: run(nil),
			want: want{state: v1alpha1.StateOpen, lastChecks: ran(nil), reproduced: metav1.ConditionUnknown}},
		{name: "exit 0 once reproduced: Verifying", inc: reproducedOpen(), r: run(exit(0)),
			want: want{state: v1alpha1.StateVerifying, lastChecks: ran(exit(0)), verifyingSince: ptr.To(at(time.Minute)), reproduced: metav1.ConditionTrue}},
		{name: "exit 1 once reproduced: stays Open", inc: reproducedOpen(), r: run(exit(1)),
			want: want{state: v1alpha1.StateOpen, lastChecks: ran(exit(1)), reproduced: metav1.ConditionTrue}},
		{name: "another exit once reproduced: unknown, stays Open", inc: reproducedOpen(), r: run(exit(127)),
			want: want{state: v1alpha1.StateOpen, lastChecks: ran(exit(127)), reproduced: metav1.ConditionTrue}},
		{name: "a timeout once reproduced: unknown, stays Open", inc: reproducedOpen(), r: run(nil),
			want: want{state: v1alpha1.StateOpen, lastChecks: ran(nil), reproduced: metav1.ConditionTrue}},
		{name: "the same result again keeps its since", inc: reproducedOpen(withLastChecks(v1alpha1.LastChecks{Precondition: lc(exit(1), 0)})), r: run(exit(1)),
			want: want{state: v1alpha1.StateOpen, lastChecks: &v1alpha1.LastChecks{Precondition: lc(exit(1), 0)}, reproduced: metav1.ConditionTrue}},
		{name: "a timeout again keeps its since", inc: reproducedOpen(withLastChecks(v1alpha1.LastChecks{Precondition: lc(nil, 0)})), r: run(nil),
			want: want{state: v1alpha1.StateOpen, lastChecks: &v1alpha1.LastChecks{Precondition: lc(nil, 0)}, reproduced: metav1.ConditionTrue}},
		{name: "another result replaces it", inc: reproducedOpen(withLastChecks(v1alpha1.LastChecks{Precondition: lc(exit(1), 0)})), r: run(exit(2)),
			want: want{state: v1alpha1.StateOpen, lastChecks: ran(exit(2)), reproduced: metav1.ConditionTrue}},
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
	entered := lc(exit(0), 0)
	run := func(code *int32, d time.Duration) result {
		return result{script: v1alpha1.ScriptVerify, exit: code, at: t0.Add(d)}
	}
	verifying := func(opts ...incOpt) *v1alpha1.Incident {
		return newIncident(v1alpha1.StateVerifying, append([]incOpt{withReproduced(metav1.ConditionTrue), withVerifyingSince(0),
			withLastChecks(v1alpha1.LastChecks{Precondition: entered})}, opts...)...)
	}
	ran := func(code *int32, d time.Duration) *v1alpha1.LastChecks {
		return &v1alpha1.LastChecks{Precondition: entered, Verify: lc(code, d)}
	}
	resolvedAt := func(d time.Duration) *v1alpha1.Resolution {
		return &v1alpha1.Resolution{By: v1alpha1.ResolvedByVerify, At: at(d)}
	}
	window := ptr.To(at(0))

	cases := []struct {
		name string
		inc  *v1alpha1.Incident
		r    result
		want want
	}{
		{name: "exit 0: Resolved by verify", inc: verifying(), r: run(exit(0), time.Minute),
			want: want{state: v1alpha1.StateResolved, lastChecks: ran(exit(0), time.Minute), resolution: resolvedAt(time.Minute), reproduced: metav1.ConditionTrue}},
		{name: "exit 1 inside the settle window: retried", inc: verifying(), r: run(exit(1), time.Minute),
			want: want{state: v1alpha1.StateVerifying, lastChecks: ran(exit(1), time.Minute), verifyingSince: window, reproduced: metav1.ConditionTrue}},
		{name: "exit 1 just before the window ends: retried", inc: verifying(), r: run(exit(1), 5*time.Minute-time.Second),
			want: want{state: v1alpha1.StateVerifying, lastChecks: ran(exit(1), 5*time.Minute-time.Second), verifyingSince: window, reproduced: metav1.ConditionTrue}},
		{name: "exit 1 when the window ends: back to Open, without the precondition result that left it", inc: verifying(), r: run(exit(1), 5*time.Minute),
			want: want{state: v1alpha1.StateOpen, lastChecks: &v1alpha1.LastChecks{Verify: lc(exit(1), 5*time.Minute)}, reproduced: metav1.ConditionTrue}},
		{name: "exit 0 after the window: Resolved", inc: verifying(), r: run(exit(0), 9*time.Minute),
			want: want{state: v1alpha1.StateResolved, lastChecks: ran(exit(0), 9*time.Minute), resolution: resolvedAt(9 * time.Minute), reproduced: metav1.ConditionTrue}},
		{name: "another exit after the window: unknown, stays Verifying", inc: verifying(), r: run(exit(2), 9*time.Minute),
			want: want{state: v1alpha1.StateVerifying, lastChecks: ran(exit(2), 9*time.Minute), verifyingSince: window, reproduced: metav1.ConditionTrue}},
		{name: "a timeout after the window: unknown, stays Verifying", inc: verifying(), r: run(nil, 9*time.Minute),
			want: want{state: v1alpha1.StateVerifying, lastChecks: ran(nil, 9*time.Minute), verifyingSince: window, reproduced: metav1.ConditionTrue}},
		{name: "the window starts at verifyingSince", inc: verifying(withVerifyingSince(4 * time.Minute)), r: run(exit(1), 6*time.Minute),
			want: want{state: v1alpha1.StateVerifying, lastChecks: ran(exit(1), 6*time.Minute), verifyingSince: ptr.To(at(4 * time.Minute)), reproduced: metav1.ConditionTrue}},
		{name: "the same result again keeps its since", inc: verifying(withLastChecks(v1alpha1.LastChecks{Precondition: entered, Verify: lc(exit(1), time.Minute)})), r: run(exit(1), 2*time.Minute),
			want: want{state: v1alpha1.StateVerifying, lastChecks: ran(exit(1), time.Minute), verifyingSince: window, reproduced: metav1.ConditionTrue}},
		{name: "without a window, the run starts it", inc: verifying(func(i *v1alpha1.Incident) { i.Status.VerifyingSince = nil }), r: run(exit(1), 6*time.Minute),
			want: want{state: v1alpha1.StateVerifying, lastChecks: ran(exit(1), 6*time.Minute), verifyingSince: ptr.To(at(6 * time.Minute)), reproduced: metav1.ConditionTrue}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record(tc.inc, cfg, tc.r, tc.r.at)
			diff(t, tc.want, got(tc.inc))
		})
	}
}

func TestRecordApply(t *testing.T) {
	run := func(code *int32) result {
		return result{script: v1alpha1.ScriptApply, exit: code, at: t0.Add(time.Minute)}
	}
	applied := func(code *int32) *v1alpha1.LastCheck { return lc(code, time.Minute) }

	cases := []struct {
		name    string
		inc     *v1alpha1.Incident
		r       result
		changed bool
		want    want
	}{
		{name: "Open, exit 0: Verifying", inc: newIncident(v1alpha1.StateOpen), r: run(exit(0)), changed: true,
			want: want{state: v1alpha1.StateVerifying, lastChecks: &v1alpha1.LastChecks{Apply: applied(exit(0))}, verifyingSince: ptr.To(at(time.Minute))}},
		{name: "Open, exit 1: recorded, stays Open", inc: newIncident(v1alpha1.StateOpen), r: run(exit(1)), changed: true,
			want: want{state: v1alpha1.StateOpen, lastChecks: &v1alpha1.LastChecks{Apply: applied(exit(1))}}},
		{name: "Open, timed out: recorded, stays Open", inc: newIncident(v1alpha1.StateOpen), r: run(nil), changed: true,
			want: want{state: v1alpha1.StateOpen, lastChecks: &v1alpha1.LastChecks{Apply: applied(nil)}}},
		{name: "Verifying, exit 0: restarts the settle window", inc: newIncident(v1alpha1.StateVerifying, withVerifyingSince(0),
			withLastChecks(v1alpha1.LastChecks{Verify: lc(exit(1), 30*time.Second)})), r: run(exit(0)), changed: true,
			want: want{state: v1alpha1.StateVerifying, lastChecks: &v1alpha1.LastChecks{Apply: applied(exit(0))}, verifyingSince: ptr.To(at(time.Minute))}},
		{name: "Verifying, exit 1: the window goes on", inc: newIncident(v1alpha1.StateVerifying, withVerifyingSince(0)), r: run(exit(1)), changed: true,
			want: want{state: v1alpha1.StateVerifying, lastChecks: &v1alpha1.LastChecks{Apply: applied(exit(1))}, verifyingSince: ptr.To(at(0))}},
		{name: "already recorded", inc: newIncident(v1alpha1.StateOpen, withLastChecks(v1alpha1.LastChecks{Apply: applied(exit(1))})), r: run(exit(1)),
			want: want{state: v1alpha1.StateOpen, lastChecks: &v1alpha1.LastChecks{Apply: applied(exit(1))}}},
		{name: "a success already recorded", inc: newIncident(v1alpha1.StateVerifying, withVerifyingSince(time.Minute),
			withLastChecks(v1alpha1.LastChecks{Apply: applied(exit(0)), Verify: lc(exit(1), 2*time.Minute)})), r: run(exit(0)),
			want: want{state: v1alpha1.StateVerifying, lastChecks: &v1alpha1.LastChecks{Apply: applied(exit(0)), Verify: lc(exit(1), 2*time.Minute)}, verifyingSince: ptr.To(at(time.Minute))}},
		{name: "Resolved: nothing", inc: newIncident(v1alpha1.StateResolved), r: run(exit(0)),
			want: want{state: v1alpha1.StateResolved}},
		{name: "Closed: nothing", inc: newIncident(v1alpha1.StateClosed), r: run(exit(0)),
			want: want{state: v1alpha1.StateClosed}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, changed := recordApply(tc.inc, tc.r, "alice")
			if changed != tc.changed {
				t.Errorf("changed: want %v, got %v", tc.changed, changed)
			}
			tc.want.reproduced = metav1.ConditionUnknown
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
	cases := []struct {
		name    string
		inc     *v1alpha1.Incident
		lastRun time.Time
		now     time.Time
		want    bool
	}{
		{name: "no check has run", inc: newIncident(v1alpha1.StateOpen), now: t0, want: true},
		{name: "inside the poll interval", inc: newIncident(v1alpha1.StateOpen), lastRun: t0, now: t0.Add(59 * time.Second), want: false},
		{name: "a poll interval after the last check", inc: newIncident(v1alpha1.StateOpen), lastRun: t0, now: t0.Add(time.Minute), want: true},
		{name: "inside the poll interval from entering Verifying", inc: newIncident(v1alpha1.StateVerifying, withVerifyingSince(30*time.Second)),
			lastRun: t0, now: t0.Add(time.Minute), want: false},
		{name: "a poll interval after entering Verifying", inc: newIncident(v1alpha1.StateVerifying, withVerifyingSince(30*time.Second)),
			lastRun: t0, now: t0.Add(90 * time.Second), want: true},
		{name: "no script to run", inc: newIncident(v1alpha1.StateAnalyzing), now: t0.Add(time.Hour), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if g := checkDue(tc.inc, cfg, tc.lastRun, tc.now); g != tc.want {
				t.Errorf("checkDue = %v, want %v", g, tc.want)
			}
		})
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
	if s := inc.Status.LastChecks.Precondition.Since; !s.Equal(ptr.To(at(0))) {
		t.Fatalf("precondition since = %v, want the first of the runs that exited 1", s)
	}
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
	if inc.Status.VerifyingSince != nil {
		t.Fatalf("verifyingSince = %v, want unset once Resolved", inc.Status.VerifyingSince)
	}
}
