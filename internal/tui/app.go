package tui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"llm-proxy/internal/api"
	"llm-proxy/internal/proxy"
)

type App struct {
	addr    string
	metrics *api.Metrics
	server  *http.Server
	errCh   <-chan error
}

func New(addr string, metrics *api.Metrics, server *http.Server, errCh <-chan error) *App {
	return &App{
		addr:    addr,
		metrics: metrics,
		server:  server,
		errCh:   errCh,
	}
}

func (a *App) Run() error {
	m := newModel(a.addr, a.metrics, a.errCh)
	p := tea.NewProgram(m)
	_, err := p.Run()
	return err
}

func (a *App) Shutdown(ctx context.Context) error {
	if a.server == nil {
		return nil
	}
	err := a.server.Shutdown(ctx)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

type tickMsg time.Time

type model struct {
	addr      string
	metrics   *api.Metrics
	errCh     <-chan error
	startedAt time.Time
	lastErr   string
	running   bool
	yolo      bool

	width      int
	height     int
	spin       spinner.Model
	snap       api.MetricsSnapshot
	prevReqs   uint64
	reqsPerSec uint64
}

func newModel(addr string, metrics *api.Metrics, errCh <-chan error) model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#89dceb"))
	return model{
		addr:      addr,
		metrics:   metrics,
		errCh:     errCh,
		startedAt: time.Now(),
		running:   true,
		yolo:      proxy.YOLOEnabled(),
		spin:      s,
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.spin.Tick, tickCmd())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "y":
			m.yolo = !m.yolo
			proxy.SetYOLO(m.yolo)
		}
	case tickMsg:
		m.snap = m.metrics.Snapshot()
		if m.snap.RequestsTotal >= m.prevReqs {
			m.reqsPerSec = m.snap.RequestsTotal - m.prevReqs
		}
		m.prevReqs = m.snap.RequestsTotal
		select {
		case err, ok := <-m.errCh:
			if ok && err != nil && !errors.Is(err, http.ErrServerClosed) {
				m.running = false
				m.lastErr = err.Error()
			}
		default:
		}
		cmds = append(cmds, tickCmd())
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

func (m model) View() tea.View {
	const (
		mochaMantle   = "#181825"
		mochaText     = "#cdd6f4"
		mochaSubtext  = "#bac2de"
		mochaBlue     = "#89b4fa"
		mochaGreen    = "#a6e3a1"
		mochaRed      = "#f38ba8"
		mochaYellow   = "#f9e2af"
		mochaPeach    = "#fab387"
		mochaSapphire = "#74c7ec"
		mochaOverlay  = "#6c7086"
	)

	appTitle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(mochaYellow)).
		Render("llm-proxy")
	subtitle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(mochaSubtext)).
		Render("OpenAI-compatible bridge for Claude CLI + Codex CLI")

	statusColor := lipgloss.Color(mochaGreen)
	statusText := "running"
	if !m.running {
		statusColor = lipgloss.Color(mochaRed)
		statusText = "stopped"
	}
	status := lipgloss.NewStyle().
		Bold(true).
		Foreground(statusColor).
		Render(statusText)
	yoloText := "off"
	yoloColor := lipgloss.Color(mochaOverlay)
	if m.yolo {
		yoloText = "ON"
		yoloColor = lipgloss.Color(mochaPeach)
	}
	yoloChip := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(mochaMantle)).
		Background(yoloColor).
		Padding(0, 1).
		Render(" YOLO " + yoloText + " ")
	statusChip := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(mochaMantle)).
		Background(statusColor).
		Padding(0, 1).
		Render(" " + statusText + " ")

	uptime := time.Since(m.startedAt).Truncate(time.Second)
	titleBar := lipgloss.NewStyle().
		Background(lipgloss.Color(mochaMantle)).
		Foreground(lipgloss.Color(mochaText)).
		Padding(0, 1).
		Render(fmt.Sprintf("%s %s  %s  %s", m.spin.View(), appTitle, statusChip, yoloChip))
	header := lipgloss.JoinVertical(lipgloss.Left, titleBar, subtitle)
	if m.yolo {
		yoloWarning := lipgloss.NewStyle().
			Foreground(lipgloss.Color(mochaPeach)).
			Render("YOLO enabled: permission prompts and sandbox checks are bypassed in upstream CLIs.")
		header = lipgloss.JoinVertical(lipgloss.Left, header, yoloWarning)
	}

	sectionTitle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(mochaBlue))
	label := lipgloss.NewStyle().
		Foreground(lipgloss.Color(mochaSubtext))
	value := lipgloss.NewStyle().
		Foreground(lipgloss.Color(mochaText))
	sepWidth := 80
	if m.width > 0 {
		sepWidth = m.width
	}
	separator := lipgloss.NewStyle().
		Foreground(lipgloss.Color(mochaOverlay)).
		Render(strings.Repeat("─", sepWidth))

	serviceBody := lipgloss.JoinVertical(lipgloss.Left,
		sectionTitle.Render("Service"),
		fmt.Sprintf("%s %s", label.Render("Status:"), status),
		fmt.Sprintf("%s %s", label.Render("YOLO mode:"), value.Render(yoloText)),
		fmt.Sprintf("%s %s", label.Render("Address:"), value.Render("http://127.0.0.1"+m.addr)),
		fmt.Sprintf("%s %s", label.Render("Uptime:"), value.Render(uptime.String())),
	)
	trafficBody := lipgloss.JoinVertical(lipgloss.Left,
		sectionTitle.Render("Traffic"),
		fmt.Sprintf("%s %s", label.Render("Requests:"), value.Render(fmt.Sprintf("%d", m.snap.RequestsTotal))),
		fmt.Sprintf("%s %s", label.Render("Errors:"), value.Render(fmt.Sprintf("%d", m.snap.ErrorsTotal))),
		fmt.Sprintf("%s %s", label.Render("In flight:"), value.Render(fmt.Sprintf("%d", m.snap.InFlight))),
		fmt.Sprintf("%s %s", label.Render("Rate (req/s):"), value.Render(fmt.Sprintf("%d", m.reqsPerSec))),
		fmt.Sprintf("%s %s", label.Render("Bytes out:"), value.Render(humanBytes(m.snap.BytesSent))),
		fmt.Sprintf("%s %s", label.Render("Avg latency:"), value.Render(fmt.Sprintf("%.1f ms", m.snap.AvgLatencyMs))),
		fmt.Sprintf("%s %s", label.Render("Max latency:"), value.Render(fmt.Sprintf("%.1f ms", m.snap.MaxLatencyMs))),
	)
	modelsBody := lipgloss.JoinVertical(lipgloss.Left,
		sectionTitle.Render("Model Stats"),
		renderModelStatsTable(m.snap.Models),
	)

	errorBlock := ""
	if m.lastErr != "" {
		errorBlock = lipgloss.NewStyle().
			Foreground(lipgloss.Color(mochaRed)).
			Render("Server error: " + m.lastErr)
	}

	footer := lipgloss.NewStyle().
		Foreground(lipgloss.Color(mochaSapphire)).
		Render("[ y ] toggle YOLO   [ q ] quit   [ ctrl+c ] quit and stop proxy")

	panelBody := lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		separator,
		serviceBody,
		separator,
		trafficBody,
		separator,
		modelsBody,
	)
	if errorBlock != "" {
		panelBody = lipgloss.JoinVertical(lipgloss.Left, panelBody, separator, errorBlock)
	}
	panelBody = lipgloss.JoinVertical(lipgloss.Left, panelBody, separator, footer)
	panelStyle := lipgloss.NewStyle().
		Background(lipgloss.Color(mochaMantle)).
		Padding(1, 2)
	if m.width > 0 {
		panelStyle = panelStyle.Width(m.width)
	}
	if m.height > 0 {
		panelStyle = panelStyle.Height(m.height)
	}
	viewText := panelStyle.Render(panelBody)
	v := tea.NewView(viewText)
	v.AltScreen = true
	return v
}

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for val := n / unit; val >= unit; val /= unit {
		div *= unit
		exp++
	}
	suffixes := []string{"KiB", "MiB", "GiB", "TiB"}
	return fmt.Sprintf("%.2f %s", float64(n)/float64(div), suffixes[exp])
}

func renderModelStatsTable(models []api.ModelStats) string {
	if len(models) == 0 {
		return "No model traffic yet."
	}

	const modelWidth = 30
	trim := func(s string) string {
		r := []rune(strings.TrimSpace(s))
		if len(r) <= modelWidth {
			return string(r)
		}
		if modelWidth <= 1 {
			return string(r[:modelWidth])
		}
		return string(r[:modelWidth-1]) + "…"
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%-*s %8s %10s %18s %16s\n",
		modelWidth, "Model", "Requests", "Tokens", "Avg Time/Response", "Avg Tokens/Call"))
	b.WriteString(strings.Repeat("─", modelWidth+8+10+18+16+4))
	b.WriteByte('\n')
	for _, s := range models {
		row := fmt.Sprintf("%-*s %8d %10d %17.1fms %16.1f",
			modelWidth,
			trim(s.Model),
			s.RequestsTotal,
			s.TokensTotal,
			s.AvgLatencyMs,
			s.AvgTokensPerCall,
		)
		b.WriteString(row)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}
