package main

import (
	"fmt"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestRender(t *testing.T) {
	m := newModel()
	m.wiz = nil // tests do not depend on a local config.json
	m.root = "/tmp/none"
	m, _ = m.update(tea.WindowSizeMsg{Width: 130, Height: 40})
	m, _ = m.update(s3Msg{objs: map[string]int64{"custom.json": 5000}})
	m, _ = m.update(typesMsg{t: []TypeInfo{{"g7e.2xlarge", "RTX PRO Server 6000", 96, 8, 64}, {"g6e.2xlarge", "L40S", 44.7, 8, 64}}})
	m, _ = m.update(priceMsg{"g7e.2xlarge", 5.85})
	for _, tb := range []tab{tabDash, tabConfig, tabModels, tabConnect} {
		m.tab = tb
		fmt.Println(m.View())
	}
	m.tab = tabDash
	m.inst = &Instance{ID: "i-0abc", State: "running", IP: "1.2.3.4", Type: "g7e.2xlarge", Launch: time.Now().Add(-40 * time.Minute)}
	mt := Metrics{Phase: "ready", GPU: 93, VRAM: 71000, VRAMTotal: 97887, Temp: 66, Power: 410, Load: 3.2, RAM: 30000, RAMTotal: 64000, Conns: 1, Up: 2400, Idle: 120, IdleLimit: 1200, Max: 21600, DiskGB: 90, Log: "step 23/50"}
	for i := 0; i < 30; i++ {
		mt.GPU = (i * 7) % 100
		m, _ = m.update(metricMsg(mt))
	}
	fmt.Println(m.View())
}
