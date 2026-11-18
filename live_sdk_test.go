package main

import (
	"os"
	"testing"
)

// DT_LIVE=1 go test -run TestLiveReadOnly -v   (read-only AWS calls through the SDK)
func TestLiveReadOnly(t *testing.T) {
	if os.Getenv("DT_LIVE") == "" {
		t.Skip("set DT_LIVE=1")
	}
	c := loadConfig("config.json")
	in, err := describeInstance(c)
	t.Logf("instance: %+v err=%v", in, err)
	if err != nil {
		t.Fatal(err)
	}
	vols, err := describeVolumes(c)
	t.Logf("volumes: %+v err=%v", vols, err)
	if err != nil {
		t.Fatal(err)
	}
	objs, err := listS3Models(c)
	if err != nil || len(objs) == 0 {
		t.Fatalf("s3: %d objects, %v", len(objs), err)
	}
	t.Logf("s3: %d objects", len(objs))
	c.Browse = "us-east-2"
	rules, err := listRules(c)
	t.Logf("rules in us-east-2: %v err=%v", rules, err)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []string{"eu-west-1", "us-east-2"} {
		ts, err := offeredTypes(r, []string{"g7e.2xlarge"})
		t.Logf("%s offers %v err=%v", r, ts, err)
	}
	t.Logf("public ip %q", publicIP())
}
