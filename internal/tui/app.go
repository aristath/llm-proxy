package tui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"llm-proxy/internal/api"
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

	width  int
	height int
	spin   spinner.Model
	snap   api.MetricsSnapshot
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
		}
	case tickMsg:
		m.snap = m.metrics.Snapshot()
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
	title := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("212")).
		Render("llm-proxy monitor")

	statusColor := lipgloss.Color("42")
	statusText := "running"
	if !m.running {
		statusColor = lipgloss.Color("196")
		statusText = "stopped"
	}
	status := lipgloss.NewStyle().
		Bold(true).
		Foreground(statusColor).
		Render(statusText)

	uptime := time.Since(m.startedAt).Truncate(time.Second)
	body := []string{
		fmt.Sprintf("%s %s", m.spin.View(), title),
		"",
		fmt.Sprintf("Status: %s", status),
		fmt.Sprintf("Address: http://127.0.0.1%s", m.addr),
		fmt.Sprintf("Uptime: %s", uptime),
		"",
		fmt.Sprintf("Requests total: %d", m.snap.RequestsTotal),
		fmt.Sprintf("Errors total:   %d", m.snap.ErrorsTotal),
		fmt.Sprintf("In flight:      %d", m.snap.InFlight),
	}

	if m.lastErr != "" {
		errLine := lipgloss.NewStyle().
			Foreground(lipgloss.Color("196")).
			Render("Server error: " + m.lastErr)
		body = append(body, "", errLine)
	}

	footer := lipgloss.NewStyle().
		Foreground(lipgloss.Color("244")).
		Render("Press q to quit. Quitting stops the proxy server.")
	body = append(body, "", footer)

	panel := lipgloss.NewStyle().
		Padding(1, 2).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("63")).
		Render(lipgloss.JoinVertical(lipgloss.Left, body...))

	viewText := panel
	if m.width > 0 {
		viewText = lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, panel)
	}
	v := tea.NewView(viewText)
	v.AltScreen = true
	return v
}
