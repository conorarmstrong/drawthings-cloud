package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Session is one finished server run. Cost is an estimate (price at last poll x hours).
type Session struct {
	ID      string    `json:"id"`
	Type    string    `json:"type"`
	Spot    bool      `json:"spot"`
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
	CostUSD float64   `json:"cost_usd"`
}

func histPath(root string) string { return filepath.Join(root, ".state", "history.jsonl") }

func appendSession(root string, s Session) {
	_ = os.MkdirAll(filepath.Dir(histPath(root)), 0o755)
	f, err := os.OpenFile(histPath(root), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(s)
	_, _ = f.Write(append(b, '\n'))
}

func monthSpend(root string, now time.Time) float64 {
	b, err := os.ReadFile(histPath(root))
	if err != nil {
		return 0
	}
	var total float64
	for _, ln := range strings.Split(string(b), "\n") {
		var s Session
		if json.Unmarshal([]byte(ln), &s) == nil && s.End.Year() == now.Year() && s.End.Month() == now.Month() {
			total += s.CostUSD
		}
	}
	return total
}

// notify shows a macOS notification. An optional sound name (e.g. "Glass") plays with it.
func notify(c Config, title, text string, sound ...string) {
	if !c.Notifications {
		return
	}
	text, title = strings.ReplaceAll(text, `"`, "'"), strings.ReplaceAll(title, `"`, "'")
	script := `display notification "` + text + `" with title "` + title + `"`
	if len(sound) > 0 {
		script += ` sound name "` + sound[0] + `"`
	}
	_ = exec.Command("osascript", "-e", script).Start()
}
