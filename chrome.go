package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// headline says in one line what is happening and what to do next.
func (m model) headline() string {
	switch {
	case m.busy == "launching":
		for _, r := range m.lrows {
			if r.Status == "trying" {
				return pill(fmt.Sprintf("LAUNCHING: trying %s in %s at %s/h", r.Type, r.Region, usd(r.Price)), cBg, cYellow)
			}
		}
		return pill("LAUNCHING: checking regions and prices", cBg, cYellow)
	case m.busy != "":
		return pill(strings.ToUpper(m.busy)+"...", cBg, cYellow)
	case m.lastErr != "" && m.inst == nil:
		return pill("FAILED: "+truncate(m.lastErr, max(20, m.w/2)), cFg, cRed)
	case m.inst == nil:
		return pill("NO SERVER. Press l to launch", cFg, cDim)
	case m.inst.State == "shutting-down":
		return pill("SHUTTING DOWN", cBg, cYellow)
	case m.met != nil && m.met.SpotWarn == 1:
		return pill("SPOT INTERRUPTION: AWS is reclaiming the server", cFg, cRed)
	case m.met != nil && m.met.Phase == "ready":
		return pill(fmt.Sprintf("READY. Connect Draw Things to 127.0.0.1:%d", m.cfg.LocalPort), cBg, cGreen)
	}
	step := bootStep(m.met)
	txt := fmt.Sprintf("BOOTING %d/%d: ", step+1, len(bootSteps))
	if m.met == nil {
		txt += "instance " + m.inst.State + ", waiting for SSH"
	} else {
		txt += m.met.stepText()
	}
	if left := m.bootLeft(); left != "" {
		txt += ", " + left
	}
	return pill(txt, cBg, cYellow)
}

func usd(p float64) string {
	if p <= 0 {
		return "$?"
	}
	return fmt.Sprintf("$%.2f", p)
}

// costInfo is the short status beside the headline: where, what, and what it costs.
func (m model) costInfo() string {
	if m.inst == nil {
		s := m.cfg.InstanceType + " " + market(m.cfg.Spot)
		if p := m.effPrice(m.cfg.InstanceType); p > 0 {
			s += fmt.Sprintf(" about $%.2f/h", p)
		}
		return s + "  profile " + onelse(m.cfg.Profile, "custom")
	}
	in := m.inst
	s := fmt.Sprintf("%s %s  %s %s", in.Region, in.Zone, in.Type, market(in.Spot))
	if px := m.priceFor(in); px > 0 {
		s += fmt.Sprintf("  $%.2f so far at $%.2f/h", px*time.Since(in.Launch).Hours(), px)
	}
	if mt := m.met; mt != nil && mt.Phase == "ready" && mt.IdleLimit > 0 {
		s += "  auto-off in " + dur(mt.IdleLimit-mt.Idle)
	}
	return s
}

// keyBar lists only the keys that do something right now.
func (m model) keyBar() string {
	var b strings.Builder
	add := func(key, d string) { b.WriteString(sKey.Render(key) + sDim.Render(" "+d+"  ")) }
	if m.editing {
		add("enter", "save")
		add("esc", "cancel")
		return b.String()
	}
	free := m.busy == ""
	down := m.inst != nil && m.inst.State == "shutting-down"
	switch {
	case m.stop != nil:
		add("c", "cancel launch")
	case free && m.inst == nil:
		add("l", "launch")
	case free && down:
		add("l", "force relaunch")
		add("d", "detach volume")
	case free:
		add("x", "terminate")
	}
	if m.sIP != "" && m.met != nil && m.met.Phase == "ready" {
		add("e", "reset idle")
	}
	if free && m.inst != nil && m.inst.State == "running" {
		add("g", "diagnostics")
	}
	switch m.tab {
	case tabConfig:
		add("up/down", "select")
		add("left/right", "change")
		add("enter", "edit")
	case tabModels:
		if free {
			add("u", "upload missing")
		}
		if free && !down && m.unusedVols() > 0 {
			add("d", "delete unused volumes")
		}
	case tabConnect:
		add("i", "allow my IP")
		add("a/A", "add SSH/direct IP")
		add("backspace", "remove")
	case tabLog:
		add("up/down pgup/pgdn home/end", "scroll")
		add("f", "filter: "+logFilters[m.logFilt].name)
	}
	add("p", "profile")
	add("r", "refresh")
	add("tab", "switch")
	add("q", "quit, server keeps running")
	return b.String()
}

// confirmLine is the one place a "press again" prompt appears, whatever the tab.
func (m model) confirmLine() string {
	t := map[string]string{
		"terminate": "Press x again to TERMINATE the server. Any other key cancels.",
		"relaunch":  "Press l again to FORCE-DETACH the volume and launch a new server. Any other key cancels.",
		"detach":    "Press d again to FORCE-DETACH the model volume. Any other key cancels.",
		"volume":    fmt.Sprintf("Press d again to DELETE %d unused volume(s). Any other key cancels.", m.unusedVols()),
	}[m.confirm]
	if t == "" {
		return ""
	}
	return pill(t, cFg, cRed) + "\n"
}

// launchPanel is the live launch table: one row per instance type and region.
func (m model) launchPanel(w int) string {
	rows := append([]launchRow{}, m.lrows...)
	typeIdx := map[string]int{}
	for _, r := range m.lrows {
		if _, ok := typeIdx[r.Type]; !ok {
			typeIdx[r.Type] = len(typeIdx)
		}
	}
	rank := func(r launchRow) int {
		switch {
		case r.Status == "not offered" || r.Status == "error":
			return 2
		case r.Price == 0:
			return 1
		}
		return 0
	}
	sort.SliceStable(rows, func(a, b int) bool {
		ra, rb := rows[a], rows[b]
		if typeIdx[ra.Type] != typeIdx[rb.Type] {
			return typeIdx[ra.Type] < typeIdx[rb.Type]
		}
		if rank(ra) != rank(rb) {
			return rank(ra) < rank(rb)
		}
		return ra.Price < rb.Price
	})
	l := []string{sDim.Render(fmt.Sprintf("%-14s %-14s %9s  %-13s %s", "type", "region", "$/h", "status", "detail"))}
	for _, r := range rows {
		st := sDim
		switch r.Status {
		case "launched":
			st = sOK
		case "trying":
			st = sWarn.Bold(true)
		case "no capacity", "failed", "setup failed", "error":
			st = sBad
		case "queued":
			st = lipgloss.NewStyle().Foreground(cFg)
		}
		price := "-"
		if r.Price > 0 {
			price = fmt.Sprintf("%.4f", r.Price)
		}
		l = append(l, fmt.Sprintf("%-14s %-14s %9s  %s %s", r.Type, r.Region, price, st.Render(fmt.Sprintf("%-13s", r.Status)),
			sDim.Render(truncate(r.Detail, max(10, w-60)))))
	}
	if m.stop != nil {
		l = append(l, "", sDim.Render("Cheapest region first. Press c to cancel."))
	}
	return panel("Launch", w, strings.Join(l, "\n"), cYellow)
}

// stepper shows the boot stages with the current one highlighted.
func (m model) stepper() string {
	cur := bootStep(m.met)
	var parts []string
	for i, s := range bootSteps {
		switch {
		case i < cur:
			parts = append(parts, sOK.Render("✓ "+s))
		case i == cur:
			parts = append(parts, sWarn.Bold(true).Render("● "+s))
		default:
			parts = append(parts, sDim.Render("○ "+s))
		}
	}
	return strings.Join(parts, sDim.Render(" > "))
}

func short(secs int) string {
	if secs >= 60 {
		return fmt.Sprintf("%dm%02ds", secs/60, secs%60)
	}
	return fmt.Sprintf("%ds", secs)
}

// bootLeft is the expected time to ready, from past boots of the same kind.
func (m model) bootLeft() string {
	if m.inst == nil {
		return ""
	}
	eta := bootETA(m.boots, m.bootWarm())
	if eta == 0 {
		return ""
	}
	left := eta - int(time.Since(m.inst.Launch).Seconds())
	if left <= 0 {
		return "any moment now"
	}
	return "about " + short(left) + " left"
}

func (m model) etaLine() string {
	kind := onoff(m.bootWarm(), "warm", "cold")
	if bootETA(m.boots, m.bootWarm()) == 0 {
		return "No " + kind + " boot measured yet. The time is learned after the first one."
	}
	return fmt.Sprintf("%s. Expected from recent %s boots (median).", m.bootLeft(), kind)
}

// bootHistory sums up measured boot times for the no-server screen.
func (m model) bootHistory() string {
	w, c := bootETA(m.boots, true), bootETA(m.boots, false)
	switch {
	case w > 0 && c > 0:
		return fmt.Sprintf("Recent boots took about %s warm and %s cold (copying models from S3).", short(w), short(c))
	case w > 0:
		return fmt.Sprintf("Recent warm boots took about %s. A cold boot also copies the models from S3.", short(w))
	case c > 0:
		return fmt.Sprintf("Recent cold boots took about %s. A warm model volume skips the copy.", short(c))
	}
	return "Boot times are measured from the first launch on. A warm model volume skips the model copy."
}

var logFilters = []struct {
	name string
	keep func(string) bool
}{
	{"all", func(string) bool { return true }},
	{"this Mac", func(l string) bool { return !isServerLine(l) }},
	{"server", isServerLine},
	{"problems", isProblem},
}

func isServerLine(l string) bool {
	return strings.Contains(l, "[server]") || strings.Contains(l, " server: ")
}

func isProblem(l string) bool {
	for _, w := range []string{"FAILED", "failed", "error", "ERROR", "WARNING", "refusing", "could not", "cannot", "no capacity", "Insufficient"} {
		if strings.Contains(l, w) {
			return true
		}
	}
	return false
}

func (m model) filteredLog() []string {
	keep := logFilters[m.logFilt].keep
	var out []string
	for _, l := range m.log {
		if keep(l) {
			out = append(out, l)
		}
	}
	return out
}
