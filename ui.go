package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	cBg     = lipgloss.Color("#1a1b26")
	cFg     = lipgloss.Color("#c0caf5")
	cDim    = lipgloss.Color("#565f89")
	cGreen  = lipgloss.Color("#9ece6a")
	cYellow = lipgloss.Color("#e0af68")
	cRed    = lipgloss.Color("#f7768e")
	cBlue   = lipgloss.Color("#7aa2f7")
	cPurple = lipgloss.Color("#bb9af7")
	cCyan   = lipgloss.Color("#7dcfff")

	sTitle = lipgloss.NewStyle().Bold(true).Foreground(cPurple)
	sDim   = lipgloss.NewStyle().Foreground(cDim)
	sBold  = lipgloss.NewStyle().Bold(true).Foreground(cFg)
	sOK    = lipgloss.NewStyle().Foreground(cGreen)
	sWarn  = lipgloss.NewStyle().Foreground(cYellow)
	sBad   = lipgloss.NewStyle().Foreground(cRed)
	sKey   = lipgloss.NewStyle().Bold(true).Foreground(cCyan)
)

func panel(title string, w int, body string, accent lipgloss.Color) string {
	t := lipgloss.NewStyle().Bold(true).Foreground(accent).Render(title)
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).
		Padding(0, 1).Width(w).Render(t + "\n" + body)
}

func pill(text string, fg, bg lipgloss.Color) string {
	return lipgloss.NewStyle().Bold(true).Foreground(fg).Background(bg).Padding(0, 1).Render(text)
}

func heat(pct float64) lipgloss.Color {
	switch {
	case pct < 60:
		return cGreen
	case pct < 85:
		return cYellow
	}
	return cRed
}

func bar(pct float64, w int, col lipgloss.Color) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	f := int(pct / 100 * float64(w))
	return lipgloss.NewStyle().Foreground(col).Render(strings.Repeat("█", f)) +
		lipgloss.NewStyle().Foreground(cDim).Render(strings.Repeat("░", w-f))
}

func gauge(label string, pct float64, w int, extra string) string {
	return fmt.Sprintf("%-6s %s %5.1f%%  %s", label, bar(pct, w, heat(pct)), pct, sDim.Render(extra))
}

func spark(h []int, w int) string {
	blocks := []rune(" ▁▂▃▄▅▆▇█")
	if len(h) > w {
		h = h[len(h)-w:]
	}
	var b strings.Builder
	for _, v := range h {
		i := v * 8 / 100
		if i > 8 {
			i = 8
		}
		b.WriteRune(blocks[i])
	}
	return lipgloss.NewStyle().Foreground(cCyan).Render(b.String()) + strings.Repeat(" ", max(0, w-len(h)))
}

func dur(secs int) string {
	if secs < 0 {
		secs = 0
	}
	return fmt.Sprintf("%d:%02d:%02d", secs/3600, secs%3600/60, secs%60)
}

func gib(b int64) string { return fmt.Sprintf("%.2f GiB", float64(b)/(1<<30)) }
