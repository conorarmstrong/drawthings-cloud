package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The first-run wizard asks for the few settings that have no sensible default, then writes config.json.

const (
	wzBucket = iota
	wzRegion
	wzDir
	wzModels
	wzRegions
	wzSpot
	wzDone
)

type modelFile struct {
	name string
	size int64
	on   bool
}

type wizard struct {
	step     int
	input    textinput.Model
	msg      string // error or hint for the current step
	checking bool   // waiting for the bucket region lookup
	files    []modelFile
	cur      int
	keepDir  string // a missing folder the user chose to keep anyway
}

type bucketRegionMsg struct {
	region string
	err    error
}

func newWizard(c Config) *wizard {
	ti := textinput.New()
	ti.CharLimit = 300
	ti.Width = 70
	ti.Focus()
	w := &wizard{input: ti}
	w.input.SetValue(c.Bucket)
	return w
}

// enterStep prepares the input for step s from the current config.
func (w *wizard) enterStep(c Config, s int) {
	w.step, w.msg = s, ""
	switch s {
	case wzRegion:
		w.input.SetValue(onelse(c.BucketRegion, "eu-west-2"))
	case wzDir:
		w.input.SetValue(c.LocalModels)
	case wzRegions:
		w.input.SetValue(strings.Join(c.Regions, ","))
	case wzModels:
		w.loadFiles(c)
	}
	w.input.CursorEnd()
}

// loadFiles lists the model files in the local folder, ticking any already in the config.
func (w *wizard) loadFiles(c Config) {
	chosen := map[string]bool{}
	for _, f := range c.Models {
		chosen[f] = true
	}
	w.files, w.cur = nil, 0
	ents, err := os.ReadDir(c.LocalModels)
	if err != nil {
		w.msg = "Could not read the folder. Type the file names, comma separated."
		w.input.SetValue(strings.Join(c.Models, ","))
		return
	}
	for _, e := range ents {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		w.files = append(w.files, modelFile{e.Name(), info.Size(), chosen[e.Name()]})
	}
	sort.Slice(w.files, func(a, b int) bool { return w.files[a].name < w.files[b].name })
	if len(w.files) == 0 {
		w.msg = "The folder has no files. Type the file names, comma separated."
		w.input.SetValue(strings.Join(c.Models, ","))
	}
}

func splitList(v string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (m model) wizardKey(k tea.KeyMsg) (model, tea.Cmd) {
	w := m.wiz
	s := k.String()
	if s == "ctrl+c" {
		return m, tea.Quit
	}
	if w.checking {
		return m, nil
	}
	if s == "esc" && w.step > wzBucket {
		back := w.step - 1
		if back == wzRegion && m.cfg.BucketRegion != "" && w.msg == "" {
			back = wzBucket
		}
		w.enterStep(m.cfg, back)
		if back == wzBucket {
			w.input.SetValue(m.cfg.Bucket)
		}
		return m, nil
	}
	picking := w.step == wzModels && len(w.files) > 0
	switch {
	case picking && (s == "up" || s == "k"):
		w.cur = max(0, w.cur-1)
		return m, nil
	case picking && (s == "down" || s == "j"):
		w.cur = min(len(w.files)-1, w.cur+1)
		return m, nil
	case picking && s == " ":
		w.files[w.cur].on = !w.files[w.cur].on
		return m, nil
	case w.step == wzSpot && (s == "left" || s == "right" || s == " " || s == "h" || s == "l"):
		m.cfg.Spot = !m.cfg.Spot
		return m, nil
	case s == "enter":
		return m.wizardNext()
	}
	if w.step != wzSpot && w.step != wzDone && !picking {
		var c tea.Cmd
		w.input, c = w.input.Update(k)
		return m, c
	}
	return m, nil
}

// wizardNext checks the current step's answer and moves on.
func (m model) wizardNext() (model, tea.Cmd) {
	w := m.wiz
	v := strings.TrimSpace(w.input.Value())
	switch w.step {
	case wzBucket:
		if v == "" {
			w.msg = "A bucket name is needed. Create one in the S3 console first if you have none."
			return m, nil
		}
		m.cfg.Bucket, w.checking, w.msg = v, true, "Looking up the bucket..."
		return m, func() tea.Msg { r, err := detectBucketRegion(v); return bucketRegionMsg{r, err} }
	case wzRegion:
		if v == "" {
			return m, nil
		}
		m.cfg.BucketRegion = v
		w.enterStep(m.cfg, wzDir)
	case wzDir:
		m.cfg.LocalModels = expandHome(v)
		if st, err := os.Stat(m.cfg.LocalModels); (err != nil || !st.IsDir()) && w.keepDir != v {
			w.msg, w.keepDir = "That folder does not exist. Press enter again to keep it anyway.", v
			return m, nil
		}
		w.enterStep(m.cfg, wzModels)
	case wzModels:
		var picked []string
		if len(w.files) > 0 {
			for _, f := range w.files {
				if f.on {
					picked = append(picked, f.name)
				}
			}
		} else {
			picked = splitList(v)
		}
		if len(picked) == 0 {
			w.msg = "Pick at least one file. Space ticks a file."
			return m, nil
		}
		m.cfg.Models = picked
		w.enterStep(m.cfg, wzRegions)
	case wzRegions:
		if rs := splitList(v); len(rs) > 0 {
			m.cfg.Regions, m.cfg.Region = rs, rs[0]
		}
		w.enterStep(m.cfg, wzSpot)
	case wzSpot:
		w.enterStep(m.cfg, wzDone)
	case wzDone:
		m.wiz = nil
		m.saveCfg()
		m.logf("config.json written. Upload any missing models on the Models tab, then press l to launch.")
		m.tab = tabModels
		return m, m.refreshAll()
	}
	return m, nil
}

func (m model) wizardBucket(msg bucketRegionMsg) model {
	w := m.wiz
	w.checking = false
	if msg.err != nil {
		w.enterStep(m.cfg, wzRegion)
		w.msg = "Could not look up the bucket (" + truncate(msg.err.Error(), 120) + "). Type its region, or esc to change the name."
		return m
	}
	m.cfg.BucketRegion = msg.region
	w.enterStep(m.cfg, wzDir)
	w.msg = fmt.Sprintf("Found %s in %s.", m.cfg.Bucket, msg.region)
	return m
}

func (m model) viewWizard() string {
	w := m.wiz
	width := min(m.w-2, 110)
	titles := []string{"S3 bucket", "Bucket region", "Models folder on this Mac", "Models the server needs", "Regions", "Spot or on-demand", "Check and save"}
	var l []string
	l = append(l, sDim.Render(fmt.Sprintf("Step %d of %d", w.step+1, len(titles))), sBold.Render(titles[w.step]), "")
	switch w.step {
	case wzBucket:
		l = append(l, "The bucket that holds your models under models/. It must already exist.", "", w.input.View())
	case wzRegion:
		l = append(l, "The AWS region the bucket is in, for example eu-west-2.", "", w.input.View())
	case wzDir:
		l = append(l, "Where Draw Things keeps its models on this Mac. The default is the Draw Things app folder.", "", w.input.View())
	case wzModels:
		if len(w.files) == 0 {
			l = append(l, "File names from the models folder, comma separated.", "", w.input.View())
			break
		}
		l = append(l, "Tick every file the server needs: the model, its encoders, LoRAs, upscaler and custom*.json.", "")
		n := max(5, m.h-16)
		start := max(0, min(w.cur-n/2, len(w.files)-n))
		var total int64
		picked := 0
		for _, f := range w.files {
			if f.on {
				total += f.size
				picked++
			}
		}
		for i := start; i < min(len(w.files), start+n); i++ {
			f := w.files[i]
			box := "[ ]"
			if f.on {
				box = sOK.Render("[x]")
			}
			line := fmt.Sprintf("%s %-64s %10s", box, truncate(f.name, 64), gib(f.size))
			if i == w.cur {
				line = lipgloss.NewStyle().Foreground(cPurple).Bold(true).Render("> ") + line
			} else {
				line = "  " + line
			}
			l = append(l, line)
		}
		l = append(l, "", sDim.Render(fmt.Sprintf("%d of %d files picked, %s. A GPU needs a little more VRAM than this.", picked, len(w.files), gib(total))))
	case wzRegions:
		l = append(l, "Regions you are happy to run in, comma separated. Each launch tries them cheapest first.",
			sDim.Render("Regions far from the bucket pay for S3 transfer on a cold boot."), "", w.input.View())
	case wzSpot:
		l = append(l, "Spot is about 70% cheaper, but AWS can reclaim it with 2 minutes notice and capacity is often short.", "",
			"  "+pill(onoff(m.cfg.Spot, "spot", "on-demand"), cBg, cCyan)+sDim.Render("   left/right to change"))
	case wzDone:
		c := m.cfg
		l = append(l,
			fmt.Sprintf("Bucket    s3://%s/models/  (%s)", c.Bucket, c.BucketRegion),
			fmt.Sprintf("Models    %d files from %s", len(c.Models), c.LocalModels),
			fmt.Sprintf("Regions   %s", strings.Join(c.Regions, ", ")),
			fmt.Sprintf("Market    %s, %s preferred", market(c.Spot), c.InstanceType),
			fmt.Sprintf("Shutdown  after %d min idle, at most %d h", c.IdleMin, c.MaxHours),
			"", sDim.Render("Enter writes config.json. Every other setting has a default and can be changed on the Config tab or in the file."))
	}
	if w.msg != "" {
		st := sWarn
		if strings.HasPrefix(w.msg, "Found") || strings.HasPrefix(w.msg, "Looking") {
			st = sOK
		}
		l = append(l, "", st.Render(w.msg))
	}
	keys := "enter next   esc back   ctrl+c quit"
	if w.step == wzModels && len(w.files) > 0 {
		keys = "up/down move   space tick   enter next   esc back"
	}
	head := sTitle.Render(" Draw Things on AWS ") + pill("FIRST RUN SETUP", cBg, cCyan)
	return head + "\n\n" + panel("Setup", width, strings.Join(l, "\n"), cCyan) + "\n" + sDim.Render(" "+keys)
}
