package main

import "testing"

func TestNormalizeCIDR(t *testing.T) {
	ok := map[string]string{"1.2.3.4": "1.2.3.4/32", " 10.0.0.0/24 ": "10.0.0.0/24", "203.0.113.7/32": "203.0.113.7/32"}
	for in, want := range ok {
		if got, err := normalizeCIDR(in); err != nil || got != want {
			t.Errorf("%q: got %q err %v want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "abc", "0.0.0.0/0", "10.0.0.0/8", "::1", "1.2.3.400"} {
		if _, err := normalizeCIDR(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestRuleCovers(t *testing.T) {
	rules := []Rule{{"203.0.113.7/32", 22}, {"5.5.5.0/24", 7859}}
	if !ruleCovers(rules, "203.0.113.7", 22) || ruleCovers(rules, "203.0.113.7", 7859) || !ruleCovers(rules, "5.5.5.9", 7859) || ruleCovers(rules, "6.6.6.6", 22) {
		t.Error("ruleCovers wrong")
	}
}
