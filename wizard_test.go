package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestWizard(t *testing.T) {
	root, dir := t.TempDir(), t.TempDir()
	for _, f := range []string{"b_model.ckpt", "a_vae.ckpt", "custom.json"} {
		os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644)
	}
	c := defaultConfig()
	fillDefaults(&c)
	m := model{root: root, cfg: c, w: 120, h: 40, ch: make(chan tea.Msg, 8), notified: map[string]bool{}, seen: map[string]string{}}
	m.wiz = newWizard(c)
	typ := func(s string) {
		for _, r := range s {
			m, _ = m.wizardKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		}
	}
	key := func(k tea.KeyType) { m, _ = m.wizardKey(tea.KeyMsg{Type: k}) }

	key(tea.KeyEnter) // empty bucket is refused
	if m.wiz.step != wzBucket || !strings.Contains(m.wiz.msg, "needed") {
		t.Fatalf("empty bucket accepted: step %d %q", m.wiz.step, m.wiz.msg)
	}
	typ("my-bucket")
	key(tea.KeyEnter)
	if !m.wiz.checking || m.cfg.Bucket != "my-bucket" {
		t.Fatal("bucket lookup not started")
	}
	m = m.wizardBucket(bucketRegionMsg{region: "eu-west-2"})
	if m.wiz.step != wzDir || m.cfg.BucketRegion != "eu-west-2" {
		t.Fatalf("after lookup: step %d region %q", m.wiz.step, m.cfg.BucketRegion)
	}
	m.wiz.input.SetValue(dir)
	key(tea.KeyEnter)
	if m.wiz.step != wzModels || len(m.wiz.files) != 3 || m.wiz.files[0].name != "a_vae.ckpt" {
		t.Fatalf("models step: %d %+v", m.wiz.step, m.wiz.files)
	}
	key(tea.KeyEnter) // nothing ticked
	if m.wiz.step != wzModels {
		t.Fatal("accepted no models")
	}
	key(tea.KeySpace)
	key(tea.KeyDown)
	key(tea.KeySpace)
	key(tea.KeyEnter)
	if strings.Join(m.cfg.Models, ",") != "a_vae.ckpt,b_model.ckpt" || m.wiz.step != wzRegions {
		t.Fatalf("models %v step %d", m.cfg.Models, m.wiz.step)
	}
	m.wiz.input.SetValue("us-east-2, eu-west-2")
	key(tea.KeyEnter)
	if strings.Join(m.cfg.Regions, ",") != "us-east-2,eu-west-2" || m.cfg.Region != "us-east-2" {
		t.Fatalf("regions %v", m.cfg.Regions)
	}
	spot := m.cfg.Spot
	key(tea.KeyRight)
	if m.cfg.Spot == spot {
		t.Fatal("spot did not toggle")
	}
	key(tea.KeyEnter)
	if !strings.Contains(stripANSI(m.View()), "Check and save") {
		t.Fatal("summary not shown")
	}
	key(tea.KeyEnter)
	if m.wiz != nil {
		t.Fatal("wizard still open")
	}
	got := loadConfig(filepath.Join(root, "config.json"))
	if got.Bucket != "my-bucket" || got.BucketRegion != "eu-west-2" || len(got.Models) != 2 || got.LocalModels != dir {
		t.Fatalf("saved config %+v", got)
	}
}

func TestWizardBucketLookupFails(t *testing.T) {
	c := defaultConfig()
	fillDefaults(&c)
	m := model{cfg: c, w: 120, h: 40}
	m.wiz = newWizard(c)
	m.wiz.checking = true
	m = m.wizardBucket(bucketRegionMsg{err: os.ErrPermission})
	if m.wiz.step != wzRegion || !strings.Contains(m.wiz.msg, "Type its region") {
		t.Fatalf("step %d %q", m.wiz.step, m.wiz.msg)
	}
}
