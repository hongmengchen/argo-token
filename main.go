package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

var version = "0.1.1"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		return runTUI()
	}
	switch args[0] {
	case "status", "config":
		return runStatus(args[1:])
	case "check":
		return runCheck(args[1:])
	case "set":
		return runSet(args[1:])
	case "clear", "logout":
		return runClear(args[1:])
	case "env":
		return runEnv(args[1:])
	case "skill":
		return runSkill(args[1:])
	case "version", "-v", "--version":
		fmt.Printf("argo-token %s\n", version)
		return 0
	case "help", "-h", "--help":
		usage(os.Stdout)
		return 0
	}
	fmt.Fprintf(os.Stderr, "未知命令 %q\n\n", args[0])
	usage(os.Stderr)
	return 2
}

func usage(w io.Writer) {
	fmt.Fprintf(w, `argo-token v%s — 给本机 argocd CLI 配置服务器和只读 token

用法:
  argo-token                  打开 TUI：填服务器、粘贴 token，验证通过后写入
  argo-token status           看当前配置（不回显 token）
  argo-token check            用已保存的 token 连服务器验证
  argo-token set [选项]       非交互写入（token 从 stdin 读）
  argo-token clear            删掉 token 文件（配置和 env 保留）
  argo-token env              打印 shell 集成片段（不含 token）
  argo-token skill [选项]     写 agent skill
  argo-token version

写入位置:
  配置   <os.UserConfigDir()>/argo-token/config.json   0600，只有地址/路径/开关
  token  ~/.argocd/token                               0600，只有 token
  env    ~/.argocd/env                                 0600，给 shell 的地址和路径

set 选项:
  -server <url>       Argo CD 地址，例如 https://argocd.example.com
  -token-file <path>  token 存哪（默认 ~/.argocd/token）
  -token <value>      直接给值（不推荐，会留在 shell 历史里；默认从 stdin 读）
  -no-verify          不联网验证就写入（离线时用）
  -no-grpc-web        不设 --grpc-web（默认设；服务器在反向代理后面时才需要关）
  -json               输出 JSON

退出码:
  0  写入成功 / 验证通过 / 查询成功
  1  服务器拒绝了 token（401/403）：没有写入任何文件
  2  用法错误、网络不通、配置缺失
`, version)
}

// ---- status ----

func runStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "参数不对：argo-token status [--json]")
		return 2
	}

	cfg, cfgPath, ok, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		return 2
	}
	envP, _ := envPath()

	token, tokErr := readToken(cfg.TokenFile)
	info := fileInfo(cfg.TokenFile)
	out := map[string]any{
		"config":        cfgPath,
		"config_exists": ok,
		"server":        cfg.Server,
		"token_file":    cfg.TokenFile,
		"token_present": tokErr == nil,
		"grpc_web":      cfg.GRPCWeb,
		"env_file":      envP,
	}
	if tokErr == nil {
		out["token_masked"] = maskToken(token)
	}
	if info != "" {
		out["token_file_info"] = info
	}

	if *asJSON {
		data, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(data))
		return 0
	}

	fmt.Printf("配置     %s\n", shortenHome(cfgPath))
	server := cfg.Server
	if server == "" {
		server = "(未设置，运行 argo-token 填写)"
	}
	fmt.Printf("服务器   %s\n", server)
	tokLine := "未写入（运行 argo-token 粘贴 token）"
	if tokErr == nil {
		tokLine = maskToken(token) + "  " + info
	}
	fmt.Printf("token    %s  %s\n", shortenHome(cfg.TokenFile), tokLine)
	fmt.Printf("grpc-web %s\n", onOffText(cfg.GRPCWeb))
	fmt.Printf("env      %s\n", shortenHome(envP))
	fmt.Println()
	fmt.Println("提示：argo-token check 验证连通；argo-token 打开 TUI 修改")
	return 0
}

func fileInfo(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("(%s，%s)", st.Mode().Perm(), humanSize(st.Size()))
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func onOffText(b bool) string {
	if b {
		return "开（--grpc-web）"
	}
	return "关"
}

// ---- check ----

func runCheck(args []string) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "参数不对：argo-token check [--json]")
		return 2
	}

	cfg, _, _, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		return 2
	}
	if cfg.Server == "" {
		fmt.Fprintln(os.Stderr, "错误: 还没配置服务器，先运行 argo-token")
		return 2
	}
	token, err := readToken(cfg.TokenFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: 没有可用的 token（%v），先运行 argo-token\n", err)
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	id, err := verify(ctx, cfg.Server, token)
	if err != nil {
		code := 2
		if errors.Is(err, ErrAuth) {
			code = 1
		}
		if *asJSON {
			data, _ := json.MarshalIndent(map[string]any{"ok": false, "error": err.Error()}, "", "  ")
			fmt.Println(string(data))
		} else {
			fmt.Fprintf(os.Stderr, "✗ %v\n", err)
			if code == 1 {
				fmt.Fprintln(os.Stderr, "  token 可能抄错、过期或被撤销；运行 argo-token 换一个")
			}
		}
		return code
	}

	if *asJSON {
		data, _ := json.MarshalIndent(map[string]any{
			"ok": true, "server": cfg.Server, "user": id.User,
			"issuer": id.Issuer, "server_version": id.ServerVersion,
		}, "", "  ")
		fmt.Println(string(data))
		return 0
	}
	fmt.Printf("✓ %s 已登录\n", id.User)
	if id.Issuer != "" {
		fmt.Printf("  签发方   %s\n", id.Issuer)
	}
	if id.ServerVersion != "" {
		fmt.Printf("  服务器   argocd %s\n", id.ServerVersion)
	}
	if cfg.GRPCWeb {
		fmt.Println("  grpc-web 开")
	}
	return 0
}

// ---- set ----

func runSet(args []string) int {
	fs := flag.NewFlagSet("set", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", "", "Argo CD 地址")
	tokenFile := fs.String("token-file", "", "token 存哪")
	tokenFlag := fs.String("token", "", "直接给 token（不推荐）")
	noVerify := fs.Bool("no-verify", false, "不联网验证")
	noGRPC := fs.Bool("no-grpc-web", false, "不设 --grpc-web")
	asJSON := fs.Bool("json", false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "参数不对：argo-token set -server URL [选项]，token 从 stdin 读")
		return 2
	}

	cfg, _, _, err := loadConfig()
	if err != nil {
		cfg = defaultConfig()
	}
	if s := strings.TrimSpace(*server); s != "" {
		cfg.Server = s
	}
	if s := strings.TrimSpace(*tokenFile); s != "" {
		cfg.TokenFile = expandHome(s)
	}
	if *noGRPC {
		cfg.GRPCWeb = false
	}
	normalizeConfig(cfg)
	if cfg.Server == "" {
		fmt.Fprintln(os.Stderr, "错误: 缺 -server（第一次写入必须给出服务器地址）")
		return 2
	}

	token := strings.TrimSpace(*tokenFlag)
	if token == "" {
		if isTerminal(os.Stdin) {
			fmt.Fprintln(os.Stderr, "错误: token 从 stdin 读；终端里请直接运行 argo-token 打开 TUI")
			return 2
		}
		data, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
		if err != nil {
			fmt.Fprintf(os.Stderr, "错误: 读 stdin 失败: %v\n", err)
			return 2
		}
		token = strings.TrimSpace(string(data))
	}
	if token == "" {
		fmt.Fprintln(os.Stderr, "错误: token 是空的")
		return 2
	}

	var id *Identity
	if !*noVerify {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		got, err := verify(ctx, cfg.Server, token)
		if err != nil {
			code := 2
			if errors.Is(err, ErrAuth) {
				code = 1
			}
			if *asJSON {
				data, _ := json.MarshalIndent(map[string]any{"ok": false, "error": err.Error()}, "", "  ")
				fmt.Println(string(data))
			} else {
				fmt.Fprintf(os.Stderr, "✗ %v\n没有写入任何文件。\n", err)
			}
			return code
		}
		id = &got
	}

	cfgPath, err := saveConfig(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: 保存配置失败: %v\n", err)
		return 2
	}
	if err := saveToken(cfg.TokenFile, token); err != nil {
		fmt.Fprintf(os.Stderr, "错误: 保存 token 失败: %v\n", err)
		return 2
	}
	envP, err := writeEnv(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: 写 env 失败: %v\n", err)
		return 2
	}

	if *asJSON {
		out := map[string]any{
			"ok": true, "server": cfg.Server, "token_file": cfg.TokenFile,
			"config": cfgPath, "env_file": envP, "token_masked": maskToken(token),
			"verified": id != nil,
		}
		if id != nil {
			out["user"] = id.User
			out["server_version"] = id.ServerVersion
		}
		data, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(data))
		return 0
	}
	fmt.Printf("已写入 %s  %s\n", shortenHome(cfg.TokenFile), maskToken(token))
	fmt.Printf("配置   %s\n", shortenHome(cfgPath))
	fmt.Printf("env    %s（重开 shell 或 source 后 argocd 生效）\n", shortenHome(envP))
	if id != nil {
		fmt.Printf("验证   ✓ %s 已登录", id.User)
		if id.ServerVersion != "" {
			fmt.Printf("，argocd %s", id.ServerVersion)
		}
		fmt.Println()
	} else {
		fmt.Println("验证   已跳过（--no-verify）")
	}
	return 0
}

// ---- clear ----

func runClear(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "参数不对：argo-token clear")
		return 2
	}
	cfg, _, _, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		return 2
	}
	if err := os.Remove(cfg.TokenFile); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "删除失败: %v\n", err)
		return 2
	}
	fmt.Printf("已删除 %s（服务器配置保留）\n", shortenHome(cfg.TokenFile))
	return 0
}

// ---- env ----

// runEnv 打印 shell 集成片段。片段只在调用 argocd 的进程里注入 token，
// 不把 token 放进全局环境；地址/opts/token 路径从 ~/.argocd/env 读。
func runEnv(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "参数不对：argo-token env")
		return 2
	}
	envP, err := envPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		return 2
	}
	fmt.Printf(`# 把下面这段放进 ~/.zshrc（每次保存配置都会重写 %s）
argocd() {
  [ -r "%s" ] && source "%s"
  local tok="${ARGOCD_TOKEN_FILE:-$HOME/.argocd/token}"
  if [ -r "$tok" ]; then
    ARGOCD_AUTH_TOKEN="$(<"$tok")" command argocd "$@"
  else
    [[ "$1 $2" != "version --client" ]] && \
      print -u2 "⚠ 还没配置 token：运行 argo-token 填写后再试"
    command argocd "$@"
  fi
}
`, shortenHome(envP), envP, envP)
	return 0
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

func displayPath(p string) string {
	return shortenHome(p)
}
