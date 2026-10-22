// Package checkreader keeps the check pods' read access current. The rules of one ClusterRole,
// bound to the check service account, grant get, list and watch on every resource the API server
// serves except Secrets, rewritten from discovery so a resource installed later is readable too.
package checkreader

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/krateo-platformops/provider-runtime/pkg/logging"
)

// verbs are the verbs every rule grants.
var verbs = []string{"get", "list", "watch"}

// Options configure the refresher.
type Options struct {
	// Role is the ClusterRole whose rules it writes. The chart creates it, without rules.
	Role     string
	Interval time.Duration
	Log      logging.Logger
}

// Setup adds the refresher to the manager. It runs on the leader, at start and then every
// Interval.
func Setup(mgr ctrl.Manager, o Options) error {
	disco, err := discovery.NewDiscoveryClientForConfig(mgr.GetConfig())
	if err != nil {
		return fmt.Errorf("discovery client: %w", err)
	}
	r := &refresher{o: o, disco: disco, reader: mgr.GetAPIReader(), writer: mgr.GetClient()}
	return mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		t := time.NewTicker(o.Interval)
		defer t.Stop()
		for {
			if err := r.sync(ctx); err != nil {
				o.Log.Info("check reader role not refreshed", "role", o.Role, "error", err.Error())
			}
			select {
			case <-ctx.Done():
				return nil
			case <-t.C:
			}
		}
	}))
}

type refresher struct {
	o     Options
	disco discovery.ServerResourcesInterface
	// reader reads the role past the cache: the controller may read only that one ClusterRole,
	// so it cannot list them for an informer.
	reader client.Reader
	writer client.Writer
}

// sync writes the role's rules from discovery when they differ. A group discovery could not read
// keeps the rules it has, so an unhealthy aggregated API does not take reads away.
func (r *refresher) sync(ctx context.Context) error {
	_, lists, derr := r.disco.ServerGroupsAndResources()
	var failed []string
	if derr != nil {
		var gerr *discovery.ErrGroupDiscoveryFailed
		if !errors.As(derr, &gerr) {
			return fmt.Errorf("discovery: %w", derr)
		}
		for gv := range gerr.Groups {
			failed = append(failed, gv.Group)
		}
	}
	role := &rbacv1.ClusterRole{}
	if err := r.reader.Get(ctx, client.ObjectKey{Name: r.o.Role}, role); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("ClusterRole %s does not exist", r.o.Role)
		}
		return err
	}
	want := withKept(Rules(lists), role.Rules, failed)
	if equality.Semantic.DeepEqual(role.Rules, want) {
		return nil
	}
	role.Rules = want
	if err := r.writer.Update(ctx, role); err != nil {
		return err
	}
	r.o.Log.Info("check reader role refreshed", "role", r.o.Role, "groups", len(want))
	return nil
}

// excluded says a resource is never granted: Secrets hold credentials, in whatever group serves
// them.
func excluded(name string) bool {
	return name == "secrets"
}

// Rules grants get, list and watch on every resource in lists that can be read, one rule per API
// group, except the excluded ones and every subresource: a subresource reads past the object
// (a pod's log, a node's or service's proxy), and kubectl get reads none.
func Rules(lists []*metav1.APIResourceList) []rbacv1.PolicyRule {
	byGroup := map[string][]string{}
	for _, l := range lists {
		if l == nil {
			continue
		}
		gv, err := schema.ParseGroupVersion(l.GroupVersion)
		if err != nil {
			continue
		}
		for _, res := range l.APIResources {
			if strings.Contains(res.Name, "/") || excluded(res.Name) ||
				!slices.Contains(res.Verbs, "get") && !slices.Contains(res.Verbs, "list") {
				continue
			}
			if !slices.Contains(byGroup[gv.Group], res.Name) {
				byGroup[gv.Group] = append(byGroup[gv.Group], res.Name)
			}
		}
	}
	return sorted(byGroup)
}

// withKept adds to want the rules current holds for the groups discovery failed to read and
// want lacks.
func withKept(want, current []rbacv1.PolicyRule, failed []string) []rbacv1.PolicyRule {
	if len(failed) == 0 {
		return want
	}
	byGroup := map[string][]string{}
	for _, rule := range want {
		byGroup[rule.APIGroups[0]] = rule.Resources
	}
	discovered := func(g string) bool {
		return slices.ContainsFunc(want, func(r rbacv1.PolicyRule) bool { return r.APIGroups[0] == g })
	}
	for _, rule := range current {
		for _, g := range rule.APIGroups {
			if discovered(g) || !slices.Contains(failed, g) {
				continue
			}
			for _, res := range rule.Resources {
				if !excluded(res) && !slices.Contains(byGroup[g], res) {
					byGroup[g] = append(byGroup[g], res)
				}
			}
		}
	}
	return sorted(byGroup)
}

func sorted(byGroup map[string][]string) []rbacv1.PolicyRule {
	groups := make([]string, 0, len(byGroup))
	for g := range byGroup {
		groups = append(groups, g)
	}
	slices.Sort(groups)
	rules := make([]rbacv1.PolicyRule, 0, len(groups))
	for _, g := range groups {
		resources := slices.Clone(byGroup[g])
		slices.Sort(resources)
		rules = append(rules, rbacv1.PolicyRule{APIGroups: []string{g}, Resources: resources, Verbs: slices.Clone(verbs)})
	}
	return rules
}
