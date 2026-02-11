package tui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
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
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
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
	appTitle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#FFE082")).
		Render("llm-proxy")
	subtitle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#94A3B8")).
		Render("OpenAI-compatible bridge for Claude CLI + Codex CLI")

	statusColor := lipgloss.Color("#22C55E")
	statusText := "running"
	if !m.running {
		statusColor = lipgloss.Color("#EF4444")
		statusText = "stopped"
	}
	status := lipgloss.NewStyle().
		Bold(true).
		Foreground(statusColor).
		Render(statusText)
	yoloText := "off"
	yoloColor := lipgloss.Color("#64748B")
	if m.yolo {
		yoloText = "ON"
		yoloColor = lipgloss.Color("#F97316")
	}
	yoloChip := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#0B1020")).
		Background(yoloColor).
		Padding(0, 1).
		Render(" YOLO " + yoloText + " ")
	statusChip := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#0B1020")).
		Background(statusColor).
		Padding(0, 1).
		Render(" " + statusText + " ")

	uptime := time.Since(m.startedAt).Truncate(time.Second)
	titleBar := lipgloss.NewStyle().
		Background(lipgloss.Color("#172554")).
		Foreground(lipgloss.Color("#E2E8F0")).
		Padding(0, 1).
		Render(fmt.Sprintf("%s %s  %s  %s", m.spin.View(), appTitle, statusChip, yoloChip))
	header := lipgloss.JoinVertical(lipgloss.Left, titleBar, subtitle)
	if m.yolo {
		yoloWarning := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FDBA74")).
			Render("YOLO enabled: permission prompts and sandbox checks are bypassed in upstream CLIs.")
		header = lipgloss.JoinVertical(lipgloss.Left, header, yoloWarning)
	}

	cardTitle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#93C5FD"))
	label := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#94A3B8"))
	value := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#E2E8F0"))

	serviceBody := lipgloss.JoinVertical(lipgloss.Left,
		cardTitle.Render("Service"),
		fmt.Sprintf("%s %s", label.Render("Status:"), status),
		fmt.Sprintf("%s %s", label.Render("YOLO mode:"), value.Render(yoloText)),
		fmt.Sprintf("%s %s", label.Render("Address:"), value.Render("http://127.0.0.1"+m.addr)),
		fmt.Sprintf("%s %s", label.Render("Uptime:"), value.Render(uptime.String())),
	)
	trafficBody := lipgloss.JoinVertical(lipgloss.Left,
		cardTitle.Render("Traffic"),
		fmt.Sprintf("%s %s", label.Render("Requests:"), value.Render(fmt.Sprintf("%d", m.snap.RequestsTotal))),
		fmt.Sprintf("%s %s", label.Render("Errors:"), value.Render(fmt.Sprintf("%d", m.snap.ErrorsTotal))),
		fmt.Sprintf("%s %s", label.Render("In flight:"), value.Render(fmt.Sprintf("%d", m.snap.InFlight))),
		fmt.Sprintf("%s %s", label.Render("Rate (req/s):"), value.Render(fmt.Sprintf("%d", m.reqsPerSec))),
		fmt.Sprintf("%s %s", label.Render("Bytes out:"), value.Render(humanBytes(m.snap.BytesSent))),
	)
	httpBody := lipgloss.JoinVertical(lipgloss.Left,
		cardTitle.Render("HTTP"),
		fmt.Sprintf("%s %s", label.Render("2xx:"), value.Render(strconv.FormatUint(m.snap.Status2xx, 10))),
		fmt.Sprintf("%s %s", label.Render("3xx:"), value.Render(strconv.FormatUint(m.snap.Status3xx, 10))),
		fmt.Sprintf("%s %s", label.Render("4xx:"), value.Render(strconv.FormatUint(m.snap.Status4xx, 10))),
		fmt.Sprintf("%s %s", label.Render("5xx:"), value.Render(strconv.FormatUint(m.snap.Status5xx, 10))),
		fmt.Sprintf("%s %s", label.Render("Avg latency:"), value.Render(fmt.Sprintf("%.1f ms", m.snap.AvgLatencyMs))),
		fmt.Sprintf("%s %s", label.Render("Max latency:"), value.Render(fmt.Sprintf("%.1f ms", m.snap.MaxLatencyMs))),
	)
	endpointsBody := lipgloss.JoinVertical(lipgloss.Left,
		cardTitle.Render("Endpoints"),
		fmt.Sprintf("%s %s", label.Render("/v1/models:"), value.Render(strconv.FormatUint(m.snap.ModelsTotal, 10))),
		fmt.Sprintf("%s %s", label.Render("/v1/chat/completions:"), value.Render(strconv.FormatUint(m.snap.ChatCompletionsTotal, 10))),
		fmt.Sprintf("%s %s", label.Render("/v1/responses:"), value.Render(strconv.FormatUint(m.snap.ResponsesTotal, 10))),
		fmt.Sprintf("%s %s", label.Render("Other:"), value.Render(strconv.FormatUint(m.snap.OtherTotal, 10))),
	)
	modelsBody := lipgloss.JoinVertical(lipgloss.Left,
		cardTitle.Render("Per-model"),
		renderModelStats(m.snap.Models),
	)

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#334155")).
		Padding(1, 2)
	serviceCard := box.Render(serviceBody)
	trafficCard := box.Render(trafficBody)
	httpCard := box.Render(httpBody)
	endpointsCard := box.Render(endpointsBody)
	modelsCard := box.Render(modelsBody)

	mainPane := lipgloss.JoinHorizontal(lipgloss.Top, serviceCard, "  ", trafficCard)
	secondaryPane := lipgloss.JoinHorizontal(lipgloss.Top, httpCard, "  ", endpointsCard)
	if m.width > 0 && m.width < 96 {
		mainPane = lipgloss.JoinVertical(lipgloss.Left, serviceCard, "", trafficCard)
		secondaryPane = lipgloss.JoinVertical(lipgloss.Left, httpCard, "", endpointsCard)
	}

	errorBlock := ""
	if m.lastErr != "" {
		errorBlock = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#B91C1C")).
			Padding(0, 1).
			Foreground(lipgloss.Color("#FCA5A5")).
			Render("Server error: " + m.lastErr)
	}

	footer := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#94A3B8")).
		Render("[ y ] toggle YOLO   [ q ] quit   [ ctrl+c ] quit and stop proxy")
	panelBody := lipgloss.JoinVertical(lipgloss.Left, header, "", mainPane, "", secondaryPane, "", modelsCard)
	if errorBlock != "" {
		panelBody = lipgloss.JoinVertical(lipgloss.Left, panelBody, "", errorBlock)
	}
	panelBody = lipgloss.JoinVertical(lipgloss.Left, panelBody, "", footer)
	panel := lipgloss.NewStyle().Padding(1, 2).Render(panelBody)

	viewText := panel
	if m.width > 0 {
		viewText = lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, panel)
	}
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

func renderModelStats(models []api.ModelStats) string {
	if len(models) == 0 {
		return "No model traffic yet."
	}
	var b strings.Builder
	b.WriteString("Model                          Req  Err  Chat Resp Other\n")
	for _, s := range models {
		row := fmt.Sprintf("%-30s %4d %4d %4d %4d %5d",
			s.Model,
			s.RequestsTotal,
			s.ErrorsTotal,
			s.ChatCompletions,
			s.Responses,
			s.OtherRequests,
		)
		b.WriteString(row)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}
