package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTrackLoad(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".state"), 0o755)
	m := &model{root: dir}
	m.cfg.GPUIdlePct = 5
	v := 1000.0
	for i := 0; i < 6; i++ {
		m.ldPrevT = m.ldPrevT.Add(-2 * time.Second) // pretend the last sample was 2 s ago
		m.trackLoad(Metrics{Phase: "ready", GPU: 0, VRAM: v})
		v += 1000
	}
	if !m.loading {
		t.Fatalf("expected loading, rate %.0f", m.ldRate)
	}
	m.ldPrevT = m.ldPrevT.Add(-2 * time.Second)
	m.trackLoad(Metrics{Phase: "ready", GPU: 100, VRAM: v})
	if m.loading {
		t.Fatal("expected loading to end when the GPU starts computing")
	}
	b, _ := os.ReadFile(filepath.Join(dir, ".state", "gpu_load_mib"))
	if string(b) != "7000" || m.ldTarget != 7000 {
		t.Fatalf("target not saved: %q %.0f", b, m.ldTarget)
	}
	// a flat VRAM with an idle GPU is not a load
	m2 := &model{root: dir, ldRead: true}
	m2.cfg.GPUIdlePct = 5
	for i := 0; i < 5; i++ {
		m2.ldPrevT = m2.ldPrevT.Add(-2 * time.Second)
		m2.trackLoad(Metrics{Phase: "ready", GPU: 0, VRAM: 20000})
	}
	if m2.loading {
		t.Fatal("flat VRAM should not count as loading")
	}
}
