package ui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"hop.top/foo/internal/llm"
)

type model struct {
	viewport    viewport.Model
	textinput   textinput.Model
	llm         *llm.Client
	ctx         context.Context
	history     []string
	err         error
	ready       bool
	placeholder string
}

func NewREPLModel(ctx context.Context, client *llm.Client) model {
	ti := textinput.New()
	ti.Placeholder = "Type a prompt..."
	ti.Focus()

	return model{
		textinput: ti,
		llm:       client,
		ctx:       ctx,
	}
}

func (m model) Init() tea.Cmd {
	return textinput.Blink
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		tiCmd tea.Cmd
		vpCmd tea.Cmd
	)

	m.textinput, tiCmd = m.textinput.Update(msg)
	m.viewport, vpCmd = m.viewport.Update(msg)

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			return m, tea.Quit
		case "enter":
			prompt := m.textinput.Value()
			if prompt == "" {
				return m, nil
			}
			m.textinput.SetValue("")
			m.history = append(m.history, fmt.Sprintf("> %s", prompt))
			m.viewport.SetContent(strings.Join(m.history, "\n"))
			m.viewport.GotoBottom()

			return m, func() tea.Msg {
				resp, err := m.llm.Prompt(m.ctx, prompt)
				if err != nil {
					return errMsg(err)
				}
				return responseMsg(resp)
			}
		}

	case tea.WindowSizeMsg:
		if !m.ready {
			m.viewport = viewport.New(
				viewport.WithWidth(msg.Width),
				viewport.WithHeight(msg.Height-3),
			)
			m.ready = true
		} else {
			m.viewport.SetWidth(msg.Width)
			m.viewport.SetHeight(msg.Height - 3)
		}

	case responseMsg:
		m.history = append(m.history, string(msg))
		m.viewport.SetContent(strings.Join(m.history, "\n"))
		m.viewport.GotoBottom()

	case errMsg:
		m.err = msg
	}

	return m, tea.Batch(tiCmd, vpCmd)
}

func (m model) View() tea.View {
	var view tea.View
	view.AltScreen = true

	if !m.ready {
		view.SetContent("Initializing...")
		return view
	}

	header := lipgloss.NewStyle().Bold(true).Render("foo REPL")
	footer := fmt.Sprintf("\n%s", m.textinput.View())

	out := fmt.Sprintf("%s\n%s\n%s", header, m.viewport.View(), footer)
	view.SetContent(out)
	return view
}

type responseMsg string
type errMsg error
