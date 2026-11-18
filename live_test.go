package main

import (
	"os"
	"testing"
)

// DT_LIVE=1 go test -run TestLiveRules  (touches the real security group with a TEST-NET address)
func TestLiveRules(t *testing.T) {
	if os.Getenv("DT_LIVE") == "" {
		t.Skip("set DT_LIVE=1")
	}
	c := loadConfig("config.json")
	const cidr = "192.0.2.10/32"
	for _, port := range []int{22, c.ServerPort} {
		if out, err := changeRule(c, true, cidr, port); err != nil {
			t.Fatalf("add %d: %s %v", port, out, err)
		}
	}
	rules, err := listRules(c)
	if err != nil || !ruleCovers(rules, "192.0.2.10", 22) || !ruleCovers(rules, "192.0.2.10", c.ServerPort) {
		t.Fatalf("rules not visible: %v %v", rules, err)
	}
	t.Logf("rules: %v", rules)
	for _, port := range []int{22, c.ServerPort} {
		if out, err := changeRule(c, false, cidr, port); err != nil {
			t.Fatalf("remove %d: %s %v", port, out, err)
		}
	}
	rules, _ = listRules(c)
	if ruleCovers(rules, "192.0.2.10", 22) || ruleCovers(rules, "192.0.2.10", c.ServerPort) {
		t.Fatalf("rules still present: %v", rules)
	}
	t.Logf("after cleanup: %v; my ip: %s", rules, publicIP())
}
