package policyjoin

import (
	"slices"
	"strconv"
	"testing"

	"github.com/bowei/kvec/pkg/kv"
	"github.com/bowei/xnp/internal/convert"
	"github.com/bowei/xnp/internal/npolicies"
	"github.com/bowei/xnp/internal/pods"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func testPod(ns, name string, labels map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels},
		Spec:       corev1.PodSpec{NodeName: "node1"},
	}
}

func testPolicy(ns, name string, spec networkingv1.NetworkPolicySpec) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       spec,
	}
}

func selector(labels map[string]string) *metav1.LabelSelector {
	return &metav1.LabelSelector{MatchLabels: labels}
}

// joinOf runs the whole startup sequence, since the join is only meaningful on
// stores the earlier steps built: the relevant labels a policy contributes are
// what the classes it is then matched against are projected onto, so a test
// that hand-built the classes would be testing a projection the controller
// never produces.
func joinOf(
	t *testing.T,
	namespaces map[string]map[string]string,
	policies []*networkingv1.NetworkPolicy,
	podList []*corev1.Pod,
) *Join {
	t.Helper()

	nsCache := map[string]*corev1.Namespace{}
	nsLabels := map[string]*kv.KV{}
	for name, labels := range namespaces {
		nsCache[name] = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		}
		compiled := convert.LabelsToKV(labels)
		nsLabels[name] = &compiled
	}

	nps := npolicies.New()
	err := nps.Init(nsCache, func(yield func(*networkingv1.NetworkPolicy) bool) {
		for _, np := range policies {
			if !yield(np) {
				return
			}
		}
	})
	if err != nil {
		t.Fatalf("NPolicies.Init() = %v, want nil", err)
	}

	store := pods.New()
	err = store.Init(nps.RLabels(), func(yield func(*corev1.Pod) bool) {
		for _, p := range podList {
			if !yield(p) {
				return
			}
		}
	})
	if err != nil {
		t.Fatalf("Pods.Init() = %v, want nil", err)
	}

	var j Join
	if err := j.Init(nps, store, nsLabels); err != nil {
		t.Fatalf("Join.Init() = %v, want nil", err)
	}
	return &j
}

// rows renders the join sorted, so a comparison does not depend on the map
// order the policies and classes were walked in.
func rows(j *Join) []string {
	out := make([]string, 0, len(j.entries))
	for _, e := range j.entries {
		dir := "ingress"
		if e.dir == DirectionEgress {
			dir = "egress"
		}
		out = append(out, e.policy.Namespace+"/"+e.policy.Name+
			" "+dir+" rule="+ruleIndex(e)+
			" src="+class(e.src)+" dest="+class(e.dest)+
			" peerNs="+e.peerNs)
	}
	slices.Sort(out)
	return out
}

// ruleIndex recovers which of its policy's rules an entry points at. The entry
// carries the pointer rather than an index, so the lookup is by identity
// against the slice the pointer has to have come from.
func ruleIndex(e entry) string {
	if e.rule == nil {
		return "none"
	}
	rules := e.policy.Ingress
	if e.dir == DirectionEgress {
		rules = e.policy.Egress
	}
	for i := range rules {
		if &rules[i] == e.rule {
			return strconv.Itoa(i)
		}
	}
	return "unknown"
}

// class renders an equivalence class by its MapKey, which is fixed by the
// contents alone and so is the same string however the class was built.
func class(c *kv.KV) string {
	if c == nil {
		return "any"
	}
	return string(c.Comparable())
}

// wantClass names the class of a pod projecting onto labels, in the form rows
// renders one.
func wantClass(labels map[string]string) string {
	c := convert.LabelsToKV(labels)
	return string(c.Comparable())
}

func checkRows(t *testing.T, j *Join, want []string) {
	t.Helper()

	slices.Sort(want)
	got := rows(j)
	if slices.Equal(got, want) {
		return
	}
	t.Errorf("join has %d rows, want %d", len(got), len(want))
	for _, r := range got {
		if !slices.Contains(want, r) {
			t.Errorf("unexpected row: %s", r)
		}
	}
	for _, r := range want {
		if !slices.Contains(got, r) {
			t.Errorf("missing row: %s", r)
		}
	}
}

// The base case: one rule, one namespace, and classes standing in for many
// pods. web1 and web2 share a class and produce one row between them, which is
// the whole point of joining classes rather than pods.
func TestInit_SameNamespace(t *testing.T) {
	web := map[string]string{"app": "web"}
	db := map[string]string{"app": "db"}

	j := joinOf(t,
		map[string]map[string]string{"prod": nil},
		[]*networkingv1.NetworkPolicy{
			testPolicy("prod", "allow-db", networkingv1.NetworkPolicySpec{
				PodSelector: *selector(web),
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
				Ingress: []networkingv1.NetworkPolicyIngressRule{{
					From: []networkingv1.NetworkPolicyPeer{{PodSelector: selector(db)}},
				}},
			}),
		},
		[]*corev1.Pod{
			testPod("prod", "web1", web),
			testPod("prod", "web2", web),
			testPod("prod", "db1", db),
			testPod("prod", "cache1", map[string]string{"app": "cache"}),
		},
	)

	checkRows(t, j, []string{
		"prod/allow-db ingress rule=0 src=" + wantClass(db) + " dest=" + wantClass(web) + " peerNs=prod",
	})
}

// Egress puts the policy's own pods on the src side. Same rule, mirrored.
func TestInit_Egress(t *testing.T) {
	web := map[string]string{"app": "web"}
	db := map[string]string{"app": "db"}

	j := joinOf(t,
		map[string]map[string]string{"prod": nil},
		[]*networkingv1.NetworkPolicy{
			testPolicy("prod", "to-db", networkingv1.NetworkPolicySpec{
				PodSelector: *selector(web),
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
				Egress: []networkingv1.NetworkPolicyEgressRule{{
					To: []networkingv1.NetworkPolicyPeer{{PodSelector: selector(db)}},
				}},
			}),
		},
		[]*corev1.Pod{testPod("prod", "web1", web), testPod("prod", "db1", db)},
	)

	checkRows(t, j, []string{
		"prod/to-db egress rule=0 src=" + wantClass(web) + " dest=" + wantClass(db) + " peerNs=prod",
	})
}

// The default deny: a policy that isolates and allows nothing. It still has to
// reach a renderer, so it produces a row per subject class with no rule.
//
// Its empty podSelector names no keys, so nothing is relevant in the namespace
// and every pod projects onto the empty class -- one subject, not three.
func TestInit_DefaultDeny(t *testing.T) {
	j := joinOf(t,
		map[string]map[string]string{"prod": nil},
		[]*networkingv1.NetworkPolicy{
			testPolicy("prod", "deny-all", networkingv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{},
				PolicyTypes: []networkingv1.PolicyType{
					networkingv1.PolicyTypeIngress,
					networkingv1.PolicyTypeEgress,
				},
			}),
		},
		[]*corev1.Pod{
			testPod("prod", "web1", map[string]string{"app": "web"}),
			testPod("prod", "db1", map[string]string{"app": "db"}),
		},
	)

	empty := wantClass(nil)
	checkRows(t, j, []string{
		"prod/deny-all ingress rule=none src=any dest=" + empty + " peerNs=",
		"prod/deny-all egress rule=none src=" + empty + " dest=any peerNs=",
	})
}

// policyTypes is authoritative: the egress rule below is dead because the
// policy does not declare Egress, and joining it would allow traffic the API
// says the policy does not.
func TestInit_PolicyTypesAuthoritative(t *testing.T) {
	web := map[string]string{"app": "web"}
	db := map[string]string{"app": "db"}

	j := joinOf(t,
		map[string]map[string]string{"prod": nil},
		[]*networkingv1.NetworkPolicy{
			testPolicy("prod", "ingress-only", networkingv1.NetworkPolicySpec{
				PodSelector: *selector(web),
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
				Egress: []networkingv1.NetworkPolicyEgressRule{{
					To: []networkingv1.NetworkPolicyPeer{{PodSelector: selector(db)}},
				}},
			}),
		},
		[]*corev1.Pod{testPod("prod", "web1", web), testPod("prod", "db1", db)},
	)

	checkRows(t, j, []string{
		"prod/ingress-only ingress rule=none src=any dest=" + wantClass(web) + " peerNs=",
	})
}

// A peer reaching into other namespaces produces one row per namespace it
// reaches. prod and other hold classes that project onto identical labels and
// are still two peers: the dedup is namespace qualified, because the pods
// behind the two classes are different pods.
func TestInit_CrossNamespace(t *testing.T) {
	web := map[string]string{"app": "web"}
	db := map[string]string{"app": "db"}

	j := joinOf(t,
		map[string]map[string]string{
			"front":   nil,
			"prod":    {"tier": "prod"},
			"other":   {"tier": "prod"},
			"staging": {"tier": "staging"},
		},
		[]*networkingv1.NetworkPolicy{
			testPolicy("front", "from-prod-db", networkingv1.NetworkPolicySpec{
				PodSelector: *selector(web),
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
				Ingress: []networkingv1.NetworkPolicyIngressRule{{
					From: []networkingv1.NetworkPolicyPeer{{
						NamespaceSelector: selector(map[string]string{"tier": "prod"}),
						PodSelector:       selector(db),
					}},
				}},
			}),
		},
		[]*corev1.Pod{
			testPod("front", "web1", web),
			testPod("prod", "db1", db),
			testPod("other", "db2", db),
			testPod("staging", "db3", db),
		},
	)

	checkRows(t, j, []string{
		"front/from-prod-db ingress rule=0 src=" + wantClass(db) + " dest=" + wantClass(web) + " peerNs=prod",
		"front/from-prod-db ingress rule=0 src=" + wantClass(db) + " dest=" + wantClass(web) + " peerNs=other",
	})
}

// An empty namespaceSelector is not an omitted one: it reaches every namespace,
// the policy's own included.
func TestInit_EmptyNamespaceSelector(t *testing.T) {
	web := map[string]string{"app": "web"}
	db := map[string]string{"app": "db"}

	j := joinOf(t,
		map[string]map[string]string{"front": nil, "prod": nil},
		[]*networkingv1.NetworkPolicy{
			testPolicy("front", "from-any-db", networkingv1.NetworkPolicySpec{
				PodSelector: *selector(web),
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
				Ingress: []networkingv1.NetworkPolicyIngressRule{{
					From: []networkingv1.NetworkPolicyPeer{{
						NamespaceSelector: &metav1.LabelSelector{},
						PodSelector:       selector(db),
					}},
				}},
			}),
		},
		[]*corev1.Pod{
			testPod("front", "web1", web),
			testPod("front", "db0", db),
			testPod("prod", "db1", db),
		},
	)

	checkRows(t, j, []string{
		"front/from-any-db ingress rule=0 src=" + wantClass(db) + " dest=" + wantClass(web) + " peerNs=front",
		"front/from-any-db ingress rule=0 src=" + wantClass(db) + " dest=" + wantClass(web) + " peerNs=prod",
	})
}

// Two peer sides that are not pod classes. Rule 0 allows every source; rule 1
// allows a CIDR, and its two blocks collapse to the single row that says "read
// the rule".
func TestInit_NonPodPeers(t *testing.T) {
	web := map[string]string{"app": "web"}

	j := joinOf(t,
		map[string]map[string]string{"prod": nil},
		[]*networkingv1.NetworkPolicy{
			testPolicy("prod", "open", networkingv1.NetworkPolicySpec{
				PodSelector: *selector(web),
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
				Ingress: []networkingv1.NetworkPolicyIngressRule{
					{},
					{From: []networkingv1.NetworkPolicyPeer{
						{IPBlock: &networkingv1.IPBlock{CIDR: "10.0.0.0/8"}},
						{IPBlock: &networkingv1.IPBlock{CIDR: "192.168.0.0/16"}},
					}},
				},
			}),
		},
		[]*corev1.Pod{testPod("prod", "web1", web)},
	)

	checkRows(t, j, []string{
		"prod/open ingress rule=0 src=any dest=" + wantClass(web) + " peerNs=",
		"prod/open ingress rule=1 src=any dest=" + wantClass(web) + " peerNs=",
	})
}

// Two peers of one rule selecting the same class is one row, not two: against a
// given subject they are the same pair, and a duplicate would render twice.
func TestInit_DuplicatePeerClasses(t *testing.T) {
	web := map[string]string{"app": "web"}
	db := map[string]string{"app": "db", "tier": "data"}

	j := joinOf(t,
		map[string]map[string]string{"prod": nil},
		[]*networkingv1.NetworkPolicy{
			testPolicy("prod", "overlapping", networkingv1.NetworkPolicySpec{
				PodSelector: *selector(web),
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
				Ingress: []networkingv1.NetworkPolicyIngressRule{{
					From: []networkingv1.NetworkPolicyPeer{
						{PodSelector: selector(map[string]string{"app": "db"})},
						{PodSelector: selector(map[string]string{"tier": "data"})},
					},
				}},
			}),
		},
		[]*corev1.Pod{testPod("prod", "web1", web), testPod("prod", "db1", db)},
	)

	// The subject projects onto both relevant keys, and carries only one.
	checkRows(t, j, []string{
		"prod/overlapping ingress rule=0 src=" + wantClass(db) + " dest=" + wantClass(web) + " peerNs=prod",
	})
}

// A policy selecting nothing that exists contributes no rows at all, not even
// the isolation row: there is no class for it to isolate.
func TestInit_NoSubjects(t *testing.T) {
	j := joinOf(t,
		map[string]map[string]string{"prod": nil},
		[]*networkingv1.NetworkPolicy{
			testPolicy("prod", "orphan", networkingv1.NetworkPolicySpec{
				PodSelector: *selector(map[string]string{"app": "absent"}),
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			}),
		},
		[]*corev1.Pod{testPod("prod", "web1", map[string]string{"app": "web"})},
	)

	checkRows(t, j, nil)
}

// Every subject class crosses with every peer class: two of each is four rows,
// which is the shape the fan-out has and the reason the peers are resolved once
// per rule rather than once per subject.
func TestInit_ClassCrossProduct(t *testing.T) {
	j := joinOf(t,
		map[string]map[string]string{"prod": nil},
		[]*networkingv1.NetworkPolicy{
			testPolicy("prod", "cross", networkingv1.NetworkPolicySpec{
				PodSelector: *selector(map[string]string{"role": "server"}),
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
				Ingress: []networkingv1.NetworkPolicyIngressRule{{
					From: []networkingv1.NetworkPolicyPeer{
						{PodSelector: selector(map[string]string{"role": "client"})},
					},
				}},
			}),
		},
		[]*corev1.Pod{
			testPod("prod", "s1", map[string]string{"role": "server", "app": "a"}),
			testPod("prod", "s2", map[string]string{"role": "server", "app": "b"}),
			testPod("prod", "c1", map[string]string{"role": "client", "app": "a"}),
			testPod("prod", "c2", map[string]string{"role": "client", "app": "b"}),
		},
	)

	// Only "role" is relevant, so "app" is projected away and the four pods
	// collapse to two classes rather than four.
	server := wantClass(map[string]string{"role": "server"})
	client := wantClass(map[string]string{"role": "client"})
	checkRows(t, j, []string{
		"prod/cross ingress rule=0 src=" + client + " dest=" + server + " peerNs=prod",
	})
}
