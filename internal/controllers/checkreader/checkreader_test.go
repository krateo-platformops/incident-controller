package checkreader

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/go-cmp/cmp"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/krateo-platformops/provider-runtime/pkg/logging"
)

func list(gv string, resources ...metav1.APIResource) *metav1.APIResourceList {
	return &metav1.APIResourceList{GroupVersion: gv, APIResources: resources}
}

func res(name string, verbs ...string) metav1.APIResource {
	if len(verbs) == 0 {
		verbs = []string{"get", "list", "watch", "create", "update", "patch", "delete"}
	}
	return metav1.APIResource{Name: name, Verbs: verbs}
}

func rule(group string, resources ...string) rbacv1.PolicyRule {
	return rbacv1.PolicyRule{APIGroups: []string{group}, Resources: resources, Verbs: []string{"get", "list", "watch"}}
}

var cluster = []*metav1.APIResourceList{
	list("v1", res("pods"), res("pods/log", "get"), res("pods/exec", "create", "get"), res("secrets"),
		res("configmaps"), res("nodes"), res("nodes/proxy", "get", "create"), res("services"),
		res("services/proxy", "get"), res("serviceaccounts"), res("serviceaccounts/token", "create"),
		res("bindings", "create")),
	list("apps/v1", res("deployments"), res("deployments/scale", "get", "update")),
	list("github.krateo.io/v1alpha1", res("deployments"), res("pullrequests")),
	list("authorization.k8s.io/v1", res("subjectaccessreviews", "create")),
	list("external.example.io/v1", res("secrets"), res("vaults")),
	list("github.krateo.io/v1alpha2", res("pullrequests")),
}

func TestRulesGrantEveryReadableResourceButSecretsAndSubresources(t *testing.T) {
	want := []rbacv1.PolicyRule{
		rule("", "configmaps", "nodes", "pods", "serviceaccounts", "services"),
		rule("apps", "deployments"),
		rule("external.example.io", "vaults"),
		rule("github.krateo.io", "deployments", "pullrequests"),
	}
	if diff := cmp.Diff(want, Rules(cluster)); diff != "" {
		t.Errorf("rules (-want +got):\n%s", diff)
	}
}

type disco struct {
	lists []*metav1.APIResourceList
	err   error
}

func (d disco) ServerResourcesForGroupVersion(string) (*metav1.APIResourceList, error) {
	return nil, errors.New("unused")
}

func (d disco) ServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
	return nil, d.lists, d.err
}

func (d disco) ServerPreferredResources() ([]*metav1.APIResourceList, error) {
	return nil, errors.New("unused")
}

func (d disco) ServerPreferredNamespacedResources() ([]*metav1.APIResourceList, error) {
	return nil, errors.New("unused")
}

func newRefresher(t *testing.T, d disco, current []rbacv1.PolicyRule) (*refresher, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "check-reader"}, Rules: current}).Build()
	log := logging.NewSlogLogger(*slog.New(slog.DiscardHandler))
	return &refresher{o: Options{Role: "check-reader", Log: log}, disco: d, reader: c, writer: c}, c
}

func rules(t *testing.T, c client.Client) []rbacv1.PolicyRule {
	t.Helper()
	role := &rbacv1.ClusterRole{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "check-reader"}, role); err != nil {
		t.Fatal(err)
	}
	return role.Rules
}

func TestSyncWritesTheDiscoveredRules(t *testing.T) {
	r, c := newRefresher(t, disco{lists: cluster}, nil)
	if err := r.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(Rules(cluster), rules(t, c)); diff != "" {
		t.Errorf("rules (-want +got):\n%s", diff)
	}
}

func TestSyncLeavesUnchangedRulesAlone(t *testing.T) {
	r, c := newRefresher(t, disco{lists: cluster}, Rules(cluster))
	before := &rbacv1.ClusterRole{}
	_ = c.Get(context.Background(), client.ObjectKey{Name: "check-reader"}, before)
	if err := r.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := &rbacv1.ClusterRole{}
	_ = c.Get(context.Background(), client.ObjectKey{Name: "check-reader"}, after)
	if before.ResourceVersion != after.ResourceVersion {
		t.Errorf("an unchanged role was written: resourceVersion %s -> %s", before.ResourceVersion, after.ResourceVersion)
	}
}

func TestAGroupDiscoveryFailedForKeepsItsRules(t *testing.T) {
	failed := &discovery.ErrGroupDiscoveryFailed{Groups: map[schema.GroupVersion]error{
		{Group: "metrics.k8s.io", Version: "v1beta1"}: errors.New("503"),
	}}
	current := []rbacv1.PolicyRule{rule("metrics.k8s.io", "nodes", "pods"), rule("gone.example.io", "things")}
	r, c := newRefresher(t, disco{lists: cluster, err: failed}, current)
	if err := r.sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := append(Rules(cluster), rule("metrics.k8s.io", "nodes", "pods"))
	if diff := cmp.Diff(want, rules(t, c)); diff != "" {
		t.Errorf("rules (-want +got):\n%s", diff)
	}
}

func TestAFailedDiscoveryWritesNothing(t *testing.T) {
	current := []rbacv1.PolicyRule{rule("apps", "deployments")}
	r, c := newRefresher(t, disco{err: errors.New("connection refused")}, current)
	if err := r.sync(context.Background()); err == nil {
		t.Error("sync succeeded on a failed discovery")
	}
	if diff := cmp.Diff(current, rules(t, c)); diff != "" {
		t.Errorf("rules changed (-want +got):\n%s", diff)
	}
}
