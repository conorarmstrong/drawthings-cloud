package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// bootRec is one measured boot: launch to ready. Warm means the models were already on the volume.
type bootRec struct {
	At   time.Time `json:"at"`
	Secs int       `json:"secs"`
	Warm bool      `json:"warm"`
	Type string    `json:"type"`
}

func bootsPath(root string) string { return filepath.Join(root, ".state", "boots.jsonl") }

func loadBoots(root string) []bootRec {
	b, err := os.ReadFile(bootsPath(root))
	if err != nil {
		return nil
	}
	var out []bootRec
	for _, ln := range strings.Split(string(b), "\n") {
		var r bootRec
		if json.Unmarshal([]byte(ln), &r) == nil && r.Secs > 0 {
			out = append(out, r)
		}
	}
	return out
}

// coldSyncSecs: a boot that spent longer than this copying models counts as cold.
const coldSyncSecs = 30

// trackBoot times the copy phase and records the boot once the server is ready.
func (m *model) trackBoot(mt Metrics) {
	if m.inst == nil {
		return
	}
	if mt.Phase != "ready" {
		m.sawBoot = true
	}
	switch {
	case mt.Phase == "syncing" && m.syncFrom.IsZero():
		m.syncFrom = time.Now()
	case mt.Phase != "syncing" && !m.syncFrom.IsZero():
		m.syncSecs += int(time.Since(m.syncFrom).Seconds())
		m.syncFrom = time.Time{}
	}
	if mt.Phase != "ready" || !m.sawBoot || !m.once(m.inst.ID+"boot") {
		return
	}
	r := bootRec{At: time.Now(), Secs: int(time.Since(m.inst.Launch).Seconds()), Warm: m.syncSecs < coldSyncSecs, Type: m.inst.Type}
	m.boots = append(m.boots, r)
	m.logf("boot took %s (%s)", dur(r.Secs), onoff(r.Warm, "warm, models already on the volume", "cold, models copied from S3"))
	if b, err := json.Marshal(r); err == nil {
		_ = os.MkdirAll(filepath.Dir(bootsPath(m.root)), 0o755)
		if f, err := os.OpenFile(bootsPath(m.root), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			_, _ = f.Write(append(b, '\n'))
			f.Close()
		}
	}
}

// bootWarm guesses whether this boot is warm: a volume exists and the copy has not run long.
func (m model) bootWarm() bool {
	if !m.cfg.ModelVolume.Enabled || len(m.vols) == 0 {
		return false
	}
	syncing := m.syncSecs
	if !m.syncFrom.IsZero() {
		syncing += int(time.Since(m.syncFrom).Seconds())
	}
	return syncing < coldSyncSecs
}

// bootETA is the median of the last five boots of the same kind, 0 when there is no history.
func bootETA(boots []bootRec, warm bool) int {
	var s []int
	for i := len(boots) - 1; i >= 0 && len(s) < 5; i-- {
		if boots[i].Warm == warm {
			s = append(s, boots[i].Secs)
		}
	}
	if len(s) == 0 {
		return 0
	}
	sort.Ints(s)
	return s[len(s)/2]
}

// bootSteps are the stages shown in the boot stepper.
var bootSteps = []string{"instance", "setup", "models", "server", "ready"}

// bootStep is the index into bootSteps of where the server is now.
func bootStep(mt *Metrics) int {
	if mt == nil {
		return 0
	}
	switch mt.Phase {
	case "booting", "attaching":
		return 1
	case "syncing":
		return 2
	case "starting":
		return 3
	case "ready":
		return 4
	}
	return 1
}
