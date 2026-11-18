package main

import (
	"os"
	"testing"
)

// DT_LIVE=1 go test -run TestLivePrices
// Regression test: with several candidate types, types and spot prices must all load (they used to come back empty).
func TestLivePrices(t *testing.T) {
	if os.Getenv("DT_LIVE") == "" {
		t.Skip("set DT_LIVE=1")
	}
	c := loadConfig("config.json")
	c.Active = "us-east-2"
	types, err := fetchTypes(c)
	if err != nil || len(types) < 2 {
		t.Fatalf("fetchTypes: %d types, err %v", len(types), err)
	}
	var names []string
	for _, ti := range types {
		names = append(names, ti.Type)
	}
	spots := fetchSpot(c, names)
	if spots["g7e.2xlarge"] <= 0 {
		t.Fatalf("no spot price for g7e.2xlarge: %v", spots)
	}
	if p, err := fetchPrice(c, "g7e.2xlarge"); err != nil || p <= 0 {
		t.Fatalf("fetchPrice: %v %v", p, err)
	}
	t.Logf("%d types, spot g7e.2xlarge $%.4f", len(types), spots["g7e.2xlarge"])
}
