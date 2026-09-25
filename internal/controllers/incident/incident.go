// Package incident is the controller of Incident managed resources. The external resource of an
// Incident is the check pod of its current step: the precondition while Open, verify while
// Verifying.
package incident

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/krateo-platformops/plumbing/kubeutil/event"
	"github.com/krateo-platformops/plumbing/kubeutil/eventrecorder"
	prv1 "github.com/krateo-platformops/provider-runtime/apis/common/v1"
	"github.com/krateo-platformops/provider-runtime/pkg/logging"
	"github.com/krateo-platformops/provider-runtime/pkg/meta"
	"github.com/krateo-platformops/provider-runtime/pkg/ratelimiter"
	"github.com/krateo-platformops/provider-runtime/pkg/reconciler"
	"github.com/krateo-platformops/provider-runtime/pkg/resource"

	"github.com/krateo-platformops/incident-controller/apis/incident/v1alpha1"
	"github.com/krateo-platformops/incident-controller/internal/controllers/common/option"
)

var errNotIncident = errors.New("managed resource is not an Incident")

const actionReconcile event.Action = "Reconcile"

// Options configure the Incident controller.
type Options struct {
	Controller option.ControllerOptions
	// Checks is the check cadence; its PollInterval is taken from Controller.
	Checks Config
	Pods   CheckPods
}

// Setup adds the Incident controller to the manager.
func Setup(mgr ctrl.Manager, o Options) error {
	name := reconciler.ControllerName(v1alpha1.IncidentGroupKind)
	log := o.Controller.Logger.WithValues("controller", name)

	recorder, err := eventrecorder.Create(context.Background(), mgr.GetConfig(), name, nil)
	if err != nil {
		return fmt.Errorf("failed to create event recorder: %w", err)
	}
	rec := event.NewAPIRecorder(recorder)

	cfg := o.Checks
	cfg.PollInterval = o.Controller.PollInterval

	r := reconciler.NewReconciler(mgr,
		resource.ManagedKind(v1alpha1.IncidentGroupVersionKind),
		reconciler.WithExternalConnecter(&connector{kube: mgr.GetClient(), cfg: cfg, pods: o.Pods, log: log, rec: rec}),
		reconciler.WithPollInterval(cfg.PollInterval),
		reconciler.WithPollIntervalHook(pollIntervalHook),
		reconciler.WithLogger(log),
		reconciler.WithRecorder(rec),
		reconciler.WithTimeout(o.Controller.Timeout),
	)

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.Controller.ForControllerRuntime()).
		For(&v1alpha1.Incident{}).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(incidentOfPod)).
		Complete(ratelimiter.New(name, r, o.Controller.GlobalRateLimiter))
}

// incidentOfPod maps a check pod to its Incident, so a finished check is read at once.
func incidentOfPod(_ context.Context, o client.Object) []reconcile.Request {
	a := o.GetAnnotations()
	ns, name := a[annotationIncidentNamespace], a[annotationIncidentName]
	if ns == "" || name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}}
}

// pollIntervalHook requeues an incident when its next check falls due, within the poll interval.
func pollIntervalHook(mg resource.Managed, poll time.Duration) time.Duration {
	inc, ok := mg.(*v1alpha1.Incident)
	if !ok || currentScript(inc) == "" {
		return poll
	}
	d := time.Until(nextCheckAt(inc, poll))
	switch {
	case d <= 0:
		// A check is running or starting; its pod's events requeue the incident.
		return poll
	case d < time.Second:
		return time.Second
	case d > poll:
		return poll
	}
	return d
}

type connector struct {
	kube client.Client
	cfg  Config
	pods CheckPods
	log  logging.Logger
	rec  event.Recorder
}

func (c *connector) Connect(_ context.Context, mg resource.Managed) (reconciler.ExternalClient, error) {
	inc, ok := mg.(*v1alpha1.Incident)
	if !ok {
		return nil, errNotIncident
	}
	return &external{
		kube: c.kube,
		cfg:  c.cfg,
		pods: c.pods,
		log:  c.log.WithValues("incident", inc.Namespace+"/"+inc.Name),
		rec:  c.rec,
		now:  time.Now,
	}, nil
}

// plan is what Observe found for Update to act on. A new external serves each reconcile.
type plan struct {
	// changes are the state changes to announce once the status is persisted.
	changes []change
	// dirty means the status changed beyond conditions.
	dirty bool
	// consumeApplied means spec.applied was acted on and goes back to false.
	consumeApplied bool
	// stale are the check pods to delete: finished ones, and those of a script no longer current.
	stale []corev1.Pod
	// live is the running check pod of the current script.
	live *corev1.Pod
}

func (p plan) String() string {
	var parts []string
	for _, c := range p.changes {
		parts = append(parts, c.message)
	}
	if p.consumeApplied {
		parts = append(parts, "spec.applied consumed")
	}
	if n := len(p.stale); n > 0 {
		parts = append(parts, fmt.Sprintf("%d check pod(s) to delete", n))
	}
	return strings.Join(parts, "; ")
}

type external struct {
	kube client.Client
	cfg  Config
	pods CheckPods
	log  logging.Logger
	rec  event.Recorder
	now  func() time.Time
	plan plan
}

// Observe reads the incident's check pods, records the finished ones and applies the resulting
// transitions and the human decisions in spec to the status. It changes nothing outside the
// Incident object held in memory; Update persists and acts.
func (e *external) Observe(ctx context.Context, mg resource.Managed) (reconciler.ExternalObservation, error) {
	inc, ok := mg.(*v1alpha1.Incident)
	if !ok {
		return reconciler.ExternalObservation{}, errNotIncident
	}

	pods, cms, err := e.list(ctx, inc)
	if err != nil {
		return reconciler.ExternalObservation{}, err
	}
	if meta.WasDeleted(inc) {
		return reconciler.ExternalObservation{ResourceExists: len(pods)+len(cms) > 0}, nil
	}

	now := e.now()
	changes, consumed := applySpec(inc, now)
	e.plan = plan{changes: changes, dirty: len(changes) > 0 || consumed, consumeApplied: consumed}

	sort.Slice(pods, func(i, j int) bool { return pods[i].CreationTimestamp.Before(&pods[j].CreationTimestamp) })
	for i := range pods {
		pod := &pods[i]
		if pod.DeletionTimestamp != nil {
			continue
		}
		r, done := resultOf(pod, e.cfg.CheckTimeout, now)
		switch {
		case !done && r.script == currentScript(inc) && e.plan.live == nil:
			e.plan.live = pod
		case !done:
			e.plan.stale = append(e.plan.stale, *pod)
		default:
			// The result of a script that is no longer current is dropped: the state it tested
			// has moved on.
			if r.script == currentScript(inc) && !recorded(inc, r) {
				e.plan.changes = append(e.plan.changes, record(inc, e.cfg, r, now)...)
				e.plan.dirty = true
			}
			e.plan.stale = append(e.plan.stale, *pod)
		}
	}

	inc.SetConditions(prv1.Available())

	if e.plan.dirty || len(e.plan.stale) > 0 {
		return reconciler.ExternalObservation{ResourceExists: true, ResourceUpToDate: false, Diff: e.plan.String()}, nil
	}
	// A create interrupted before its outcome was recorded may have left no pod. Nothing leaks
	// from a lost check pod, so such an incident reports its pod existing: the reconciler closes
	// the handshake, and the next reconcile starts the check again.
	if e.plan.live == nil && checkDue(inc, e.cfg, now) && !meta.ExternalCreateIncomplete(inc) {
		return reconciler.ExternalObservation{ResourceExists: false}, nil
	}
	return reconciler.ExternalObservation{ResourceExists: true, ResourceUpToDate: true}, nil
}

// Create starts the check pod of the current script.
func (e *external) Create(ctx context.Context, mg resource.Managed) error {
	inc, ok := mg.(*v1alpha1.Incident)
	if !ok {
		return errNotIncident
	}
	s := currentScript(inc)
	if s == "" {
		return nil
	}
	return e.startCheck(ctx, inc, s)
}

// Update persists the status Observe computed, then consumes spec.applied, deletes the stale check
// pods, and starts the next check when it is due. The status goes first, so a check result or a
// consumed spec.applied is never lost with its pod or its flag.
func (e *external) Update(ctx context.Context, mg resource.Managed) error {
	inc, ok := mg.(*v1alpha1.Incident)
	if !ok {
		return errNotIncident
	}

	if e.plan.dirty {
		if err := e.kube.Status().Update(ctx, inc); err != nil {
			return fmt.Errorf("cannot persist incident status: %w", err)
		}
		for _, c := range e.plan.changes {
			e.rec.Event(inc, event.Normal(event.Reason(c.reason), actionReconcile, c.message))
			e.log.Info(c.message, "reason", c.reason)
		}
	}
	if e.plan.consumeApplied {
		inc.Spec.Applied = false
		if err := e.kube.Update(ctx, inc); err != nil {
			return fmt.Errorf("cannot reset spec.applied: %w", err)
		}
	}
	for i := range e.plan.stale {
		if err := e.kube.Delete(ctx, &e.plan.stale[i], client.PropagationPolicy("Background")); resource.IgnoreNotFound(err) != nil {
			return fmt.Errorf("cannot delete check pod %s: %w", e.plan.stale[i].Name, err)
		}
	}
	if e.plan.live == nil && checkDue(inc, e.cfg, e.now()) {
		return e.startCheck(ctx, inc, currentScript(inc))
	}
	return nil
}

// Delete removes every check pod and ConfigMap of the incident.
func (e *external) Delete(ctx context.Context, mg resource.Managed) error {
	inc, ok := mg.(*v1alpha1.Incident)
	if !ok {
		return errNotIncident
	}
	pods, cms, err := e.list(ctx, inc)
	if err != nil {
		return err
	}
	for i := range pods {
		if err := e.kube.Delete(ctx, &pods[i], client.PropagationPolicy("Background")); resource.IgnoreNotFound(err) != nil {
			return fmt.Errorf("cannot delete check pod %s: %w", pods[i].Name, err)
		}
	}
	for i := range cms {
		if err := e.kube.Delete(ctx, &cms[i]); resource.IgnoreNotFound(err) != nil {
			return fmt.Errorf("cannot delete check ConfigMap %s: %w", cms[i].Name, err)
		}
	}
	return nil
}

func (e *external) list(ctx context.Context, inc *v1alpha1.Incident) ([]corev1.Pod, []corev1.ConfigMap, error) {
	opts := []client.ListOption{client.InNamespace(e.pods.Namespace), client.MatchingLabels(selector(inc))}
	var pods corev1.PodList
	if err := e.kube.List(ctx, &pods, opts...); err != nil {
		return nil, nil, fmt.Errorf("cannot list check pods: %w", err)
	}
	var cms corev1.ConfigMapList
	if err := e.kube.List(ctx, &cms, opts...); err != nil {
		return nil, nil, fmt.Errorf("cannot list check ConfigMaps: %w", err)
	}
	return pods.Items, cms.Items, nil
}

// startCheck creates the check pod, then the ConfigMap with its script, owned by the pod. The
// kubelet waits for the ConfigMap before it starts the container.
func (e *external) startCheck(ctx context.Context, inc *v1alpha1.Incident, s v1alpha1.Script) error {
	pod := e.pods.Pod(inc, s)
	if err := e.kube.Create(ctx, pod); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("cannot create check pod: %w", err)
		}
		if err := e.kube.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
			return fmt.Errorf("cannot get check pod: %w", err)
		}
	}
	if err := e.kube.Create(ctx, e.pods.ConfigMap(inc, pod, s)); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("cannot create check ConfigMap: %w", err)
	}
	e.log.Debug("started check", "script", s, "pod", pod.Name)
	return nil
}
