package repr

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/set"
)

func TestNewLabels(t *testing.T) {
	labels := labelsToKV(map[string]string{"app": "web", "tier": "prod"})
	if labels.Len() != 2 {
		t.Errorf("Len() = %d, want 2", labels.Len())
	}
	if v, ok := labels.Get("app"); !ok || v != "web" {
		t.Errorf("Get(\"app\") = %q, %v, want \"web\", true", v, ok)
	}
	if _, ok := labels.Get("missing"); ok {
		t.Errorf("Get(\"missing\") ok = true, want false")
	}

	empty := labelsToKV(nil)
	if empty.Len() != 0 {
		t.Errorf("NewLabels(nil).Len() = %d, want 0", empty.Len())
	}
}

func TestRelevantLabels(t *testing.T) {
	labels := map[string]string{"app": "web", "tier": "prod", "pod-template-hash": "abc123"}

	for _, tc := range []struct {
		name   string
		labels map[string]string
		keys   []string
		want   map[string]string
	}{
		{
			name:   "keeps the relevant keys and drops the rest",
			labels: labels,
			keys:   []string{"app", "tier"},
			want:   map[string]string{"app": "web", "tier": "prod"},
		},
		{
			// The projection is what makes two objects equivalent, so a key no
			// policy names has to leave no trace: these labels and a pod with
			// only app=web must compile to the same KV.
			name:   "an irrelevant key leaves nothing behind",
			labels: labels,
			keys:   []string{"app"},
			want:   map[string]string{"app": "web"},
		},
		{
			name:   "a relevant key the object does not carry stays absent",
			labels: labels,
			keys:   []string{"app", "env"},
			want:   map[string]string{"app": "web"},
		},
		{
			// Walked from the keys rather than from the labels, which is the
			// other side of RelevantLabels.
			name:   "more keys than labels",
			labels: map[string]string{"app": "web"},
			keys:   []string{"a", "b", "c", "d", "app", "e"},
			want:   map[string]string{"app": "web"},
		},
		{
			name:   "no relevant keys",
			labels: labels,
			keys:   nil,
			want:   nil,
		},
		{
			name:   "no labels",
			labels: nil,
			keys:   []string{"app"},
			want:   nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := RelevantLabels(tc.labels, set.New(tc.keys...))
			if got.Len() != len(tc.want) {
				t.Fatalf("Len() = %d, want %d", got.Len(), len(tc.want))
			}
			for k, want := range tc.want {
				if v, ok := got.Get(k); !ok || v != want {
					t.Errorf("Get(%q) = %q, %v, want %q, true", k, v, ok, want)
				}
			}
			if want := labelsToKV(tc.want); !got.Equal(&want) {
				t.Errorf("projection of %v onto %v is not the KV of %v", tc.labels, tc.keys, tc.want)
			}
		})
	}
}

func TestNewPredicate(t *testing.T) {
	web := labelsToKV(map[string]string{"app": "web", "env": "prod"})
	db := labelsToKV(map[string]string{"app": "db"})

	for _, tc := range []struct {
		name    string
		sel     *metav1.LabelSelector
		matches map[string]bool // label set name -> want Match
	}{
		{
			name:    "empty selector matches everything",
			sel:     &metav1.LabelSelector{},
			matches: map[string]bool{"web": true, "db": true},
		},
		{
			name:    "matchLabels",
			sel:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			matches: map[string]bool{"web": true, "db": false},
		},
		{
			name: "matchLabels are ANDed",
			sel: &metav1.LabelSelector{MatchLabels: map[string]string{
				"app": "web",
				"env": "staging",
			}},
			matches: map[string]bool{"web": false, "db": false},
		},
		{
			name: "In",
			sel: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
				Key: "app", Operator: metav1.LabelSelectorOpIn, Values: []string{"web", "cache"},
			}}},
			matches: map[string]bool{"web": true, "db": false},
		},
		{
			name: "NotIn also matches when the key is absent",
			sel: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
				Key: "env", Operator: metav1.LabelSelectorOpNotIn, Values: []string{"staging"},
			}}},
			matches: map[string]bool{"web": true, "db": true},
		},
		{
			name: "Exists",
			sel: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
				Key: "env", Operator: metav1.LabelSelectorOpExists,
			}}},
			matches: map[string]bool{"web": true, "db": false},
		},
		{
			name: "DoesNotExist",
			sel: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
				Key: "env", Operator: metav1.LabelSelectorOpDoesNotExist,
			}}},
			matches: map[string]bool{"web": false, "db": true},
		},
		{
			name: "matchLabels and matchExpressions are ANDed",
			sel: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "web"},
				MatchExpressions: []metav1.LabelSelectorRequirement{{
					Key: "env", Operator: metav1.LabelSelectorOpIn, Values: []string{"prod"},
				}},
			},
			matches: map[string]bool{"web": true, "db": false},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := selectorToPred(tc.sel)
			if err != nil {
				t.Fatalf("NewPredicate: %v", err)
			}
			for name, want := range tc.matches {
				labels := web
				if name == "db" {
					labels = db
				}
				if got := p.Match(&labels); got != want {
					t.Errorf("Match(%s) = %v, want %v", name, got, want)
				}
			}
		})
	}
}

func TestNewPredicate_Nil(t *testing.T) {
	p, err := selectorToPred(nil)
	if err != nil {
		t.Fatalf("NewPredicate(nil): %v", err)
	}
	if p != nil {
		t.Errorf("NewPredicate(nil) = %v, want nil (an omitted selector is not an empty one)", p)
	}
}

func TestNewPredicate_UnsupportedOperator(t *testing.T) {
	sel := &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
		Key: "app", Operator: "Bogus", Values: []string{"web"},
	}}}
	if _, err := selectorToPred(sel); err == nil {
		t.Fatalf("NewPredicate: want error for unsupported operator, got nil")
	}
}
