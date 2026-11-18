package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

type tab int

const (
	tabDash tab = iota
	tabConfig
	tabModels
	tabConnect
	tabLog
)

var tabNames = []string{"1 Dashboard", "2 Config", "3 Models", "4 Connect", "5 Log"}

type Metrics struct {
	Phase       string  `json:"phase"`
	GPU         int     `json:"gpu"`
	VRAM        float64 `json:"vram"`
	VRAMTotal   float64 `json:"vram_total"`
	Temp        float64 `json:"temp"`
	Power       float64 `json:"power"`
	Load        float64 `json:"load"`
	RAM         float64 `json:"ram"`
	RAMTotal    float64 `json:"ram_total"`
	Conns       int     `json:"conns"`
	Up          int     `json:"up"`
	Idle        int     `json:"idle"`
	IdleLimit   int     `json:"idle_limit"`
	Max         int     `json:"max"`
	ModelsBytes int64   `json:"models_bytes"`
	DiskGB      int     `json:"disk_gb"`
	Log         string  `json:"log"`
	DiskRead    float64 `json:"disk_read"`
	SrvCPU      float64 `json:"srv_cpu"`
	NetTx       float64 `json:"net_tx"`
	Step        string  `json:"step"`
	FilesDone   int     `json:"files_done"`
	FilesTotal  int     `json:"files_total"`
	SpotWarn    int     `json:"spot_warn"`
	SpotInfo    string  `json:"spot_info"`
}

type (
	tickMsg time.Time
	instMsg struct {
		in  *Instance
		err error
	}
	typesMsg struct {
		t   []TypeInfo
		err error
	}
	spotMsg  map[string]float64
	priceMsg struct {
		typ string
		usd float64
	}
	s3Msg struct {
		objs map[string]int64
		err  error
	}
	sgMsg struct {
		rules []Rule
		err   error
	}
	ipMsg  string
	volMsg struct {
		v   []Volume
		err error
	}
	metricMsg     Metrics
	streamMsg     string
	scriptLineMsg struct{ label, line string }
	remoteLineMsg string
	doneMsg       struct {
		label, out string
		err        error
	}
)

type model struct {
	cfg       Config
	root      string
	w, h      int
	tab       tab
	inst      *Instance
	instErr   string
	polled    bool
	types     []TypeInfo
	prices    map[string]float64
	spots     map[string]float64
	pricing   map[string]bool
	s3        map[string]int64
	s3Err     string
	vols      []Volume
	volErr    string
	volDone   bool
	met       *Metrics
	hist      []int
	vhist     []int
	stream    string
	ch        chan tea.Msg
	cancel    context.CancelFunc
	sIP       string
	log       []string
	busy      string
	confirm   string
	cursor    int
	editing   bool
	input     textinput.Model
	lastInst  *Instance
	lastPx    float64
	month     float64
	notified  map[string]bool
	seen      map[string]string
	reqStart  time.Time
	cancels   int
	cancelSec int
	jobs      int
	conns     int
	ldPrev    float64
	ldPrevT   time.Time
	ldRate    float64
	loading   bool
	ldStart   time.Time
	ldTarget  float64
	ldRead    bool
	stage     string
	stageCand string
	stageN    int
	stageT    time.Time
	idleFrom  time.Time
	winOn     bool
	winStart  time.Time
	winSecs   map[string]int
	lastWin   string
	jobOn     bool
	jobStart  time.Time
	lastBusy  time.Time
	lastJob   time.Duration
	rules     []Rule
	rulesErr  string
	myip      string
	rcur      int
	editKind  string
	stop      context.CancelFunc // cancels the running launch
	lrows     []launchRow        // live launch table
	lastErr   string             // last failed operation, shown in the headline until the next action
	logOff    int                // Log tab: lines scrolled up from the newest
	logFilt   int                // Log tab: index into logFilters
	boots     []bootRec          // past boot times, for the boot ETA
	sawBoot   bool               // this server was seen before it was ready, so its boot time is real
	syncFrom  time.Time
	syncSecs  int
	wiz       *wizard // first-run setup, shown until config.json exists
}

func (m *model) logf(f string, a ...any) { m.logAt(time.Now(), f, a...) }

// logAt adds a line to the Activity panel and appends it to logs/tui.log.
func (m *model) logAt(t time.Time, f string, a ...any) {
	line := t.Format("15:04:05") + " " + fmt.Sprintf(f, a...)
	m.log = append(m.log, line)
	if len(m.log) > 5000 {
		m.log = m.log[len(m.log)-5000:]
	}
	if m.root != "" {
		_ = os.MkdirAll(filepath.Join(m.root, "logs"), 0o755)
		if fh, err := os.OpenFile(filepath.Join(m.root, "logs", "tui.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintln(fh, t.Format("2006-01-02 15:04:05")+" "+fmt.Sprintf(f, a...))
			fh.Close()
		}
	}
}

// logChange logs a polled value only when it differs from the last one logged under key.
func (m *model) logChange(key, f string, a ...any) {
	v := fmt.Sprintf(f, a...)
	if m.seen[key] == v {
		return
	}
	m.seen[key] = v
	m.logf("%s", v)
}

func (m model) cfgPath() string { return filepath.Join(m.root, "config.json") }
func (m model) saveCfg()        { m.cfg.save(m.cfgPath()) }

func newModel() model {
	root, _ := os.Getwd()
	ti := textinput.New()
	ti.CharLimit = 300
	m := model{root: root, ch: make(chan tea.Msg, 64), prices: map[string]float64{}, spots: map[string]float64{},
		pricing: map[string]bool{}, notified: map[string]bool{}, seen: map[string]string{}, input: ti}
	m.cfg = loadConfig(m.cfgPath())
	if _, err := os.Stat(m.cfgPath()); err != nil {
		m.wiz = newWizard(m.cfg)
	}
	if b, err := os.ReadFile(filepath.Join(root, ".state", "last_region")); err == nil {
		m.cfg.Browse = strings.TrimSpace(string(b))
	}
	m.month = monthSpend(root, time.Now())
	m.boots = loadBoots(root)
	return m
}

func (m model) Init() tea.Cmd {
	if m.wiz != nil {
		return tea.Batch(tick(), m.listen(), textinput.Blink)
	}
	return tea.Batch(m.refreshAll(), tick(), m.listen())
}

func tick() tea.Cmd { return tea.Tick(5*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }) }
func (m model) listen() tea.Cmd {
	ch := m.ch
	return func() tea.Msg { return <-ch }
}
func (m model) pollInst() tea.Cmd {
	c := m.cfg
	return func() tea.Msg { in, err := describeInstance(c); return instMsg{in, err} }
}
func (m model) loadTypes() tea.Cmd {
	c := m.cfg
	return func() tea.Msg { t, err := fetchTypes(c); return typesMsg{t, err} }
}
func (m model) loadS3() tea.Cmd {
	c := m.cfg
	return func() tea.Msg { o, err := listS3Models(c); return s3Msg{o, err} }
}
func (m model) loadVol() tea.Cmd {
	c := m.cfg
	return func() tea.Msg { v, err := describeVolumes(c); return volMsg{v, err} }
}
func (m *model) loadPrice(t string) tea.Cmd {
	if _, ok := m.prices[t]; ok || m.pricing[t] {
		return nil
	}
	m.pricing[t] = true
	c := m.cfg
	return func() tea.Msg { p, _ := fetchPrice(c, t); return priceMsg{t, p} }
}

func (m model) loadRules() tea.Cmd {
	c := m.cfg
	return func() tea.Msg { r, err := listRules(c); return sgMsg{r, err} }
}
func (m model) loadIP() tea.Cmd { return func() tea.Msg { return ipMsg(publicIP()) } }

func (m model) refreshAll() tea.Cmd {
	return tea.Batch(m.pollInst(), m.loadTypes(), m.loadS3(), m.loadVol(), m.loadRules(), m.loadIP())
}

func (m model) ruleCmd(add bool, cidr string, port int) tea.Cmd {
	c := m.cfg
	return func() tea.Msg {
		out, err := changeRule(c, add, cidr, port)
		return doneMsg{"network", out, err}
	}
}

// op runs a long job off the UI thread. Its progress lines and launch table rows land as they happen.
func (m model) op(ctx context.Context, label string, f func(j *job) (string, error)) tea.Cmd {
	m.saveCfg()
	ch := m.ch
	j := &job{c: m.cfg, root: m.root, ctx: ctx,
		out: func(s string) { ch <- scriptLineMsg{label, s} },
		row: func(r launchRow) { ch <- launchRowMsg(r) }}
	return func() tea.Msg {
		out, err := f(j)
		return doneMsg{label, out, err}
	}
}

func (m *model) startStream(ip string) {
	if m.cancel != nil {
		m.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel, m.sIP = cancel, ip
	c := m.cfg
	fwd := fmt.Sprintf("%d:127.0.0.1:%d", c.LocalPort, c.ServerPort)
	ch := m.ch
	go func() { // every line the server writes to its event file, in order, surviving reconnects
		got := 0
		for ctx.Err() == nil {
			args := append(sshOpts(c), "-o", "ServerAliveInterval=10", c.SSHUser+"@"+ip,
				fmt.Sprintf("touch /var/log/dt-events.log 2>/dev/null; tail -n +%d -F /var/log/dt-events.log", got+1))
			cmd := exec.CommandContext(ctx, "ssh", args...)
			if out, err := cmd.StdoutPipe(); err == nil && cmd.Start() == nil {
				sc := bufio.NewScanner(out)
				sc.Buffer(make([]byte, 64*1024), 1024*1024)
				for sc.Scan() {
					got++
					ch <- remoteLineMsg(sc.Text())
				}
				_ = cmd.Wait()
			}
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
		}
	}()
	go func() {
		for ctx.Err() == nil {
			args := append(sshOpts(c), "-o", "ServerAliveInterval=10", "-o", "ExitOnForwardFailure=yes", "-L", fwd,
				c.SSHUser+"@"+ip, "/usr/local/bin/dt-metrics.sh")
			cmd := exec.CommandContext(ctx, "ssh", args...)
			out, _ := cmd.StdoutPipe()
			var errb strings.Builder
			cmd.Stderr = &errb
			if err := cmd.Start(); err == nil {
				ch <- streamMsg("tunnel up")
				sc := bufio.NewScanner(out)
				for sc.Scan() {
					var mt Metrics
					if err := json.Unmarshal(sc.Bytes(), &mt); err == nil {
						ch <- metricMsg(mt)
					} else {
						ch <- streamMsg("bad metrics line (" + err.Error() + "): " + truncate(sc.Text(), 100))
					}
				}
				_ = cmd.Wait()
			}
			if e := strings.TrimSpace(errb.String()); e != "" {
				ch <- streamMsg("ssh: " + firstLine(e))
			} else {
				ch <- streamMsg("waiting for ssh")
			}
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
		}
	}()
}

// trackCancel measures GPU time thrown away when a generation is cancelled (the Draw Things client
// often restarts it straight away). It cannot stop that, but it shows what each cancel cost.
func (m *model) trackCancel(t time.Time, line string) {
	switch {
	case strings.Contains(line, "Received image processing request, begin."):
		m.reqStart = t
	case strings.Contains(line, "cacncelled image generation") || strings.Contains(line, "cancelled image generation"):
		if m.reqStart.IsZero() {
			return
		}
		secs := int(t.Sub(m.reqStart).Seconds())
		m.cancels++
		m.cancelSec += secs
		m.logAt(t, "JOB CANCELLED after %s of work, about $%.2f thrown away. %d cancelled this session, %s in total",
			dur(secs), m.lastPx*float64(secs)/3600, m.cancels, dur(m.cancelSec))
		m.reqStart = time.Time{}
	}
}

func (m *model) stopStream() {
	if m.cancel != nil {
		m.cancel()
		m.cancel, m.sIP = nil, ""
	}
	m.met, m.stream = nil, ""
}

func (m model) priceFor(in *Instance) float64 {
	if in.Spot && m.spots[in.Type] > 0 {
		return m.spots[in.Type]
	}
	return m.prices[in.Type]
}

func (m model) effPrice(t string) float64 {
	if m.cfg.Spot && m.spots[t] > 0 {
		return m.spots[t]
	}
	return m.prices[t]
}

// spend is this month's finished sessions plus the running one.
func (m model) spend() float64 {
	s := m.month
	if m.inst != nil {
		s += m.priceFor(m.inst) * time.Since(m.inst.Launch).Hours()
	}
	return s
}

func (m *model) once(key string) bool {
	if m.notified[key] {
		return false
	}
	m.notified[key] = true
	return true
}

func (m model) update(msg tea.Msg) (model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case tickMsg:
		if m.inst != nil {
			hrs := time.Since(m.inst.Launch).Hours()
			if m.cfg.WarnAfterHours > 0 && hrs > float64(m.cfg.WarnAfterHours) && m.once(m.inst.ID+"long") {
				m.logf("server has been up %.1f h", hrs)
				notify(m.cfg, "Draw Things server", fmt.Sprintf("Still running after %.0f hours", hrs))
			}
		}
		if b := m.cfg.MonthlyBudgetUSD; b > 0 && m.spend() >= b && m.once("budget"+time.Now().Format("2006-01")) {
			m.logf("monthly budget $%.0f reached", b)
			notify(m.cfg, "Draw Things server", fmt.Sprintf("Monthly budget of $%.0f reached", b))
		}
		return m, tea.Batch(m.pollInst(), tick())
	case instMsg:
		if msg.err != nil {
			m.instErr = msg.err.Error()
			m.logChange("inst", "server lookup across %s failed: %s", strings.Join(m.cfg.Regions, ","), m.instErr)
			break
		}
		m.instErr = ""
		if msg.in == nil {
			m.logChange("inst", "no server found in %s", strings.Join(m.cfg.Regions, ","))
		} else {
			m.logChange("inst", "server %s %s %s %s in %s ip=%s launched %s", msg.in.ID, msg.in.Type, onoff(msg.in.Spot, "spot", "on-demand"),
				msg.in.State, msg.in.Region, onelse(msg.in.IP, "none yet"), msg.in.Launch.Local().Format("15:04:05"))
		}
		if !m.polled {
			m.polled = true
			if msg.in != nil {
				m.logf("found running server %s", msg.in.ID)
			}
		}
		if msg.in != nil && m.lastInst != nil && msg.in.ID != m.lastInst.ID {
			end := time.Now()
			cost := m.lastPx * end.Sub(m.lastInst.Launch).Hours()
			appendSession(m.root, Session{ID: m.lastInst.ID, Type: m.lastInst.Type, Spot: m.lastInst.Spot, Start: m.lastInst.Launch, End: end, CostUSD: cost})
			m.month = monthSpend(m.root, end)
			m.logf("old server ended after %s, about $%.2f. New server %s", dur(int(end.Sub(m.lastInst.Launch).Seconds())), cost, msg.in.ID)
			m.jobs, m.jobOn, m.conns, m.met = 0, false, 0, nil
			m.loading, m.ldRate, m.ldPrevT = false, 0, time.Time{}
			m.stage, m.winOn = "", false
			m.reqStart, m.cancels, m.cancelSec = time.Time{}, 0, 0
			m.sawBoot, m.syncFrom, m.syncSecs = false, time.Time{}, 0
			m.stopStream()
		}
		m.inst = msg.in
		var regionCmd tea.Cmd
		if msg.in != nil && msg.in.Region != m.cfg.Active {
			m.cfg.Active = msg.in.Region
			m.prices, m.spots, m.pricing = map[string]float64{}, map[string]float64{}, map[string]bool{}
			regionCmd = tea.Batch(m.loadTypes(), m.loadRules())
		}
		if msg.in != nil {
			if px := m.priceFor(msg.in); px > 0 || m.lastInst == nil || m.lastInst.ID != msg.in.ID {
				m.lastPx = px
			}
			m.lastInst = msg.in
			m.logChange("px"+msg.in.ID, "pricing %s at $%.4f/h (%s, %s) for the session cost", msg.in.ID, m.lastPx, onoff(msg.in.Spot, "spot", "on-demand"), msg.in.Region)
			if msg.in.State == "running" && msg.in.IP != "" && m.sIP != msg.in.IP {
				m.startStream(msg.in.IP)
				m.logf("opening tunnel to %s", msg.in.IP)
			}
		} else if m.lastInst != nil {
			end := time.Now()
			cost := m.lastPx * end.Sub(m.lastInst.Launch).Hours()
			appendSession(m.root, Session{ID: m.lastInst.ID, Type: m.lastInst.Type, Spot: m.lastInst.Spot, Start: m.lastInst.Launch, End: end, CostUSD: cost})
			m.month = monthSpend(m.root, end)
			m.logf("server ended after %s, about $%.2f", dur(int(end.Sub(m.lastInst.Launch).Seconds())), cost)
			notify(m.cfg, "Draw Things server", fmt.Sprintf("Server ended, about $%.2f", cost))
			m.lastInst, m.jobs, m.jobOn, m.conns = nil, 0, false, 0
			m.loading, m.ldRate, m.ldPrevT = false, 0, time.Time{}
			m.stage, m.winOn = "", false
			m.reqStart, m.cancels, m.cancelSec = time.Time{}, 0, 0
			m.sawBoot, m.syncFrom, m.syncSecs = false, time.Time{}, 0
			m.stopStream()
			if m.lastPx == 0 {
				m.logf("no price was known for that server, so its cost was recorded as $0")
			}
			m.lastPx = 0
			m.cfg.Active = ""
			m.prices, m.spots, m.pricing = map[string]float64{}, map[string]float64{}, map[string]bool{}
			return m, tea.Batch(m.loadVol(), m.loadTypes(), m.loadRules())
		}
		if regionCmd != nil {
			return m, regionCmd
		}
	case typesMsg:
		m.types = msg.t
		if msg.err != nil {
			m.logChange("typesErr", "instance types lookup in %s failed: %v", m.cfg.reg(), msg.err)
		} else {
			var tn []string
			for _, t := range msg.t {
				tn = append(tn, fmt.Sprintf("%s(%.0fGiB %s)", t.Type, t.VRAMGiB, t.GPU))
			}
			m.logChange("types", "instance types offered in %s: %s", m.cfg.reg(), strings.Join(tn, ", "))
		}
		var cmds []tea.Cmd
		var names []string
		for _, t := range msg.t {
			cmds = append(cmds, m.loadPrice(t.Type))
			names = append(names, t.Type)
		}
		if len(names) > 0 {
			c := m.cfg
			cmds = append(cmds, func() tea.Msg { return spotMsg(fetchSpot(c, names)) })
		}
		return m, tea.Batch(cmds...)
	case spotMsg:
		m.spots = map[string]float64(msg)
		var ks []string
		for k := range m.spots {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		var parts []string
		for _, k := range ks {
			parts = append(parts, fmt.Sprintf("%s=$%.4f", k, m.spots[k]))
		}
		m.logChange("spots", "spot prices in %s (USD/h): %s", m.cfg.reg(), onelse(strings.Join(parts, " "), "none returned"))
	case priceMsg:
		m.prices[msg.typ] = msg.usd
		m.logChange("price"+msg.typ, "on-demand price in %s: %s = $%.4f/h", m.cfg.reg(), msg.typ, msg.usd)
	case s3Msg:
		m.s3, m.s3Err = msg.objs, ""
		if msg.err != nil {
			m.s3Err = msg.err.Error()
			m.logChange("s3", "S3 listing of %s failed: %s", m.cfg.Bucket, m.s3Err)
		} else {
			var tot int64
			for _, v := range msg.objs {
				tot += v
			}
			m.logChange("s3", "S3 bucket %s (%s): %d model files, %.1f GiB", m.cfg.Bucket, m.cfg.BucketRegion, len(msg.objs), float64(tot)/(1<<30))
		}
	case sgMsg:
		m.rules, m.rulesErr = msg.rules, ""
		if msg.err != nil {
			m.rulesErr = msg.err.Error()
			m.logChange("rules", "security group rules in %s failed: %s", m.cfg.reg(), m.rulesErr)
		} else {
			var rs []string
			for _, r := range msg.rules {
				rs = append(rs, fmt.Sprintf("%s:%d", r.CIDR, r.Port))
			}
			m.logChange("rules", "security group rules in %s: %s", m.cfg.reg(), onelse(strings.Join(rs, ", "), "none"))
		}
		if m.rcur >= len(m.rules) {
			m.rcur = max(0, len(m.rules)-1)
		}
	case ipMsg:
		m.myip = string(msg)
		m.logChange("ip", "your public IP: %s", m.myip)
	case volMsg:
		m.vols, m.volErr, m.volDone = msg.v, "", true
		if msg.err != nil {
			m.volErr = msg.err.Error()
			m.logChange("vols", "model volume lookup failed: %s", m.volErr)
		} else {
			var vs []string
			for _, v := range msg.v {
				vs = append(vs, fmt.Sprintf("%s %s %dGB %s", v.ID, v.AZ, v.SizeGB, v.State))
			}
			m.logChange("vols", "model volumes in %s: %s", strings.Join(m.cfg.Regions, ","), onelse(strings.Join(vs, "; "), "none"))
		}
	case metricMsg:
		mt := Metrics(msg)
		m.met, m.stream = &mt, "connected"
		m.hist = append(m.hist, mt.GPU)
		if mt.VRAMTotal > 0 {
			m.vhist = append(m.vhist, int(mt.VRAM*100/mt.VRAMTotal))
		}
		if len(m.hist) > 300 {
			m.hist, m.vhist = m.hist[1:], m.vhist[1:]
		}
		m.trackJob(mt)
		m.trackLoad(mt)
		m.trackStage(mt)
		if mt.Conns != m.conns {
			switch {
			case mt.Conns > m.conns && m.conns == 0:
				m.logf("client connected (%d)", mt.Conns)
				notify(m.cfg, "Draw Things server", "Client connected")
			case mt.Conns == 0:
				m.logf("client disconnected")
			default:
				m.logf("clients now %d", mt.Conns)
			}
			m.conns = mt.Conns
		}
		id := ""
		if m.inst != nil {
			id = m.inst.ID
		}
		m.trackBoot(mt)
		if mt.Phase == "ready" && m.once(id+"ready") {
			m.logf("server READY")
			notify(m.cfg, "Draw Things server", "Ready. Connect from Draw Things.", "Glass")
		}
		if mt.SpotWarn == 1 && m.once(id+"spot") {
			m.logf("SPOT INTERRUPTION notice: %s", mt.SpotInfo)
			notify(m.cfg, "Draw Things server", "Spot interruption: AWS is reclaiming the server")
		}
		return m, m.listen()
	case streamMsg:
		m.stream = string(msg)
		m.logChange("stream", "tunnel: %s", m.stream)
		return m, m.listen()
	case remoteLineMsg:
		line := string(msg)
		t := time.Now()
		if i := strings.IndexByte(line, ' '); i > 0 {
			if sec, err := strconv.ParseInt(line[:i], 10, 64); err == nil {
				t, line = time.Unix(sec, 0), line[i+1:]
			}
		}
		m.logAt(t, "[server] %s", truncate(line, 220))
		m.trackCancel(t, line)
		return m, m.listen()
	case launchRowMsg:
		r := launchRow(msg)
		found := false
		for i := range m.lrows {
			if m.lrows[i].Type == r.Type && m.lrows[i].Region == r.Region {
				m.lrows[i], found = r, true
			}
		}
		if !found {
			m.lrows = append(m.lrows, r)
		}
		return m, m.listen()
	case scriptLineMsg:
		if strings.TrimSpace(msg.line) != "" {
			m.logf("[%s] %s", msg.label, msg.line)
		}
		return m, m.listen()
	case doneMsg:
		m.busy = ""
		if msg.label == "launch" && m.stop != nil {
			m.stop()
			m.stop = nil
		}
		if msg.err != nil {
			m.lastErr = msg.label + ": " + msg.err.Error()
			m.logf("%s FAILED: %s", msg.label, msg.err.Error())
			if msg.label == "launch" && strings.Contains(msg.err.Error(), "InsufficientInstanceCapacity") {
				kind := onoff(m.cfg.Spot, "spot", "on-demand")
				m.logf("AWS has no %s capacity for %s in any region or zone right now. Nothing was created.", kind, m.cfg.InstanceType)
				if m.cfg.Spot {
					m.logf("Press l to retry, or turn Spot off on the Config tab to launch on-demand (costs more).")
				} else {
					m.logf("Press l to retry in a few minutes.")
				}
			}
		} else {
			m.logf("%s done: %s", msg.label, msg.out)
		}
		return m, m.refreshAll()
	case bucketRegionMsg:
		if m.wiz != nil {
			m = m.wizardBucket(msg)
		}
	case tea.KeyMsg:
		if m.wiz != nil {
			return m.wizardKey(msg)
		}
		return m.key(msg)
	}
	return m, nil
}

// trackJob infers generations from GPU use: busy means a job is running.
func (m *model) trackJob(mt Metrics) {
	now := time.Now()
	if mt.GPU >= m.cfg.GPUIdlePct {
		m.lastBusy = now
		if !m.jobOn {
			m.jobOn, m.jobStart = true, now
			m.jobs++
		}
		return
	}
	if m.jobOn && now.Sub(m.lastBusy) > 45*time.Second {
		m.jobOn = false
		m.lastJob = m.lastBusy.Sub(m.jobStart)
		if m.lastJob > 2*time.Minute {
			m.logf("generation finished after %s", dur(int(m.lastJob.Seconds())))
			notify(m.cfg, "Draw Things server", "Generation finished after "+dur(int(m.lastJob.Seconds())))
		}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	nm, c := m.update(msg)
	return nm, c
}

// requiredBytes counts models present in S3 with a size matching the local copy.
func (m model) requiredBytes() (have, total int64, missing int) {
	for _, f := range m.cfg.Models {
		if s, ok := m.s3[f]; ok {
			if st, err := os.Stat(filepath.Join(m.cfg.LocalModels, f)); err == nil && st.Size() != s {
				missing++
				continue
			}
			have += s
			total += s
		} else {
			missing++
		}
	}
	return
}

func (m model) key(k tea.KeyMsg) (model, tea.Cmd) {
	if m.editing {
		switch k.String() {
		case "enter":
			m.editing = false
			if m.editKind != "cfg" {
				cidr, err := normalizeCIDR(m.input.Value())
				if err != nil {
					m.logf("%v", err)
					return m, nil
				}
				port := 22
				if m.editKind == "direct" {
					port = m.cfg.ServerPort
				}
				m.logf("adding %s on port %d", cidr, port)
				return m, m.ruleCmd(true, cidr, port)
			}
			f := m.fields()[m.cursor]
			f.set(&m, m.input.Value())
			m.saveCfg()
			return m, m.refreshAll()
		case "esc":
			m.editing = false
			return m, nil
		}
		var c tea.Cmd
		m.input, c = m.input.Update(k)
		return m, c
	}
	s := k.String()
	if s != "x" && s != "d" && s != "l" {
		m.confirm = ""
	}
	if s == "l" || s == "x" || s == "d" || s == "u" {
		m.lastErr = ""
	}
	switch s {
	case "ctrl+c", "q":
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit
	case "1", "2", "3", "4", "5":
		m.tab = tab(s[0] - '1')
		return m, nil
	case "tab":
		m.tab = (m.tab + 1) % 5
		return m, nil
	case "shift+tab":
		m.tab = (m.tab + 4) % 5
		return m, nil
	case "p":
		m.fields()[0].flip(&m, 1)
		m.saveCfg()
		return m, m.loadTypes()
	case "c":
		if m.stop != nil {
			m.stop()
			m.logf("launch cancel requested, it stops at the next step")
		}
		return m, nil
	case "l":
		if m.busy != "" {
			return m, nil
		}
		relaunch := false
		if m.inst != nil {
			if m.inst.State != "shutting-down" {
				return m, nil
			}
			if m.confirm != "relaunch" {
				m.confirm = "relaunch"
				return m, nil
			}
			m.confirm, relaunch = "", true
		}
		if _, _, miss := m.requiredBytes(); m.s3 == nil || miss > 0 {
			m.logf("cannot launch: models missing or wrong size in S3 (see Models tab)")
			return m, nil
		}
		ctx, stop := context.WithCancel(context.Background())
		m.stop, m.lrows, m.busy = stop, nil, "launching"
		if relaunch {
			m.logf("force-detaching the model volume from the old server, then launching a new one")
		}
		m.logf("launch settings: type=%s spot=%v profile=%q idle=%dm max=%dh rootdisk=%dGB volume=%v(%dGB, %d iops, %d MiB/s) bucket=%s(%s) image=%s cache=%v fallback_types=%v retry=%dm",
			m.cfg.InstanceType, m.cfg.Spot, m.cfg.Profile, m.cfg.IdleMin, m.cfg.MaxHours, m.cfg.VolumeGB, m.cfg.ModelVolume.Enabled,
			m.cfg.ModelVolume.SizeGB, m.cfg.ModelVolume.IOPS, m.cfg.ModelVolume.ThroughputMiBs, m.cfg.Bucket, m.cfg.BucketRegion, m.cfg.DockerImage, m.cfg.ImageCache, m.cfg.FallbackTypes, m.cfg.RetryMin)
		m.logf("launching %s (%s), trying regions cheapest first from %s", m.cfg.InstanceType, map[bool]string{true: "spot", false: "on-demand"}[m.cfg.Spot], strings.Join(m.cfg.Regions, ", "))
		return m, m.op(ctx, "launch", func(j *job) (string, error) { return j.launch(relaunch) })
	case "x":
		if m.inst == nil || m.busy != "" {
			return m, nil
		}
		if m.confirm != "terminate" {
			m.confirm = "terminate"
			return m, nil
		}
		m.confirm, m.busy = "", "terminating"
		m.logf("terminating server")
		return m, m.op(context.Background(), "terminate", (*job).terminate)
	case "g":
		if m.busy != "" || m.inst == nil {
			return m, nil
		}
		m.logf("saving diagnostics to logs/ ...")
		m.busy = "saving diagnostics"
		return m, m.op(context.Background(), "diagnostics", (*job).diag)
	case "d":
		if m.busy == "" && m.inst != nil && m.inst.State == "shutting-down" {
			if m.confirm != "detach" {
				m.confirm = "detach"
				return m, nil
			}
			m.confirm, m.busy = "", "detaching volume"
			m.logf("force-detaching the model volume from the old server")
			return m, m.op(context.Background(), "detach volume", (*job).freeVolumes)
		}
		if m.tab != tabModels || m.busy != "" {
			return m, nil
		}
		if m.unusedVols() == 0 {
			m.logf("no unused volumes to delete")
			return m, nil
		}
		if m.confirm != "volume" {
			m.confirm = "volume"
			return m, nil
		}
		m.confirm, m.busy = "", "deleting volume"
		return m, m.op(context.Background(), "delete volume", (*job).deleteVolumes)
	case "i":
		if m.myip == "" {
			m.logf("could not work out your public IP")
			return m, nil
		}
		m.logf("allowing SSH from %s", m.myip)
		return m, m.ruleCmd(true, m.myip+"/32", 22)
	case "a", "A":
		m.tab, m.editing, m.editKind = tabConnect, true, map[string]string{"a": "ssh", "A": "direct"}[s]
		m.input.SetValue("")
		m.input.Focus()
		return m, nil
	case "backspace", "delete":
		if m.tab == tabConnect && m.rcur < len(m.rules) {
			r := m.rules[m.rcur]
			m.logf("removing %s port %d", r.CIDR, r.Port)
			return m, m.ruleCmd(false, r.CIDR, r.Port)
		}
	case "e":
		if m.sIP != "" {
			c, ip := m.cfg, m.sIP
			m.logf("idle timer reset")
			return m, func() tea.Msg {
				out, err := sh(15*time.Second, "ssh", append(sshOpts(c), c.SSHUser+"@"+ip, "touch /run/dt/keepalive")...)
				return doneMsg{"extend", out, err}
			}
		}
	case "r":
		return m, m.refreshAll()
	case "u":
		if m.tab == tabModels && m.busy == "" {
			m.busy = "uploading"
			m.logf("uploading missing models")
			s3 := m.s3
			return m, m.op(context.Background(), "upload", func(j *job) (string, error) { return j.upload(s3) })
		}
	}
	if m.tab == tabConfig {
		return m.configKey(s)
	}
	if m.tab == tabLog {
		page := max(1, m.h-12)
		switch s {
		case "up", "k":
			m.logOff++
		case "down", "j":
			m.logOff--
		case "pgup":
			m.logOff += page
		case "pgdown", " ":
			m.logOff -= page
		case "home":
			m.logOff = len(m.log)
		case "end":
			m.logOff = 0
		case "f":
			m.logFilt, m.logOff = (m.logFilt+1)%len(logFilters), 0
		}
		m.logOff = max(0, min(m.logOff, len(m.filteredLog())-1))
	}
	if m.tab == tabConnect {
		switch s {
		case "up", "k":
			m.rcur = max(0, m.rcur-1)
		case "down", "j":
			m.rcur = min(max(0, len(m.rules)-1), m.rcur+1)
		}
	}
	return m, nil
}

// ---- config editor ----

type field struct {
	name string
	help string
	get  func(m model) string
	set  func(m *model, v string)
	flip func(m *model, dir int)
}

func onoff(b bool, on, off string) string {
	if b {
		return on
	}
	return off
}

func atoiMin(v string, min int) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	return n, err == nil && n >= min
}

func (m model) fields() []field {
	custom := func(m *model) { m.cfg.Profile = "" }
	return []field{
		{name: "Profile", help: "Presets from config.json. Press p anywhere to cycle.",
			get: func(m model) string { return onelse(m.cfg.Profile, "(custom)") },
			flip: func(m *model, d int) {
				names := m.cfg.profileNames()
				if len(names) == 0 {
					return
				}
				idx := -1
				for i, n := range names {
					if n == m.cfg.Profile {
						idx = i
					}
				}
				m.cfg.applyProfile(names[(idx+d+len(names))%len(names)])
			}},
		{name: "Instance type", help: "left/right to pick. Fit is judged from your model sizes.",
			get: func(m model) string { return m.cfg.InstanceType },
			flip: func(m *model, d int) {
				if len(m.types) == 0 {
					return
				}
				idx := 0
				for i, t := range m.types {
					if t.Type == m.cfg.InstanceType {
						idx = i
					}
				}
				m.cfg.InstanceType = m.types[(idx+d+len(m.types))%len(m.types)].Type
				custom(m)
			}},
		{name: "Spot instance", help: "About 70% cheaper. AWS can reclaim it with 2 minutes notice.",
			get:  func(m model) string { return onoff(m.cfg.Spot, "on", "off (on-demand)") },
			flip: func(m *model, d int) { m.cfg.Spot = !m.cfg.Spot; custom(m) }},
		{name: "Persistent model volume", help: "Keeps models on an EBS disk between runs for faster boots. Billed monthly while it exists.",
			get:  func(m model) string { return onoff(m.cfg.ModelVolume.Enabled, "on", "off (sync from S3 every launch)") },
			flip: func(m *model, d int) { m.cfg.ModelVolume.Enabled = !m.cfg.ModelVolume.Enabled }},
		{name: "Volume IOPS", help: "gp3 volume speed, 3000 to 16000. Extra IOPS cost money every month, even with no server. Applied at the next launch (AWS allows one change per 6 hours).",
			get: func(m model) string {
				return fmt.Sprintf("%d   about $%.2f / month with the throughput below", m.cfg.ModelVolume.IOPS, m.cfg.ModelVolume.monthlyUSD())
			},
			set: func(m *model, v string) {
				if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
					m.setVolPerf(n, m.cfg.ModelVolume.ThroughputMiBs)
				}
			}},
		{name: "Volume throughput (MiB/s)", help: "gp3 volume throughput, 125 to 1000, and at most 1 MiB/s per 4 IOPS. Extra throughput costs money every month. Applied at the next launch.",
			get: func(m model) string { return fmt.Sprintf("%d", m.cfg.ModelVolume.ThroughputMiBs) },
			set: func(m *model, v string) {
				if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
					m.setVolPerf(m.cfg.ModelVolume.IOPS, n)
				}
			}},
		{name: "Delete idle volume (min)", help: "Delete the model volume after this many minutes with no server. 0 keeps it. Applied at the next launch.",
			get: func(m model) string {
				if m.cfg.ModelVolume.DeleteAfterMin <= 0 {
					return "never"
				}
				return fmt.Sprintf("%d min", m.cfg.ModelVolume.DeleteAfterMin)
			},
			set: func(m *model, v string) {
				if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
					m.cfg.ModelVolume.DeleteAfterMin = n
				}
			}},
		{name: "Allowed IPs (SSH)", help: "Comma separated CIDRs. 'auto' means your current public IP. Only SSH is exposed.",
			get: func(m model) string { return strings.Join(m.cfg.AllowedCIDRs, ", ") },
			set: func(m *model, v string) {
				var out []string
				for _, p := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' }) {
					out = append(out, p)
				}
				if len(out) > 0 {
					m.cfg.AllowedCIDRs = out
				}
			}},
		{name: "Shared secret", help: "Optional password the server asks Draw Things for. left/right generates one, enter types one, empty clears. Enter the same in the app.",
			get: func(m model) string { return onelse(m.cfg.SharedSecret, "(none)") },
			set: func(m *model, v string) { m.cfg.SharedSecret = strings.Trim(strings.TrimSpace(v), "-") },
			flip: func(m *model, d int) {
				const al = "abcdefghijkmnpqrstuvwxyz23456789"
				b := make([]byte, 16)
				_, _ = rand.Read(b)
				for i := range b {
					b[i] = al[int(b[i])%len(al)]
				}
				m.cfg.SharedSecret = string(b)
			}},
		{name: "Idle shutdown (min)", help: "Minutes of no GPU activity before the server terminates itself.",
			get: func(m model) string { return strconv.Itoa(m.cfg.IdleMin) },
			set: func(m *model, v string) {
				if n, ok := atoiMin(v, 1); ok {
					m.cfg.IdleMin = n
					custom(m)
				}
			}},
		{name: "Max lifetime (h)", help: "Hard stop regardless of activity.",
			get: func(m model) string { return strconv.Itoa(m.cfg.MaxHours) },
			set: func(m *model, v string) {
				if n, ok := atoiMin(v, 1); ok {
					m.cfg.MaxHours = n
					custom(m)
				}
			}},
		{name: "CPU offload", help: "Move some weights to system RAM. Only needed on smaller GPUs.",
			get:  func(m model) string { return onoff(m.cfg.CPUOffload, "on", "off") },
			flip: func(m *model, d int) { m.cfg.CPUOffload = !m.cfg.CPUOffload; custom(m) }},
		{name: "Regions", help: "Comma separated. A launch tries them cheapest first (by current price). The first is used for browsing prices when no server runs. Models are read from the bucket, so other regions than the bucket's pay S3 transfer.",
			get: func(m model) string { return strings.Join(m.cfg.Regions, ",") },
			set: func(m *model, v string) {
				var rs []string
				for _, r := range strings.Split(v, ",") {
					if r = strings.TrimSpace(r); r != "" {
						rs = append(rs, r)
					}
				}
				if len(rs) > 0 {
					m.cfg.Regions, m.cfg.Region = rs, rs[0]
				}
			}},
		{name: "Bucket", help: "S3 bucket holding models/ .",
			get: func(m model) string { return m.cfg.Bucket },
			set: func(m *model, v string) { m.cfg.Bucket = strings.TrimSpace(v) }},
		{name: "Root disk (GB)", help: "Disposable system disk. Deleted when the server ends.",
			get: func(m model) string { return strconv.Itoa(m.cfg.VolumeGB) },
			set: func(m *model, v string) {
				if n, ok := atoiMin(v, 100); ok {
					m.cfg.VolumeGB = n
				}
			}},
		{name: "Local tunnel port", help: "Port on this Mac that Draw Things connects to.",
			get: func(m model) string { return strconv.Itoa(m.cfg.LocalPort) },
			set: func(m *model, v string) {
				if n, ok := atoiMin(v, 1025); ok {
					m.cfg.LocalPort = n
				}
			}},
		{name: "Local models dir", help: "Where your Draw Things models live on this Mac.",
			get: func(m model) string { return m.cfg.LocalModels },
			set: func(m *model, v string) { m.cfg.LocalModels = strings.TrimSpace(v) }},
	}
}

func onelse(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (m model) configKey(s string) (model, tea.Cmd) {
	fs := m.fields()
	switch s {
	case "up", "k":
		m.cursor = (m.cursor + len(fs) - 1) % len(fs)
	case "down", "j":
		m.cursor = (m.cursor + 1) % len(fs)
	case "left", "h", "right":
		if f := fs[m.cursor]; f.flip != nil {
			d := 1
			if s != "right" {
				d = -1
			}
			f.flip(&m, d)
			m.saveCfg()
			if m.cursor == 0 {
				return m, m.loadTypes()
			}
		}
	case "enter", " ":
		f := fs[m.cursor]
		if f.set != nil {
			m.editing, m.editKind = true, "cfg"
			m.input.SetValue(f.get(m))
			m.input.CursorEnd()
			m.input.Focus()
		} else if f.flip != nil {
			f.flip(&m, 1)
			m.saveCfg()
		}
	}
	return m, nil
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-init" {
		root, _ := os.Getwd()
		p := filepath.Join(root, "config.json")
		if _, err := os.Stat(p); err == nil {
			fmt.Println("config.json already exists:", p)
			return
		}
		loadConfig(p).save(p)
		fmt.Println("wrote", p)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "-dry-launch" {
		// Runs the whole launch path, but EC2 only validates run-instances. Nothing is started.
		m := newModel()
		j := &job{c: m.cfg, root: m.root, ctx: context.Background(), dry: true,
			out: func(s string) { fmt.Println(time.Now().Format("15:04:05"), s) }}
		out, err := j.launch(false)
		fmt.Println("result:", out, err)
		if err != nil {
			os.Exit(1)
		}
		return
	}
	p := tea.NewProgram(newModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
}

// phaseText is the plain-English description of a boot phase.
func phaseText(p string) string {
	switch p {
	case "booting":
		return "Instance booted, starting setup"
	case "attaching":
		return "Waiting for the model volume to attach"
	case "syncing":
		return "Copying models from S3"
	case "starting":
		return "Downloading the server program (Docker image) and starting it"
	case "ready":
		return "Ready. Safe to connect"
	}
	return "Starting up"
}

// stepText prefers the step the server reports, else the phase text.
func (mt Metrics) stepText() string {
	if mt.Step != "" {
		return mt.Step
	}
	return phaseText(mt.Phase)
}

// unusedVols counts this project's volumes that no server is using.
func (m model) unusedVols() int {
	n := 0
	for _, v := range m.vols {
		if v.State == "available" {
			n++
		}
	}
	return n
}

// EBS gp3 limits and London prices (per month), for the cost estimate. Prices checked against the AWS Pricing API.
const (
	gp3MinIOPS, gp3MaxIOPS = 3000, 16000
	gp3MinTput, gp3MaxTput = 125, 1000
	priceGB, priceIOPS     = 0.0928, 0.0058
	priceTput              = 0.0464
)

func (v ModelVolume) monthlyUSD() float64 {
	c := float64(v.SizeGB) * priceGB
	if v.IOPS > gp3MinIOPS {
		c += float64(v.IOPS-gp3MinIOPS) * priceIOPS
	}
	if v.ThroughputMiBs > gp3MinTput {
		c += float64(v.ThroughputMiBs-gp3MinTput) * priceTput
	}
	return c
}

// setVolPerf applies new IOPS and throughput, keeping them inside what gp3 allows.
// gp3 allows at most 0.25 MiB/s of throughput per provisioned IOPS.
func (m *model) setVolPerf(iops, tput int) {
	iops = min(max(iops, gp3MinIOPS), gp3MaxIOPS)
	tput = min(max(tput, gp3MinTput), gp3MaxTput)
	if tput*4 > iops {
		if tput*4 <= gp3MaxIOPS {
			iops = tput * 4
		} else {
			tput = iops / 4
		}
		m.logf("gp3 allows 0.25 MiB/s per IOPS, adjusted to %d IOPS and %d MiB/s", iops, tput)
	}
	m.cfg.ModelVolume.IOPS, m.cfg.ModelVolume.ThroughputMiBs = iops, tput
}

// trackLoad watches GPU memory fill while the GPU is idle: that is a model loading from disk.
// The server does not report progress, so the total comes from the size seen at the end of the last load.
func (m *model) trackLoad(mt Metrics) {
	now := time.Now()
	if !m.ldRead {
		m.ldRead = true
		if b, err := os.ReadFile(filepath.Join(m.root, ".state", "gpu_load_mib")); err == nil {
			m.ldTarget, _ = strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
		}
	}
	if !m.ldPrevT.IsZero() {
		if dt := now.Sub(m.ldPrevT).Seconds(); dt > 0.5 {
			m.ldRate = 0.6*m.ldRate + 0.4*(mt.VRAM-m.ldPrev)/dt
		}
	}
	m.ldPrev, m.ldPrevT = mt.VRAM, now
	idle := mt.GPU < m.cfg.GPUIdlePct
	switch {
	case !m.loading && mt.Phase == "ready" && idle && m.ldRate > 20:
		m.loading, m.ldStart = true, now
		m.logf("loading models from disk onto the GPU")
	case m.loading && (!idle || m.ldRate < 5):
		m.loading = false
		secs := int(now.Sub(m.ldStart).Seconds())
		m.logf("models on the GPU: %.1f GiB after %s", mt.VRAM/1024, dur(secs))
		if !idle && mt.VRAM > 4096 {
			m.ldTarget = mt.VRAM
			_ = os.WriteFile(filepath.Join(m.root, ".state", "gpu_load_mib"), []byte(fmt.Sprintf("%.0f", mt.VRAM)), 0o644)
		}
	}
}

// stageOf names what the server is doing right now, from the measured numbers.
func (m model) stageOf(mt Metrics) string {
	switch {
	case mt.GPU >= m.cfg.GPUIdlePct:
		return "computing on the GPU"
	case m.loading:
		return "loading a model onto the GPU"
	case mt.NetTx > 5:
		return "sending data to your Mac"
	case mt.SrvCPU >= 60:
		return "CPU busy (preparing)"
	case mt.DiskRead > 20:
		return "reading the model disk"
	}
	return "idle"
}

// trackStage logs every change of stage with how long the last one took, and a breakdown when a job ends.
func (m *model) trackStage(mt Metrics) {
	if mt.Phase != "ready" {
		return
	}
	now := time.Now()
	s := m.stageOf(mt)
	if m.stage == "" {
		m.stage, m.stageT = s, now
	}
	if s != m.stage {
		if s == m.stageCand {
			m.stageN++
		} else {
			m.stageCand, m.stageN = s, 1
		}
		if m.stageN >= 2 { // two samples in a row, about 4 seconds, so blips do not flap
			took := int(now.Sub(m.stageT).Seconds())
			if m.stage != "idle" && m.winOn {
				m.winSecs[m.stage] += took
			}
			if s != "idle" && !m.winOn {
				m.winOn, m.winStart, m.winSecs = true, now, map[string]int{}
				m.logf("job activity started")
			}
			m.logf("stage: %s took %s. Now: %s", m.stage, dur(took), s)
			m.stage, m.stageT, m.stageCand, m.stageN = s, now, "", 0
		}
	} else {
		m.stageCand, m.stageN = "", 0
	}
	if m.stage == "idle" {
		if m.idleFrom.IsZero() {
			m.idleFrom = now
		}
		if m.winOn && now.Sub(m.idleFrom) > 45*time.Second {
			m.winOn = false
			total := int(now.Sub(m.winStart).Seconds()) - 45
			var parts []string
			for _, k := range []string{"CPU busy (preparing)", "reading the model disk", "loading a model onto the GPU", "computing on the GPU", "sending data to your Mac"} {
				if v := m.winSecs[k]; v > 0 {
					parts = append(parts, fmt.Sprintf("%s %s", k, dur(v)))
				}
			}
			m.lastWin = fmt.Sprintf("total %s: %s", dur(total), strings.Join(parts, ", "))
			m.logf("JOB BREAKDOWN %s", m.lastWin)
		}
	} else {
		m.idleFrom = time.Time{}
	}
}
