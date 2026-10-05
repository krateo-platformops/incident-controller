package incident

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	psaapi "k8s.io/pod-security-admission/api"
	"k8s.io/pod-security-admission/policy"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/krateo-platformops/plumbing/kubeutil/event"
	"github.com/krateo-platformops/provider-runtime/pkg/logging"

	"github.com/krateo-platformops/incident-controller/apis/incident/v1alpha1"
)

const credentialsNamespace = "krateo-system"

var applyCfg = ApplyConfig{Timeout: 2 * time.Minute, TTL: 720 * time.Hour, CredentialsNamespace: credentialsNamespace}

// policyAt is when the policy was installed; requests are created after it.
var policyAt = time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)

type recordedEvent struct {
	obj    string
	typ    event.Type
	reason event.Reason
	msg    string
}

type eventLog struct{ events []recordedEvent }

func (l *eventLog) Event(o runtime.Object, e event.Event) {
	l.events = append(l.events, recordedEvent{obj: o.(client.Object).GetName(), typ: e.Type, reason: e.Reason, msg: e.Message})
}

// applyEnv is a fake cluster: incidents, IncidentApplies, credentials Secrets and pods, the
// requester admission policy, and an authorizer that allows what allowPatch says.
type applyEnv struct {
	kube       client.Client
	policies   map[string]time.Time
	allowPatch bool
	sars       []authorizationv1.SubjectAccessReviewSpec
	events     eventLog
}

func newApplyEnv(t *testing.T, objs ...client.Object) *applyEnv {
	t.Helper()
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, authorizationv1.AddToScheme, v1alpha1.SchemeBuilder.AddToScheme} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	env := &applyEnv{
		allowPatch: true,
		policies: map[string]time.Time{
			"MutatingAdmissionPolicy":        policyAt,
			"MutatingAdmissionPolicyBinding": policyAt,
		},
	}
	env.kube = fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.Incident{}, &v1alpha1.IncidentApply{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
				u, ok := o.(*unstructured.Unstructured)
				if !ok {
					return c.Get(ctx, key, o, opts...)
				}
				created, ok := env.policies[u.GetKind()]
				if !ok || key.Name != v1alpha1.RequesterPolicy {
					return apierrors.NewNotFound(schema.GroupResource{Resource: u.GetKind()}, key.Name)
				}
				u.SetName(key.Name)
				u.SetCreationTimestamp(metav1.NewTime(created))
				return nil
			},
			Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
				if sar, ok := o.(*authorizationv1.SubjectAccessReview); ok {
					env.sars = append(env.sars, sar.Spec)
					sar.Status.Allowed = env.allowPatch
					return nil
				}
				return c.Create(ctx, o, opts...)
			},
		}).
		Build()
	return env
}

func (env *applyEnv) external(now time.Time) *applyExternal {
	return &applyExternal{
		kube:   env.kube,
		reader: env.kube,
		cfg:    applyCfg,
		pods:   pods,
		api:    apiServer{host: "https://10.96.0.1:443", ca: []byte("apiserver-ca")},
		logs:   func(context.Context, *corev1.Pod) (string, error) { return "deployment.apps/worker patched\n", nil },
		log:    logging.NewNopLogger(),
		rec:    &env.events,
		now:    func() time.Time { return now },
	}
}

func (env *applyEnv) get(t *testing.T, o client.Object, ns, name string) {
	t.Helper()
	if err := env.kube.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, o); err != nil {
		t.Fatal(err)
	}
}

func newApply(name string, created time.Time, opts ...func(*v1alpha1.IncidentApply)) *v1alpha1.IncidentApply {
	ia := &v1alpha1.IncidentApply{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "krateo-system",
			UID:               types.UID("6f1d2c3b-4a59-4e8f-9d7c-" + name),
			CreationTimestamp: metav1.NewTime(created),
		},
		Spec: v1alpha1.IncidentApplySpec{
			IncidentRef: v1alpha1.IncidentRef{Name: "cd-not-ready-20260925-140000"},
			RequestedBy: v1alpha1.Requester{Username: "alice", Groups: []string{"devs", "system:authenticated"}},
		},
	}
	for _, o := range opts {
		o(ia)
	}
	return ia
}

// credentials is the Secret authn writes at login: a client certificate for cn valid until
// notAfter, each PEM block base64-encoded once more, as authn stores them.
func credentials(t *testing.T, username, cn string, notAfter time.Time) *corev1.Secret {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn, Organization: []string{"devs"}},
		NotBefore:    policyAt.Add(-time.Hour),
		NotAfter:     notAfter,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	b64 := func(typ string, b []byte) []byte {
		return []byte(base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: b})))
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: username + "-clientconfig", Namespace: credentialsNamespace},
		Data: map[string][]byte{
			keyClientCert: b64("CERTIFICATE", der),
			keyClientKey:  b64("EC PRIVATE KEY", keyDER),
			"server-url":  []byte("https://kubernetes.default.svc"),
		},
	}
}

func finishedPod(ia *v1alpha1.IncidentApply, started time.Time, code int32) *corev1.Pod {
	p := pods.ApplyPod(ia, applyCfg.Timeout)
	p.CreationTimestamp = metav1.NewTime(started)
	p.Status.StartTime = ptr.To(metav1.NewTime(started))
	p.Status.Phase = corev1.PodSucceeded
	if code != 0 {
		p.Status.Phase = corev1.PodFailed
	}
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  containerName,
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: code, FinishedAt: metav1.NewTime(started.Add(8 * time.Second))}},
	}}
	return p
}

func TestApplyStartsTheRunAsTheRequester(t *testing.T) {
	now := policyAt.Add(time.Hour)
	ia := newApply("cd-apply-1", now.Add(-time.Second))
	env := newApplyEnv(t, newIncident(v1alpha1.StateOpen), ia, credentials(t, "alice", "alice", now.Add(24*time.Hour)))
	e := env.external(now)

	obs, err := e.Observe(context.Background(), ia)
	if err != nil {
		t.Fatal(err)
	}
	if obs.ResourceExists {
		t.Fatalf("an admitted request has no pod yet: %+v (status %+v)", obs, ia.Status)
	}
	if err := e.Create(context.Background(), ia); err != nil {
		t.Fatal(err)
	}

	if len(env.sars) != 1 || env.sars[0].User != "alice" || env.sars[0].ResourceAttributes.Verb != "patch" ||
		env.sars[0].ResourceAttributes.Resource != "incidents" || env.sars[0].ResourceAttributes.Name != "cd-not-ready-20260925-140000" {
		t.Errorf("want one review of alice patching the incident, got %+v", env.sars)
	}

	name := applyName(ia)
	var pod corev1.Pod
	env.get(t, &pod, checksNamespace, name)
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Error("the apply pod must not mount the service account token")
	}
	if _, ok := pod.Labels[labelIncidentUID]; ok {
		t.Error("the apply pod must not carry the incident UID label: the Incident controller would take it for a check pod")
	}
	if pod.Labels[LabelComponent] != ComponentCheck {
		t.Error("the apply pod must carry the check component label: the NetworkPolicy selects it")
	}
	c := pod.Spec.Containers[0]
	if !hasEnv(c, "KUBECONFIG", "/kube/config") {
		t.Errorf("KUBECONFIG must point at the mounted kubeconfig: %+v", c.Env)
	}
	if pod.Spec.DNSPolicy != corev1.DNSNone || *pod.Spec.ActiveDeadlineSeconds != 120 {
		t.Errorf("the sandbox settings must hold: dns %q, deadline %d", pod.Spec.DNSPolicy, *pod.Spec.ActiveDeadlineSeconds)
	}

	var sec corev1.Secret
	env.get(t, &sec, checksNamespace, name)
	if len(sec.OwnerReferences) != 1 || sec.OwnerReferences[0].Name != name {
		t.Errorf("the kubeconfig Secret must be owned by the pod: %+v", sec.OwnerReferences)
	}
	kc, err := clientcmd.Load(sec.Data[kubeconfigKey])
	if err != nil {
		t.Fatal(err)
	}
	cl := kc.Clusters[kc.Contexts[kc.CurrentContext].Cluster]
	if cl.Server != "https://10.96.0.1:443" || string(cl.CertificateAuthorityData) != "apiserver-ca" {
		t.Errorf("the kubeconfig must reach the apiserver by IP with its CA: %+v", cl)
	}
	ai := kc.AuthInfos[kc.Contexts[kc.CurrentContext].AuthInfo]
	block, _ := pem.Decode(ai.ClientCertificateData)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || cert.Subject.CommonName != "alice" || ai.Token != "" {
		t.Errorf("the kubeconfig must hold alice's client certificate and no token: %v %+v", err, ai)
	}

	var cm corev1.ConfigMap
	env.get(t, &cm, checksNamespace, name)
	if cm.Data[scriptKey] != "true" {
		t.Errorf("the ConfigMap must hold the apply script: %q", cm.Data[scriptKey])
	}
}

func hasEnv(c corev1.Container, name, value string) bool {
	for _, e := range c.Env {
		if e.Name == name && e.Value == value {
			return true
		}
	}
	return false
}

func TestApplyRejects(t *testing.T) {
	now := policyAt.Add(time.Hour)
	valid := now.Add(24 * time.Hour)
	cases := []struct {
		name   string
		apply  *v1alpha1.IncidentApply
		objs   []client.Object
		setup  func(*applyEnv)
		reason string
	}{
		{
			name:   "no requester",
			apply:  newApply("a", now, func(ia *v1alpha1.IncidentApply) { ia.Spec.RequestedBy = v1alpha1.Requester{} }),
			objs:   []client.Object{newIncident(v1alpha1.StateOpen), credentials(t, "alice", "alice", valid)},
			reason: "no requester is recorded",
		},
		{
			name:   "no policy",
			apply:  newApply("a", now),
			objs:   []client.Object{newIncident(v1alpha1.StateOpen), credentials(t, "alice", "alice", valid)},
			setup:  func(env *applyEnv) { delete(env.policies, "MutatingAdmissionPolicyBinding") },
			reason: "is not installed",
		},
		{
			name:   "policy newer than the request",
			apply:  newApply("a", policyAt.Add(-time.Minute)),
			objs:   []client.Object{newIncident(v1alpha1.StateOpen), credentials(t, "alice", "alice", valid)},
			reason: "was created after this request",
		},
		{
			name:   "no incident",
			apply:  newApply("a", now),
			objs:   []client.Object{credentials(t, "alice", "alice", valid)},
			reason: "does not exist",
		},
		{
			name:   "incident not Open",
			apply:  newApply("a", now),
			objs:   []client.Object{newIncident(v1alpha1.StateVerifying), credentials(t, "alice", "alice", valid)},
			reason: "is Verifying; only an Open incident's apply script runs",
		},
		{
			name:  "no apply script",
			apply: newApply("a", now),
			objs: []client.Object{newIncident(v1alpha1.StateOpen, func(inc *v1alpha1.Incident) { inc.Status.HowToFix.Apply = "" }),
				credentials(t, "alice", "alice", valid)},
			reason: "has no apply script",
		},
		{
			name:  "another run is going",
			apply: newApply("b", now),
			objs: []client.Object{newIncident(v1alpha1.StateOpen), credentials(t, "alice", "alice", valid),
				newApply("a", now.Add(time.Second), func(ia *v1alpha1.IncidentApply) { ia.Status.Phase = v1alpha1.ApplyRunning })},
			reason: "IncidentApply a for this incident is already pending or running",
		},
		{
			name:  "an older request goes first",
			apply: newApply("b", now),
			objs: []client.Object{newIncident(v1alpha1.StateOpen), credentials(t, "alice", "alice", valid),
				newApply("a", now.Add(-time.Second))},
			reason: "IncidentApply a for this incident is already pending or running",
		},
		{
			name:   "may not patch the incident",
			apply:  newApply("a", now),
			objs:   []client.Object{newIncident(v1alpha1.StateOpen), credentials(t, "alice", "alice", valid)},
			setup:  func(env *applyEnv) { env.allowPatch = false },
			reason: "alice may not patch incident",
		},
		{
			name:   "no credentials",
			apply:  newApply("a", now),
			objs:   []client.Object{newIncident(v1alpha1.StateOpen)},
			reason: "alice has no Krateo login credentials: log in again",
		},
		{
			name:   "someone else's certificate",
			apply:  newApply("a", now),
			objs:   []client.Object{newIncident(v1alpha1.StateOpen), credentials(t, "alice", "mallory", valid)},
			reason: "the credentials of alice are for mallory",
		},
		{
			name:   "login expires during the run",
			apply:  newApply("a", now),
			objs:   []client.Object{newIncident(v1alpha1.StateOpen), credentials(t, "alice", "alice", now.Add(time.Minute))},
			reason: "before the run could finish: log in again",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newApplyEnv(t, append(tc.objs, tc.apply)...)
			if tc.setup != nil {
				tc.setup(env)
			}
			e := env.external(now)
			ia := tc.apply.DeepCopy()
			env.get(t, ia, ia.Namespace, ia.Name)
			obs, err := e.Observe(context.Background(), ia)
			if err != nil {
				t.Fatal(err)
			}
			if !obs.ResourceExists || obs.ResourceUpToDate {
				t.Fatalf("a rejection is a status change, not a run: %+v", obs)
			}
			if ia.Status.Phase != v1alpha1.ApplyRejected || !strings.Contains(ia.Status.Message, tc.reason) {
				t.Fatalf("want Rejected %q, got %s %q", tc.reason, ia.Status.Phase, ia.Status.Message)
			}
			if err := e.Update(context.Background(), ia); err != nil {
				t.Fatal(err)
			}
			if len(env.events.events) != 1 || env.events.events[0].reason != v1alpha1.ReasonApplyFinished ||
				!strings.HasPrefix(env.events.events[0].msg, "Rejected: ") {
				t.Errorf("want one ApplyFinished Event starting with the phase, got %+v", env.events.events)
			}
			var pl corev1.PodList
			if err := env.kube.List(context.Background(), &pl); err != nil || len(pl.Items) != 0 {
				t.Errorf("a rejected request runs nothing: %v %d pods", err, len(pl.Items))
			}
		})
	}
}

func TestApplyRecordsTheRun(t *testing.T) {
	started := policyAt.Add(time.Hour)
	now := started.Add(30 * time.Second)
	cases := []struct {
		name      string
		code      int32
		phase     v1alpha1.ApplyPhase
		state     v1alpha1.State
		eventType event.Type
	}{
		{name: "exit 0 verifies", code: 0, phase: v1alpha1.ApplySucceeded, state: v1alpha1.StateVerifying, eventType: event.TypeNormal},
		{name: "exit 1 stays Open", code: 1, phase: v1alpha1.ApplyFailed, state: v1alpha1.StateOpen, eventType: event.TypeWarning},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ia := newApply("cd-apply-1", started.Add(-time.Second), func(ia *v1alpha1.IncidentApply) {
				ia.Status.Phase = v1alpha1.ApplyRunning
				ia.Status.StartedAt = ptr.To(metav1.NewTime(started))
			})
			env := newApplyEnv(t, newIncident(v1alpha1.StateOpen), ia, finishedPod(ia, started, tc.code))
			e := env.external(now)
			env.get(t, ia, ia.Namespace, ia.Name)

			if _, err := e.Observe(context.Background(), ia); err != nil {
				t.Fatal(err)
			}
			if err := e.Update(context.Background(), ia); err != nil {
				t.Fatal(err)
			}

			var got v1alpha1.IncidentApply
			env.get(t, &got, ia.Namespace, ia.Name)
			if got.Status.Phase != tc.phase || got.Status.ExitCode == nil || *got.Status.ExitCode != tc.code ||
				got.Status.Output != "deployment.apps/worker patched\n" || got.Status.FinishedAt == nil {
				t.Errorf("unexpected status: %+v", got.Status)
			}

			var inc v1alpha1.Incident
			env.get(t, &inc, "krateo-system", "cd-not-ready-20260925-140000")
			if inc.Status.State != tc.state {
				t.Errorf("incident state: want %s, got %s", tc.state, inc.Status.State)
			}
			if n := len(inc.Status.Checks); n != 1 || inc.Status.Checks[0].Script != v1alpha1.ScriptApply ||
				inc.Status.Checks[0].Exit == nil || *inc.Status.Checks[0].Exit != tc.code {
				t.Errorf("want one apply check with exit %d, got %+v", tc.code, inc.Status.Checks)
			}

			var last recordedEvent
			for _, ev := range env.events.events {
				if ev.obj == ia.Name {
					last = ev
				}
			}
			if last.reason != v1alpha1.ReasonApplyFinished || last.typ != tc.eventType || !strings.HasPrefix(last.msg, string(tc.phase)+": ") {
				t.Errorf("want an ApplyFinished %s Event, got %+v", tc.eventType, env.events.events)
			}

			var pod corev1.Pod
			err := env.kube.Get(context.Background(), types.NamespacedName{Namespace: checksNamespace, Name: applyName(ia)}, &pod)
			if !apierrors.IsNotFound(err) {
				t.Errorf("the finished apply pod must be deleted, got %v", err)
			}

			// A second pass over the same result records nothing more.
			if _, err := e.Observe(context.Background(), &got); err != nil {
				t.Fatal(err)
			}
			env.get(t, &inc, "krateo-system", "cd-not-ready-20260925-140000")
			if len(inc.Status.Checks) != 1 {
				t.Errorf("the run must be recorded once, got %+v", inc.Status.Checks)
			}
		})
	}
}

func TestApplyDeletedAfterItsTTL(t *testing.T) {
	finished := policyAt.Add(time.Hour)
	ia := newApply("cd-apply-1", finished.Add(-time.Minute), func(ia *v1alpha1.IncidentApply) {
		ia.Status.Phase = v1alpha1.ApplySucceeded
		ia.Status.FinishedAt = ptr.To(metav1.NewTime(finished))
	})

	for _, tc := range []struct {
		name    string
		now     time.Time
		deleted bool
	}{
		{name: "within the TTL", now: finished.Add(applyCfg.TTL - time.Second)},
		{name: "past the TTL", now: finished.Add(applyCfg.TTL), deleted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newApplyEnv(t, ia.DeepCopy())
			e := env.external(tc.now)
			got := &v1alpha1.IncidentApply{}
			env.get(t, got, ia.Namespace, ia.Name)
			obs, err := e.Observe(context.Background(), got)
			if err != nil {
				t.Fatal(err)
			}
			if obs.ResourceUpToDate == tc.deleted {
				t.Fatalf("up to date must be %v: %+v", !tc.deleted, obs)
			}
			if err := e.Update(context.Background(), got); err != nil {
				t.Fatal(err)
			}
			err = env.kube.Get(context.Background(), client.ObjectKeyFromObject(ia), &v1alpha1.IncidentApply{})
			if tc.deleted != apierrors.IsNotFound(err) {
				t.Errorf("deleted: want %v, got err %v", tc.deleted, err)
			}
		})
	}
}

func TestApplyPollIntervalHookWaitsForTheTTL(t *testing.T) {
	hook := applyPollIntervalHook(time.Hour)
	running := newApply("a", policyAt, func(ia *v1alpha1.IncidentApply) { ia.Status.Phase = v1alpha1.ApplyRunning })
	if got := hook(running, time.Minute); got != time.Minute {
		t.Errorf("an unfinished request keeps the poll interval: %v", got)
	}
	finished := newApply("a", policyAt, func(ia *v1alpha1.IncidentApply) {
		ia.Status.Phase = v1alpha1.ApplyFailed
		ia.Status.FinishedAt = ptr.To(metav1.NewTime(time.Now().Add(-30 * time.Minute)))
	})
	if got := hook(finished, time.Minute); got < 29*time.Minute || got > 30*time.Minute {
		t.Errorf("a finished request waits for its TTL: %v", got)
	}
	long := applyPollIntervalHook(30 * 24 * time.Hour)
	if got := long(finished, time.Minute); got != maxApplyRequeue {
		t.Errorf("a long TTL is waited out in steps of %v: %v", maxApplyRequeue, got)
	}
}

// TestApplyPodPassesRestrictedPodSecurity checks the apply pod against the restricted Pod Security
// Standard the checks namespace enforces.
func TestApplyPodPassesRestrictedPodSecurity(t *testing.T) {
	evaluator, err := policy.NewEvaluator(policy.DefaultChecks(), nil)
	if err != nil {
		t.Fatal(err)
	}
	p := pods.ApplyPod(newApply("a", policyAt), time.Minute)
	lv := psaapi.LevelVersion{Level: psaapi.LevelRestricted, Version: psaapi.LatestVersion()}
	if r := policy.AggregateCheckResults(evaluator.EvaluatePod(lv, &p.ObjectMeta, &p.Spec)); !r.Allowed {
		t.Errorf("the apply pod violates restricted: %s", r.ForbiddenDetail())
	}
}

func TestPemOfReadsBothEncodings(t *testing.T) {
	block := "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----"
	if got := string(pemOf([]byte(block))); got != block {
		t.Errorf("plain PEM: %q", got)
	}
	if got := string(pemOf([]byte(base64.StdEncoding.EncodeToString([]byte(block))))); got != block {
		t.Errorf("base64 PEM: %q", got)
	}
}

func TestTailBufferKeepsTheEnd(t *testing.T) {
	tb := &tailBuffer{max: 4}
	_, _ = tb.Write([]byte("abc"))
	_, _ = tb.Write([]byte("defg"))
	if string(tb.b) != "defg" {
		t.Errorf("want the last 4 bytes, got %q", tb.b)
	}
}
