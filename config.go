package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 配置文件只放「连哪台服务器、token 放在哪、要不要 grpc-web」，
// token 本身单独一个 0600 文件；两者都不进任何仓库。
type Config struct {
	Server    string `json:"server"`
	TokenFile string `json:"token_file"`
	GRPCWeb   bool   `json:"grpc_web"`
}

func homeDir() (string, error) {
	return os.UserHomeDir()
}

// argoDir 是 ~/.argocd：token 和给 shell 用的 env 文件都在这。
func argoDir() (string, error) {
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".argocd"), nil
}

func defaultTokenFile() string {
	dir, err := argoDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "token")
}

func defaultConfig() *Config {
	return &Config{TokenFile: defaultTokenFile(), GRPCWeb: true}
}

func configPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "argo-token", "config.json"), nil
}

func envPath() (string, error) {
	dir, err := argoDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "env"), nil
}

// loadConfig 读配置；文件不存在时返回默认值，ok=false。
func loadConfig() (cfg *Config, path string, ok bool, err error) {
	path, err = configPath()
	if err != nil {
		return nil, "", false, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return defaultConfig(), path, false, nil
	}
	if err != nil {
		return nil, path, false, err
	}
	cfg = &Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, path, false, fmt.Errorf("解析 %s 失败: %w", shortenHome(path), err)
	}
	normalizeConfig(cfg)
	syncEnvFile(cfg)
	return cfg, path, true, nil
}

func normalizeConfig(c *Config) {
	c.Server = normalizeServer(c.Server)
	c.TokenFile = expandHome(strings.TrimSpace(c.TokenFile))
	if c.TokenFile == "" {
		c.TokenFile = defaultTokenFile()
	}
}

// saveConfig 原子写入配置（0600）。
func saveConfig(c *Config) (string, error) {
	path, err := configPath()
	if err != nil {
		return "", err
	}
	if err := writeFileAtomic(path, mustJSON(c), 0o600); err != nil {
		return path, err
	}
	return path, nil
}

func mustJSON(v any) []byte {
	data, _ := json.MarshalIndent(v, "", "  ")
	return append(data, '\n')
}

// saveToken 原子写入 token 文件（0600）。粘贴常带上的 Bearer 前缀、引号、
// 折行在这里一并清掉，存进去的就是光秃秃的 token。
func saveToken(path, token string) error {
	token = normalizeToken(token)
	if token == "" {
		return fmt.Errorf("token 是空的")
	}
	return writeFileAtomic(path, []byte(token+"\n"), 0o600)
}

func readToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := normalizeToken(string(data))
	if token == "" {
		return "", fmt.Errorf("%s 是空的", shortenHome(path))
	}
	return token, nil
}

// writeEnv 生成 shell 用的 env 文件：只有服务器地址和 token 路径，没有 token 本身。
// zsh 里的 argocd() 包装函数每次调用前 source 它，于是改了配置立即生效。
func writeEnv(c *Config) (string, error) {
	path, err := envPath()
	if err != nil {
		return "", err
	}
	head := "# 由 argo-token 生成（" + time.Now().Format("2006-01-02 15:04") + "），下次保存会覆盖，别手改\n"
	if err := writeFileAtomic(path, []byte(head+envBody(c)), 0o600); err != nil {
		return path, err
	}
	return path, nil
}

// envBody 是真正给 shell source 的那几行。单独拎出来，好拿它和已有的 env
// 文件比内容——上面那行时间戳每次写都不一样，不能拿来判断有没有变化。
func envBody(c *Config) string {
	var b strings.Builder
	// argocd CLI 的 ARGOCD_SERVER 只要主机名/端口，带 https:// 会拼成 https://https//…
	b.WriteString("export ARGOCD_SERVER=" + shellQuote(serverHost(c.Server)) + "\n")
	var opts []string
	if c.GRPCWeb {
		opts = append(opts, "--grpc-web")
	}
	if strings.HasPrefix(strings.ToLower(c.Server), "http://") {
		opts = append(opts, "--plaintext")
	}
	b.WriteString("export ARGOCD_OPTS=" + shellQuote(strings.Join(opts, " ")) + "\n")
	b.WriteString("export ARGOCD_TOKEN_FILE=" + shellQuote(c.TokenFile) + "\n")
	return b.String()
}

// syncEnvFile 让 ~/.argocd/env 跟着配置走：配置文件可能刚从别的机器同步过来
// （里面还是那边的反斜杠路径），env 也可能根本没生成过。内容对得上就不动，
// 对不上就重写；写失败只当没发生，不影响命令本身。
func syncEnvFile(c *Config) {
	if c == nil || c.Server == "" || c.TokenFile == "" {
		return
	}
	path, err := envPath()
	if err != nil {
		return
	}
	if old, err := os.ReadFile(path); err == nil && envRest(string(old)) == envBody(c) {
		return
	}
	_, _ = writeEnv(c)
}

// envRest 去掉 env 文件里的注释行，剩下的就是要比对的内容。
func envRest(s string) string {
	var b strings.Builder
	for _, line := range strings.SplitAfter(s, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		b.WriteString(line)
	}
	return b.String()
}

// serverHost 把配置里的 URL 变成 argocd CLI 认的主机名（可能带端口）。
func serverHost(server string) string {
	s := strings.TrimSpace(server)
	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil && u.Host != "" {
			return u.Host
		}
	}
	return strings.TrimRight(s, "/")
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// writeFileAtomic 先写同目录临时文件再 rename；目录按需要创建（0700）。
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建目录 %s 失败: %w", shortenHome(dir), err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".argo-token-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

// expandHome 把 ~、~/x、~\x 展开成绝对路径。
// 反斜杠也认：配置会跟着 hmck 多端同步在 Windows 和 macOS/Linux 之间来回，
// 在 Windows 上写下的 ~\.argocd\token 到这里必须还能落到同一个文件。
func expandHome(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	home, err := homeDir()
	if err != nil || home == "" {
		return p
	}
	switch {
	case p == "~":
		return home
	case strings.HasPrefix(p, "~/"), strings.HasPrefix(p, `~\`):
		rest := strings.TrimLeft(p[2:], `/\`)
		return filepath.Join(home, filepath.FromSlash(strings.ReplaceAll(rest, `\`, "/")))
	}
	return p
}

func shortenHome(p string) string {
	home, err := homeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(os.PathSeparator)) {
		return "~" + p[len(home):]
	}
	return p
}

// normalizeServer 去掉空白和末尾斜杠，没写协议就补 https://。
func normalizeServer(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	return strings.TrimRight(s, "/")
}

// maskToken 只留头尾，输出里永远不会出现完整 token。
func maskToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	r := []rune(token)
	if len(r) <= 12 {
		return strings.Repeat("•", len(r))
	}
	return string(r[:6]) + "…" + string(r[len(r)-4:])
}
