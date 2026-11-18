package main

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestZoneHint(t *testing.T) {
	msg := "InsufficientInstanceCapacity: We currently do not have sufficient g7e.2xlarge capacity in the Availability Zone you requested (us-east-1a). Our system will be working on provisioning additional capacity. You can currently get g7e.2xlarge capacity by not specifying an Availability Zone in your request or choosing us-east-1b, us-east-1d."
	if got := zoneHint(msg); got != "us-east-1b" {
		t.Fatalf("hint %q", got)
	}
	if zoneHint("InsufficientInstanceCapacity: no capacity") != "" {
		t.Fatal("hint from a message without one")
	}
}

func TestOrderByPrice(t *testing.T) {
	got := orderByPrice([]string{"a", "b", "c", "d"}, []float64{2.5, 0, 1.2, 1.9})
	if strings.Join(got, ",") != "c,d,a,b" {
		t.Fatalf("order %v", got)
	}
}

func TestRenderUserdata(t *testing.T) {
	c := defaultConfig()
	fillDefaults(&c)
	ud := renderUserdata(c, "auto")
	if strings.Contains(ud, "@@") {
		i := strings.Index(ud, "@@")
		t.Fatalf("placeholder left: %q", ud[i:i+30])
	}
	if !strings.Contains(ud, c.Bucket) || !strings.HasPrefix(ud, "#!") {
		t.Fatal("userdata not filled in")
	}
}

func TestReaperZip(t *testing.T) {
	b, err := reaperZip()
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil || len(zr.File) != 1 || zr.File[0].Name != "handler.py" {
		t.Fatalf("zip %v %v", err, zr)
	}
}

func TestBootETA(t *testing.T) {
	bs := []bootRec{{Secs: 300, Warm: false}, {Secs: 100, Warm: true}, {Secs: 120, Warm: true}, {Secs: 90, Warm: true}}
	if got := bootETA(bs, true); got != 100 {
		t.Fatalf("warm eta %d", got)
	}
	if got := bootETA(bs, false); got != 300 {
		t.Fatalf("cold eta %d", got)
	}
	if bootETA(nil, true) != 0 {
		t.Fatal("eta without history")
	}
}

func TestKeyBarAndHeadline(t *testing.T) {
	m := newModel()
	m.wiz = nil // tests do not depend on a local config.json
	m.root = t.TempDir()
	m, _ = m.update(tea.WindowSizeMsg{Width: 140, Height: 40})
	plain := func(s string) string { return stripANSI(s) }
	if kb := plain(m.keyBar()); !strings.Contains(" "+kb, " l launch") || strings.Contains(kb, "x terminate") || strings.Contains(kb, "c cancel") {
		t.Fatalf("no-server keys: %s", kb)
	}
	if h := plain(m.headline()); !strings.Contains(h, "NO SERVER") {
		t.Fatalf("headline %s", h)
	}
	m.busy, m.stop = "launching", func() {}
	m.lrows = []launchRow{{Type: "g7e.2xlarge", Region: "us-east-2", Price: 1.82, Status: "trying"}}
	if h := plain(m.headline()); !strings.Contains(h, "trying g7e.2xlarge in us-east-2 at $1.82/h") {
		t.Fatalf("launch headline %s", h)
	}
	if kb := plain(m.keyBar()); !strings.Contains(kb, "c cancel launch") || strings.Contains(" "+kb, " l launch") {
		t.Fatalf("launch keys: %s", kb)
	}
	m.busy, m.stop = "", nil
	m.inst = &Instance{ID: "i-1", State: "running", Type: "g7e.2xlarge", Region: "us-east-2", Zone: "us-east-2a", Launch: time.Now().Add(-time.Minute)}
	if kb := plain(m.keyBar()); !strings.Contains(kb, "x terminate") || strings.Contains(" "+kb, " l launch") {
		t.Fatalf("running keys: %s", kb)
	}
	if h := plain(m.headline()); !strings.Contains(h, "BOOTING 1/5") {
		t.Fatalf("boot headline %s", h)
	}
	m.inst.State = "shutting-down"
	if kb := plain(m.keyBar()); !strings.Contains(kb, "l force relaunch") || !strings.Contains(kb, "d detach volume") {
		t.Fatalf("shutting-down keys: %s", kb)
	}
	m.confirm = "detach"
	if !strings.Contains(plain(m.View()), "Press d again to FORCE-DETACH") {
		t.Fatal("confirm prompt missing")
	}
}

func TestLogFilterAndScroll(t *testing.T) {
	m := newModel()
	m.wiz = nil // tests do not depend on a local config.json
	m.root = ""
	m.log = []string{"10:00:00 [launch] start", "10:00:01 [server] docker pulled", "10:00:02 launch FAILED: x", "10:00:03 [launch] ok"}
	m.logFilt = 2
	if got := m.filteredLog(); len(got) != 1 || !strings.Contains(got[0], "docker") {
		t.Fatalf("server filter %v", got)
	}
	m.logFilt = 3
	if got := m.filteredLog(); len(got) != 1 || !strings.Contains(got[0], "FAILED") {
		t.Fatalf("problem filter %v", got)
	}
	m.logFilt = 0
	m.tab = tabLog
	m.h = 30
	m, _ = m.key(tea.KeyMsg{Type: tea.KeyUp})
	m, _ = m.key(tea.KeyMsg{Type: tea.KeyUp})
	if m.logOff != 2 {
		t.Fatalf("scroll %d", m.logOff)
	}
	m, _ = m.key(tea.KeyMsg{Type: tea.KeyEnd})
	if m.logOff != 0 {
		t.Fatalf("end %d", m.logOff)
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	esc := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			esc = true
		case esc && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'):
			esc = false
		case !esc:
			b.WriteRune(r)
		}
	}
	return b.String()
}
