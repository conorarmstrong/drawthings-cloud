package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func (m model) fit(t TypeInfo) (string, lipgloss.Style) {
	_, total, _ := m.requiredBytes()
	need := float64(total) / (1 << 30)
	switch {
	case need == 0:
		return "unknown", sDim
	case t.VRAMGiB >= need*0.9:
		return "fits", sOK
	case t.VRAMGiB >= need*0.6:
		return "tight, may run out of VRAM", sWarn
	}
	return "too small", sBad
}

func (m model) View() string {
	if m.w == 0 {
		return "loading..."
	}
	if m.wiz != nil {
		return m.viewWizard()
	}
	head := sTitle.Render(" Draw Things on AWS ") + m.headline()
	if room := m.w - lipgloss.Width(head) - 2; room > 10 {
		head += sDim.Render("  " + truncate(m.costInfo(), room))
	}
	var tabs []string
	for i, n := range tabNames {
		if tab(i) == m.tab {
			tabs = append(tabs, pill(n, cBg, cPurple))
		} else {
			tabs = append(tabs, sDim.Render(" "+n+" "))
		}
	}
	var body string
	switch m.tab {
	case tabDash:
		body = m.viewDash()
	case tabConfig:
		body = m.viewConfig()
	case tabModels:
		body = m.viewModels()
	case tabLog:
		body = m.viewLog()
	default:
		body = m.viewConnect()
	}
	return head + "\n" + strings.Join(tabs, "") + "\n\n" + body + "\n" + m.confirmLine() + lipgloss.NewStyle().Width(m.w).Render(m.keyBar())
}

func (m model) spendLine() string {
	s := fmt.Sprintf("$%.2f this month (estimate)", m.spend())
	st := sOK
	if b := m.cfg.MonthlyBudgetUSD; b > 0 {
		s += fmt.Sprintf(" of $%.0f budget", b)
		if m.spend() >= b {
			st = sBad
		}
	}
	return st.Render(s)
}

func (m model) volumeSummary() string {
	switch {
	case !m.cfg.ModelVolume.Enabled:
		return sDim.Render("off, models sync from S3 each launch")
	case m.volErr != "":
		return sBad.Render(m.volErr)
	case !m.volDone:
		return sDim.Render("checking...")
	case len(m.vols) == 0:
		return sWarn.Render(fmt.Sprintf("none yet, created on first launch (%d GB per zone)", m.cfg.ModelVolume.SizeGB))
	}
	var parts []string
	for _, v := range m.vols {
		parts = append(parts, fmt.Sprintf("%s %dGB %s %s", v.ID, v.SizeGB, v.AZ, v.State))
	}
	return sOK.Render(strings.Join(parts, "; "))
}

func (m model) viewDash() string {
	w := m.w - 2
	half := w/2 - 2
	if m.inst == nil {
		price := m.effPrice(m.cfg.InstanceType)
		_, total, miss := m.requiredBytes()
		l := []string{sBold.Render("No server running.") + "  Press " + sKey.Render("l") + " to launch.", ""}
		if m.busy == "launching" {
			l[0] = sBold.Render("Launching.") + " The table below shows each region as it is tried."
		}
		l = append(l, fmt.Sprintf("Type      %s", m.cfg.InstanceType))
		if price > 0 {
			kind := onoff(m.cfg.Spot && m.spots[m.cfg.InstanceType] > 0, "spot (current, varies)", "on-demand")
			l = append(l, fmt.Sprintf("Price     %s", sWarn.Render(fmt.Sprintf("$%.2f / hour %s", price, kind))))
		}
		l = append(l,
			fmt.Sprintf("Models    %s", m.modelsSummary(total, miss)),
			fmt.Sprintf("Volume    %s", m.volumeSummary()),
			fmt.Sprintf("Shutdown  after %d min idle, hard cap %d h", m.cfg.IdleMin, m.cfg.MaxHours),
			fmt.Sprintf("SSH from  %s", strings.Join(m.cfg.AllowedCIDRs, ", ")),
			fmt.Sprintf("Spend     %s", m.spendLine()),
			"", sDim.Render(m.bootHistory()))
		if m.instErr != "" {
			l = append(l, "", sBad.Render("AWS: "+m.instErr))
		}
		out := panel("Server", w, strings.Join(l, "\n"), cBlue)
		if len(m.lrows) > 0 {
			out += "\n" + m.launchPanel(w)
		}
		return out + "\n" + m.logPanel(w)
	}
	in := m.inst
	up := int(time.Since(in.Launch).Seconds())
	px := m.priceFor(in)
	l := []string{
		fmt.Sprintf("Instance  %s  %s  %s", in.ID, in.Type, onoff(in.Spot, "spot", "on-demand")),
		fmt.Sprintf("Where     %s  %s", sBold.Render(onelse(in.Region, "?")), onelse(in.Zone, "zone unknown")),
		fmt.Sprintf("State     %s   IP %s", in.State, in.IP),
		fmt.Sprintf("Uptime    %s", dur(up)),
	}
	if px > 0 {
		l = append(l, fmt.Sprintf("Cost      %s  %s", sWarn.Render(fmt.Sprintf("$%.2f", px*float64(up)/3600)), sDim.Render(fmt.Sprintf("at $%.2f/h", px))))
	}
	l = append(l, fmt.Sprintf("Spend     %s", m.spendLine()))
	tun := onelse(m.stream, "starting")
	col := onoffStyle(m.stream == "connected")
	l = append(l, fmt.Sprintf("Tunnel    %s  %s", col.Render(tun), sDim.Render(fmt.Sprintf("localhost:%d", m.cfg.LocalPort))))
	if m.cfg.WarnAfterHours > 0 && float64(up)/3600 > float64(m.cfg.WarnAfterHours) {
		l = append(l, sBad.Render(fmt.Sprintf("Up over %d h. Still needed?", m.cfg.WarnAfterHours)))
	}
	if m.met != nil && m.met.SpotWarn == 1 {
		l = append(l, "", sBad.Render("SPOT INTERRUPTION: AWS is reclaiming this server."), sBad.Render(truncate(m.met.SpotInfo, half-4)), sDim.Render("Press l after it ends to relaunch."))
	}
	left := panel("Server", half, strings.Join(l, "\n"), cBlue)
	if m.inst.State == "shutting-down" {
		left += "\n" + sWarn.Render(" Still shutting down. Press l twice to force-detach the model volume and launch a new server now, or d twice to only detach it.")
	}
	launch := ""
	if m.busy == "launching" && len(m.lrows) > 0 {
		launch = m.launchPanel(w) + "\n"
	}
	if m.met == nil {
		body := []string{sBad.Render("NOT READY. Do not connect yet."), "", m.stepper(), sDim.Render(m.etaLine()), "",
			sWarn.Render("Instance is " + m.inst.State + ". Waiting for SSH and the metrics stream."), sDim.Render(m.stream)}
		right := panel("Metrics", half, strings.Join(body, "\n"), cDim)
		return lipgloss.JoinHorizontal(lipgloss.Top, left, right) + "\n" + launch + m.logPanel(w)
	}
	mt := m.met
	var r []string
	bw := max(10, half-34)
	if mt.Phase == "ready" {
		r = append(r, sOK.Render(fmt.Sprintf("READY. Connect Draw Things to 127.0.0.1 port %d", m.cfg.LocalPort)))
	} else {
		r = append(r, sBad.Render("NOT READY. Do not connect yet."), m.stepper(), sWarn.Render(mt.stepText()), sDim.Render(m.etaLine()))
		if mt.Phase == "syncing" && mt.FilesTotal > 0 {
			r = append(r, fmt.Sprintf("Files  %d of %d copied", mt.FilesDone, mt.FilesTotal))
		}
	}
	r = append(r, sDim.Render("Phase  ")+sBold.Render(mt.Phase))
	if mt.Phase == "syncing" || mt.Phase == "booting" || mt.Phase == "attaching" {
		_, total, _ := m.requiredBytes()
		if total > 0 {
			p := float64(mt.ModelsBytes) * 100 / float64(total)
			r = append(r, gauge("sync", p, bw, gib(mt.ModelsBytes)+" / "+gib(total)))
		}
	}
	vp, rp := 0.0, 0.0
	if mt.VRAMTotal > 0 {
		vp = mt.VRAM * 100 / mt.VRAMTotal
	}
	if mt.RAMTotal > 0 {
		rp = mt.RAM * 100 / mt.RAMTotal
	}
	if m.inst != nil {
		r = append(r, fmt.Sprintf("Where  %s  %s", sBold.Render(onelse(m.inst.Region, "?")), onelse(m.inst.Zone, "zone unknown")))
	}
	r = append(r,
		gauge("GPU", float64(mt.GPU), bw, fmt.Sprintf("%.0f C  %.0f W", mt.Temp, mt.Power)),
		gauge("VRAM", vp, bw, fmt.Sprintf("%.1f / %.1f GiB", mt.VRAM/1024, mt.VRAMTotal/1024)),
		gauge("RAM", rp, bw, fmt.Sprintf("%.1f / %.1f GiB", mt.RAM/1024, mt.RAMTotal/1024)),
		fmt.Sprintf("Load   %.2f   Disk %d GB   Clients %d", mt.Load, mt.DiskGB, mt.Conns),
		"", sDim.Render("GPU  ")+spark(m.hist, bw+10), sDim.Render("VRAM ")+spark(m.vhist, bw+10), "")
	if m.loading {
		r = append(r, sWarn.Render("Loading models from disk onto the GPU. Generation starts when this finishes."))
		sp := fmt.Sprintf("%.0f MiB/s", m.ldRate)
		if mt.DiskRead > 0 {
			sp += fmt.Sprintf("  disk %.0f MiB/s", mt.DiskRead)
		}
		if m.ldTarget > mt.VRAM {
			p := min(mt.VRAM*100/m.ldTarget, 99)
			eta := ""
			if m.ldRate > 5 {
				eta = "  ETA " + dur(int((m.ldTarget-mt.VRAM)/m.ldRate))
			}
			r = append(r, gauge("To GPU", p, bw, fmt.Sprintf("%.1f / %.1f GiB  %s%s", mt.VRAM/1024, m.ldTarget/1024, sp, eta)))
		} else {
			r = append(r, fmt.Sprintf("To GPU %s", sDim.Render(fmt.Sprintf("%.1f GiB loaded  %s  (no total yet, learned after the first job)", mt.VRAM/1024, sp))))
		}
		r = append(r, "")
	}
	if m.stage != "" {
		r = append(r, fmt.Sprintf("Stage  %s %s", sBold.Render(m.stage), sDim.Render("for "+dur(int(time.Since(m.stageT).Seconds())))))
		r = append(r, sDim.Render(fmt.Sprintf("       server CPU %.0f%%   net out %.0f MiB/s   disk read %.0f MiB/s", mt.SrvCPU, mt.NetTx, mt.DiskRead)))
	}
	if m.lastWin != "" {
		r = append(r, sDim.Render("Last   "+truncate(m.lastWin, half-12)))
	}
	switch {
	case m.loading:
		r = append(r, "Job    "+sWarn.Render("waiting for models to load, "+dur(int(time.Since(m.ldStart).Seconds()))))
	case m.jobOn:
		r = append(r, "Job    "+sOK.Render("generating for "+dur(int(time.Since(m.jobStart).Seconds())))+sDim.Render("  (inferred from GPU use)"))
	case m.jobs > 0:
		r = append(r, "Job    "+sDim.Render(fmt.Sprintf("idle. Last took %s. %d this session", dur(int(m.lastJob.Seconds())), m.jobs)))
	default:
		r = append(r, "Job    "+sDim.Render("none yet"))
	}
	ip := 0.0
	if mt.IdleLimit > 0 {
		ip = float64(mt.Idle) * 100 / float64(mt.IdleLimit)
	}
	if mt.Phase == "ready" {
		r = append(r, fmt.Sprintf("Idle   %s %s", bar(ip, bw, heat(ip)), sDim.Render("auto-off in "+dur(mt.IdleLimit-mt.Idle))))
	}
	r = append(r, fmt.Sprintf("Cap    %s", sDim.Render("hard stop in "+dur(mt.Max-mt.Up))))
	if mt.Log != "" {
		r = append(r, "", sDim.Render("log: "+truncate(mt.Log, half-8)))
	}
	right := panel("Live metrics", half, strings.Join(r, "\n"), cGreen)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, right) + "\n" + launch + m.logPanel(w)
}

func onoffStyle(ok bool) lipgloss.Style {
	if ok {
		return sOK
	}
	return sWarn
}

func truncate(s string, n int) string {
	if n > 3 && len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}

func (m model) modelsSummary(total int64, miss int) string {
	switch {
	case m.s3 == nil && m.s3Err != "":
		return sBad.Render("S3 error: " + m.s3Err)
	case m.s3 == nil:
		return sDim.Render("checking S3...")
	case miss > 0:
		return sBad.Render(fmt.Sprintf("%d missing or wrong size in S3", miss))
	}
	return sOK.Render("all present in S3 ") + sDim.Render(gib(total))
}

func (m model) logPanel(w int) string {
	l := m.log
	if len(l) > 10 {
		l = l[len(l)-10:]
	}
	if len(l) == 0 {
		l = []string{sDim.Render("(no activity yet)")}
	}
	return panel("Activity", w, strings.Join(l, "\n"), cDim)
}

func (m model) viewConfig() string {
	w := m.w - 2
	fs := m.fields()
	var rows []string
	for i, f := range fs {
		v := f.get(m)
		if m.editing && m.editKind == "cfg" && i == m.cursor {
			v = m.input.View()
		}
		line := fmt.Sprintf("%-25s %s", f.name, v)
		if i == m.cursor {
			line = lipgloss.NewStyle().Foreground(cPurple).Bold(true).Render("> " + line)
		} else {
			line = "  " + line
		}
		rows = append(rows, line)
	}
	rows = append(rows, "", sDim.Render(fs[m.cursor].help),
		sDim.Render("up/down select, left/right change, enter edit, esc cancel. Everything saves to config.json (more options live there). Applies to the next launch."))
	out := panel("Settings", w, strings.Join(rows, "\n"), cPurple)

	t := []string{sDim.Render(fmt.Sprintf("%-14s %-22s %8s %7s %10s %9s  %s", "type", "gpu", "vram", "ram", "on-dem $/h", "spot $/h", "fit for your models"))}
	for _, ti := range m.types {
		fit, st := m.fit(ti)
		pr, sp := "...", "..."
		if p := m.prices[ti.Type]; p > 0 {
			pr = fmt.Sprintf("%.2f", p)
		}
		if p := m.spots[ti.Type]; p > 0 {
			sp = fmt.Sprintf("%.2f", p)
		}
		line := fmt.Sprintf("%-14s %-22s %5.0f GiB %4.0f GiB %10s %9s  %s", ti.Type, truncate(ti.GPU, 22), ti.VRAMGiB, ti.RAMGiB, pr, sp, st.Render(fit))
		if ti.Type == m.cfg.InstanceType {
			line = lipgloss.NewStyle().Bold(true).Foreground(cFg).Render("> " + line)
		} else {
			line = "  " + line
		}
		t = append(t, line)
	}
	if len(m.types) == 0 {
		t = append(t, sDim.Render("loading instance types for "+m.cfg.reg()+"..."))
	}
	t = append(t, "", sDim.Render("Fit is a rough rule of thumb from total model size. On-demand prices come from the AWS Pricing API, spot is the lowest current zone price."))
	return out + "\n" + panel("Instance types in "+m.cfg.reg(), w, strings.Join(t, "\n"), cBlue)
}

func (m model) viewModels() string {
	w := m.w - 2
	rows := []string{sDim.Render(fmt.Sprintf("%-62s %12s %12s  %s", "file", "local", "s3", "status"))}
	for _, f := range m.cfg.wanted() {
		var ls int64 = -1
		if st, err := os.Stat(filepath.Join(m.cfg.LocalModels, f)); err == nil {
			ls = st.Size()
		}
		ss, ok := m.s3[f]
		status := sOK.Render("ok")
		switch {
		case m.s3 == nil:
			status = sDim.Render("checking")
		case !ok:
			status = sBad.Render("missing in S3")
		case ls >= 0 && ls != ss:
			status = sWarn.Render("size differs (uploading?)")
		case ls < 0:
			status = sWarn.Render("not on this Mac")
		}
		lt, st := "-", "-"
		if ls >= 0 {
			lt = gib(ls)
		}
		if ok {
			st = gib(ss)
		}
		rows = append(rows, fmt.Sprintf("%-62s %12s %12s  %s", truncate(f, 62), lt, st, status))
	}
	_, total, miss := m.requiredBytes()
	rows = append(rows, "", fmt.Sprintf("s3://%s/models/   %s", m.cfg.Bucket, m.modelsSummary(total, miss)))
	if m.s3Err != "" {
		rows = append(rows, sBad.Render(m.s3Err))
	}
	rows = append(rows, sDim.Render("Press u to upload anything missing from "+m.cfg.LocalModels+". r refreshes."))
	title := "Required models"
	if len(m.cfg.ModelSets) > 0 {
		title += "  set: " + onelse(m.cfg.ModelSet, "(none)")
	}
	out := panel(title, w, strings.Join(rows, "\n"), cYellow)

	v := []string{fmt.Sprintf("Model volume  %s", m.volumeSummary()),
		sDim.Render("Keeps models between runs so a launch skips the S3 sync. It is billed monthly while it exists, even with no server."),
		sDim.Render("One volume per zone, created where AWS has capacity. A new zone does one full S3 sync, then boots warm.")}
	if n := m.unusedVols(); n > 0 {
		v = append(v, sDim.Render(fmt.Sprintf("Press d twice to delete the %d unused volume(s). A volume attached to a server is left alone.", n)))
	}
	return out + "\n" + panel("Persistent volume", w, strings.Join(v, "\n"), cCyan) + "\n" + m.logPanel(w)
}

func (m model) viewConnect() string {
	w := m.w - 2
	c := m.cfg
	tun := m.stream
	if m.inst == nil {
		tun = "no server running"
	}
	ip := ""
	if m.inst != nil {
		ip = m.inst.IP
	}
	l := []string{
		sBold.Render("On this Mac (SSH tunnel, encrypted)"),
		"  Keep this TUI open. In Draw Things add a remote server: " + sKey.Render(fmt.Sprintf("127.0.0.1  port %d", c.LocalPort)) + ", TLS off.",
		"  " + sDim.Render("The menu name differs between app versions (look for Remote Server / Machine in Settings)."),
		fmt.Sprintf("  Tunnel: %s", tun),
		"",
		sBold.Render("From a phone or another device (direct, NOT encrypted)"),
	}
	if ip != "" {
		l = append(l, "  Host "+sKey.Render(ip)+"  port "+sKey.Render(fmt.Sprint(c.ServerPort)), "  "+sDim.Render("Add the device's IP below with A first. No tunnel is used, so prompts and images cross the internet in plain text."))
	} else {
		l = append(l, "  "+sDim.Render("Available once a server is running. Add the device's IP below with A."))
	}
	if c.SharedSecret != "" {
		l = append(l, "  Shared secret: "+sKey.Render(c.SharedSecret)+sDim.Render("  (enter it in the app)"))
	}
	out := panel("Connect Draw Things", w, strings.Join(l, "\n"), cCyan)

	r := []string{}
	for i, rl := range m.rules {
		kind := "SSH   "
		if rl.Port != 22 {
			kind = fmt.Sprintf("Direct %d", rl.Port)
		}
		line := fmt.Sprintf("%-10s %s", kind, rl.CIDR)
		if m.myip != "" && ruleCovers([]Rule{rl}, m.myip, rl.Port) {
			line += sDim.Render("   <- this Mac")
		}
		if i == m.rcur {
			line = lipgloss.NewStyle().Foreground(cPurple).Bold(true).Render("> " + line)
		} else {
			line = "  " + line
		}
		r = append(r, line)
	}
	if len(r) == 0 {
		r = append(r, sDim.Render("no rules yet (they are created at the first launch)"))
	}
	if m.rulesErr != "" {
		r = append(r, sBad.Render(m.rulesErr))
	}
	if m.inst != nil && m.myip != "" && !ruleCovers(m.rules, m.myip, 22) {
		r = append(r, "", sBad.Render(fmt.Sprintf("Your current IP %s is not allowed to SSH. Press i to allow it.", m.myip)))
	}
	if m.editing && m.editKind != "cfg" {
		r = append(r, "", sBold.Render("Add "+map[string]string{"ssh": "SSH", "direct": "direct"}[m.editKind]+" IP or CIDR: ")+m.input.View(), sDim.Render("enter to add, esc to cancel"))
	}
	r = append(r, "", sDim.Render("i allow this Mac's current IP   a add an SSH IP   A add a direct (phone) IP   up/down select   backspace remove"),
		sDim.Render("Changes apply to the running server at once. Every launch resets rules to allowed_cidrs in config.json. Phone IPs on mobile data change often."))
	return out + "\n" + panel("Who can connect", w, strings.Join(r, "\n"), cYellow) + "\n" + m.logPanel(w)
}

func (m model) viewLog() string {
	w := m.w - 2
	n := max(8, m.h-10)
	all := m.filteredLog()
	end := max(0, len(all)-m.logOff)
	start := max(0, end-n)
	l := make([]string, 0, n)
	for _, ln := range all[start:end] {
		l = append(l, truncate(ln, w-4))
	}
	if len(l) == 0 {
		l = []string{sDim.Render("(nothing matches this filter yet)")}
	}
	title := fmt.Sprintf("Full log  filter: %s  lines %d-%d of %d", logFilters[m.logFilt].name, start+1, end, len(all))
	if m.logOff > 0 {
		title += "  (scrolled back, end jumps to the newest)"
	}
	return panel(title, w, strings.Join(l, "\n"), cDim) + "\n" +
		sDim.Render(" Everything here is also saved to logs/tui.log. Press g to download the server's timeline, boot and container logs to logs/diag-*/.")
}
