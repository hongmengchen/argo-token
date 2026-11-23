package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/hongmengchen/tui/theme"
)

var styles = theme.Default

type tuiState int

const (
	stateForm tuiState = iota
	stateWorking
	stateDone
)

// fieldLabels 与控制 model.fields 的下标一一对应。
var fieldLabels = []string{"服务器", "token", "保存到"}

type savedMsg struct {
	cfg              *Config
	cfgPath, envPath string
	id               Identity
	verified         bool
	masked           string
	err              error
}

type model struct {
	cfg     *Config
	fields  []textinput.Model
	focus   int
	grpcWeb bool

	state     tuiState
	spin      spinner.Model
	verifying bool

	hasToken bool
	masked   string

	note    string
	noteBad bool

	cfgPath, envPath string
	id               Identity
	verified         bool

	width int
}

func newModel() model {
	cfg, cfgPath, _, err := loadConfig()
	if cfg == nil {
		cfg = defaultConfig()
	}
	if cfgPath == "" {
		cfgPath, _ = configPath()
	}
	envP, _ := envPath()

	note := ""
	if err != nil {
		note = "读取旧配置失败，已按默认值打开: " + err.Error()
	}
	hasToken := false
	masked := ""
	if token, tokErr := readToken(cfg.TokenFile); tokErr == nil {
		hasToken = true
		masked = maskToken(token)
	}

	server := textinput.New()
	server.Prompt = styles.Accent.Render("› ")
	server.Placeholder = "https://argocd.example.com"
	server.CharLimit = 512
	server.SetValue(cfg.Server)

	tokenIn := textinput.New()
	tokenIn.Prompt = styles.Accent.Render("› ")
	tokenIn.EchoMode = textinput.EchoPassword
	tokenIn.EchoCharacter = '•'
	tokenIn.CharLimit = 4096
	if hasToken {
		tokenIn.Placeholder = "留空 = 沿用现有 token"
	} else {
		tokenIn.Placeholder = "粘贴 token（不回显）"
	}

	pathIn := textinput.New()
	pathIn.Prompt = styles.Accent.Render("› ")
	pathIn.Placeholder = "~/.argocd/token"
	pathIn.CharLimit = 1024
	pathIn.SetValue(shortenHome(cfg.TokenFile))

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = styles.Accent

	m := model{
		cfg:      cfg,
		fields:   []textinput.Model{server, tokenIn, pathIn},
		grpcWeb:  cfg.GRPCWeb,
		spin:     sp,
		hasToken: hasToken,
		masked:   masked,
		note:     note,
		noteBad:  err != nil,
		cfgPath:  cfgPath,
		envPath:  envP,
		width:    80,
	}
	m.fields[0].Focus()
	return m
}

func (m model) Init() tea.Cmd {
	return textinput.Blink
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		w := m.width - 20
		if w < 24 {
			w = 24
		}
		if w > 72 {
			w = 72
		}
		for i := range m.fields {
			m.fields[i].Width = w
		}
		return m, nil

	case tea.KeyMsg:
		switch m.state {
		case stateForm:
			return m.updateForm(msg)
		case stateWorking:
			if msg.Type == tea.KeyCtrlC || msg.Type == tea.KeyEsc {
				return m, tea.Quit
			}
			return m, nil
		case stateDone:
			switch msg.String() {
			case "q", "esc", "ctrl+c":
				return m, tea.Quit
			case "enter":
				m.state = stateForm
				m.note = ""
				m.noteBad = false
				m.fields[1].SetValue("")
				m.setFocus(0)
				return m, textinput.Blink
			}
			return m, nil
		}

	case spinner.TickMsg:
		if m.state == stateWorking {
			var cmd tea.Cmd
			m.spin, cmd = m.spin.Update(msg)
			return m, cmd
		}
		return m, nil

	case savedMsg:
		return m.applySaved(msg)
	}

	// 普通输入交给当前字段
	if m.state == stateForm {
		var cmd tea.Cmd
		m.fields[m.focus], cmd = m.fields[m.focus].Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m model) updateForm(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.Type {
	case tea.KeyCtrlC, tea.KeyEsc:
		return m, tea.Quit
	case tea.KeyTab, tea.KeyDown:
		m.moveFocus(1)
		return m, textinput.Blink
	case tea.KeyShiftTab, tea.KeyUp:
		m.moveFocus(-1)
		return m, textinput.Blink
	case tea.KeyCtrlT:
		m.grpcWeb = !m.grpcWeb
		return m, nil
	case tea.KeyCtrlS:
		return m.startSave(false)
	case tea.KeyEnter:
		if m.focus == len(m.fields)-1 {
			return m.startSave(true)
		}
		m.moveFocus(1)
		return m, textinput.Blink
	}
	var cmd tea.Cmd
	m.fields[m.focus], cmd = m.fields[m.focus].Update(k)
	return m, cmd
}

func (m *model) moveFocus(delta int) {
	m.setFocus((m.focus + delta + len(m.fields)) % len(m.fields))
}

func (m *model) setFocus(i int) {
	for j := range m.fields {
		m.fields[j].Blur()
	}
	m.focus = i
	m.fields[i].Focus()
}

func (m model) startSave(verifyIt bool) (tea.Model, tea.Cmd) {
	m.state = stateWorking
	m.verifying = verifyIt
	m.note = ""
	m.noteBad = false
	return m, tea.Batch(m.spin.Tick, m.saveCmd(verifyIt))
}

// saveCmd 先在后台验证（可选），通过了才落盘：验证失败绝不覆盖原来能用的 token。
func (m model) saveCmd(verifyIt bool) tea.Cmd {
	server := strings.TrimSpace(m.fields[0].Value())
	token := strings.TrimSpace(m.fields[1].Value())
	tokenFile := expandHome(strings.TrimSpace(m.fields[2].Value()))
	grpc := m.grpcWeb
	existing := m.cfg

	return func() tea.Msg {
		cfg := &Config{Server: server, TokenFile: tokenFile, GRPCWeb: grpc}
		normalizeConfig(cfg)
		if cfg.Server == "" {
			return savedMsg{err: fmt.Errorf("先填服务器地址")}
		}
		if token == "" {
			if existing == nil || existing.TokenFile != cfg.TokenFile {
				return savedMsg{err: fmt.Errorf("token 是空的：粘贴一个再保存")}
			}
			old, err := readToken(cfg.TokenFile)
			if os.IsNotExist(err) {
				return savedMsg{err: fmt.Errorf("token 是空的：粘贴一个再保存")}
			}
			if err != nil {
				return savedMsg{err: fmt.Errorf("没有可沿用的 token（%v），粘贴一个再保存", err)}
			}
			token = old
		}

		var id Identity
		if verifyIt {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			got, err := verify(ctx, cfg.Server, token)
			if err != nil {
				return savedMsg{err: err}
			}
			id = got
		}

		cfgPath, err := saveConfig(cfg)
		if err != nil {
			return savedMsg{err: fmt.Errorf("保存配置失败: %w", err)}
		}
		if err := saveToken(cfg.TokenFile, token); err != nil {
			return savedMsg{err: fmt.Errorf("保存 token 失败: %w", err)}
		}
		envP, err := writeEnv(cfg)
		if err != nil {
			return savedMsg{err: fmt.Errorf("写 env 失败: %w", err)}
		}
		return savedMsg{
			cfg: cfg, cfgPath: cfgPath, envPath: envP,
			id: id, verified: verifyIt, masked: maskToken(token),
		}
	}
}

func (m model) applySaved(msg savedMsg) (tea.Model, tea.Cmd) {
	m.state = stateForm
	if msg.err != nil {
		m.noteBad = true
		m.note = msg.err.Error()
		switch {
		case errors.Is(msg.err, ErrAuth):
			m.note += "。没有写入任何文件；换一个 token 再试"
			m.setFocus(1)
		case m.verifying:
			m.note += "。没有写入；修好后按 enter 重试，或 ctrl+s 跳过验证直接保存"
		}
		return m, textinput.Blink
	}

	m.cfg = msg.cfg
	m.grpcWeb = msg.cfg.GRPCWeb
	m.cfgPath = msg.cfgPath
	m.envPath = msg.envPath
	m.id = msg.id
	m.verified = msg.verified
	m.masked = msg.masked
	m.hasToken = true
	m.fields[0].SetValue(msg.cfg.Server)
	m.fields[1].SetValue("")
	m.fields[1].Placeholder = "留空 = 沿用现有 token"
	m.fields[2].SetValue(shortenHome(msg.cfg.TokenFile))
	m.state = stateDone
	return m, nil
}

func (m model) View() string {
	switch m.state {
	case stateForm:
		return "\n" + m.viewForm() + "\n"
	case stateWorking:
		return "\n" + m.viewWorking() + "\n"
	default:
		return "\n" + m.viewDone() + "\n"
	}
}

func (m model) title() string {
	return " " + styles.Title.Render("argo-token") +
		styles.Dim.Render("  v"+version+"  配置 Argo CD CLI")
}

func (m model) viewForm() string {
	var b strings.Builder
	b.WriteString(m.title() + "\n")
	b.WriteString(styles.Dim.Render("  填好服务器、粘贴 token，验证通过后写入本机；argocd 每次调用自己带 token。") + "\n\n")

	for i, f := range m.fields {
		cursor := "  "
		label := styles.Label
		if i == m.focus {
			cursor = styles.Accent.Render("❯ ")
			label = label.Foreground(lipgloss.AdaptiveColor{Light: "#0F172A", Dark: "#E2E8F0"})
		}
		b.WriteString(" " + cursor + label.Render(fieldLabels[i]) + f.View() + "\n")
		if i == 1 && m.hasToken {
			b.WriteString("    " + styles.Dim.Render("已有 token "+m.masked+"，留空就是沿用它") + "\n")
		}
	}

	toggle := styles.Dim.Render("[关]")
	if m.grpcWeb {
		toggle = styles.Good.Render("[开]")
	}
	b.WriteString("   " + styles.Label.Render("grpc-web") + toggle +
		styles.Dim.Render("  ctrl+t 切换（服务器在反向代理后面时开）") + "\n")

	b.WriteString("\n")
	if m.cfg != nil && m.cfg.Server != "" {
		b.WriteString("   " + styles.Dim.Render("当前配置: "+m.cfg.Server+"   "+shortenHome(m.cfg.TokenFile)) + "\n")
	}
	if m.note != "" {
		line := "   " + styles.Warn.Render("! "+m.note)
		if m.noteBad {
			line = "   " + styles.Bad.Render("✗ "+m.note)
		}
		b.WriteString(line + "\n")
	}

	b.WriteString("\n")
	b.WriteString(styles.Hints(
		"↑↓", "切字段", "enter", "下一个/保存验证", "ctrl+s", "跳过验证保存",
		"ctrl+t", "grpc-web", "esc", "退出",
	))
	return b.String()
}

func (m model) viewWorking() string {
	label := "正在保存…"
	if m.verifying {
		label = "正在连 " + strings.TrimSpace(m.fields[0].Value()) + " 验证 token…"
	}
	return " " + m.spin.View() + " " + styles.Text.Render(label) + "\n\n" +
		styles.Hints("esc", "退出")
}

func (m model) viewDone() string {
	var b strings.Builder
	if m.verified {
		b.WriteString(" " + styles.Good.Render("✓ 已写入并验证") + "\n\n")
	} else {
		b.WriteString(" " + styles.Warn.Render("✓ 已保存（没有联网验证）") + "\n\n")
	}

	row := func(label, value string) string {
		return "   " + styles.Label.Render(label) + styles.Text.Render(value) + "\n"
	}
	if m.cfg != nil {
		b.WriteString(row("服务器", m.cfg.Server))
	}
	if m.id.User != "" {
		who := m.id.User
		if m.id.ServerVersion != "" {
			who += "，argocd " + m.id.ServerVersion
		}
		b.WriteString(row("身份", who))
	}
	b.WriteString(row("token", m.masked+"  "+shortenHome(m.tokenFile())))
	b.WriteString(row("配置", shortenHome(m.cfgPath)))
	b.WriteString(row("env", shortenHome(m.envPath)))

	b.WriteString("\n")
	b.WriteString(styles.Dim.Render("   之后直接在 shell 里跑（不用重开）:") + "\n")
	b.WriteString(styles.Accent.Render("     argocd app list") + "\n")
	if m.cfg != nil && m.cfg.GRPCWeb {
		b.WriteString(styles.Dim.Render("   （env 里已经带了 --grpc-web，不用手加）") + "\n")
	}

	b.WriteString("\n")
	b.WriteString(styles.Hints("enter", "继续修改", "q", "退出"))
	return b.String()
}

func (m model) tokenFile() string {
	if m.cfg != nil {
		return m.cfg.TokenFile
	}
	return ""
}

func runTUI() int {
	if !isTerminal(os.Stdout) || !isTerminal(os.Stdin) {
		fmt.Fprintln(os.Stderr, "TUI 需要真终端。非交互环境请用: argo-token set -server URL（token 走 stdin）")
		return 2
	}
	p := tea.NewProgram(newModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "TUI 启动失败: %v\n", err)
		return 2
	}
	return 0
}
