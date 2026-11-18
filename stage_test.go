package main

import (
	"strings"
	"testing"
	"time"
)

func TestStageAndBreakdown(t *testing.T) {
	m := &model{root: t.TempDir()}
	m.cfg.GPUIdlePct = 5
	step := func(mt Metrics, n int) {
		mt.Phase = "ready"
		for i := 0; i < n; i++ {
			m.trackStage(mt)
		}
	}
	step(Metrics{}, 3)
	if m.stage != "idle" {
		t.Fatalf("stage %q", m.stage)
	}
	step(Metrics{SrvCPU: 95}, 3)
	if m.stage != "CPU busy (preparing)" || !m.winOn {
		t.Fatalf("stage %q win %v", m.stage, m.winOn)
	}
	m.stageT = m.stageT.Add(-60 * time.Second) // pretend 60 s passed
	step(Metrics{GPU: 100}, 3)
	if m.stage != "computing on the GPU" {
		t.Fatalf("stage %q", m.stage)
	}
	m.stageT = m.stageT.Add(-120 * time.Second)
	step(Metrics{}, 3)
	if m.stage != "idle" {
		t.Fatalf("stage %q", m.stage)
	}
	m.idleFrom = m.idleFrom.Add(-60 * time.Second)
	step(Metrics{}, 1)
	if m.winOn || !strings.Contains(m.lastWin, "CPU busy (preparing) 0:01:0") || !strings.Contains(m.lastWin, "computing on the GPU 0:02:0") {
		t.Fatalf("breakdown %q", m.lastWin)
	}
	// a single blip must not change the stage
	step(Metrics{GPU: 100}, 1)
	step(Metrics{}, 1)
	if m.stage != "idle" {
		t.Fatalf("blip changed stage to %q", m.stage)
	}
}
