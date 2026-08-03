package repr

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
