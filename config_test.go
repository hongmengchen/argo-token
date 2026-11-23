package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeServer(t *testing.T) {
	cases := map[string]string{
		"argocd.example.com":            "https://argocd.example.com",
		"  https://argocd.example.com/": "https://argocd.example.com",
		"http://argocd.example.com":     "http://argocd.example.com",
		"":                              "",
	}
	for in, want := range cases {
		if got := normalizeServer(in); got != want {
			t.Errorf("normalizeServer(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMaskToken(t *testing.T) {
	if got := maskToken(""); got != "" {
		t.Errorf("空 token 应返回空，得到 %q", got)
	}
	if got := maskToken("short"); strings.Contains(got, "short") {
		t.Errorf("短 token 不应回显: %q", got)
	}
	long := "eyJhbGciOiJIUzI1NiJ9.payload.signature9x2"
	got := maskToken(long)
	if !strings.HasPrefix(got, "eyJhbG") || !strings.HasSuffix(got, "9x2") {
		t.Errorf("掩码应保留头尾: %q", got)
	}
	if strings.Contains(got, "payload") {
		t.Errorf("掩码不应包含中间内容: %q", got)
	}
}

func TestExpandHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := expandHome("~/.argocd/token"); got != filepath.Join(home, ".argocd/token") {
		t.Errorf("expandHome 展开 ~ 失败: %q", got)
	}
	if got := expandHome("/abs/path"); got != "/abs/path" {
		t.Errorf("绝对路径不应改动: %q", got)
	}
}

func TestSaveLoadConfigAndToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := &Config{
		Server:    "argocd.example.com/",
		TokenFile: "~/.argocd/token",
		GRPCWeb:   true,
	}
	normalizeConfig(cfg)
	if cfg.Server != "https://argocd.example.com" {
		t.Fatalf("server 未归一化: %q", cfg.Server)
	}

	cfgPath, err := saveConfig(cfg)
	if err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
	st, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("config 权限应为 0600，得到 %v", st.Mode().Perm())
	}

	loaded, _, ok, err := loadConfig()
	if err != nil || !ok {
		t.Fatalf("loadConfig: ok=%v err=%v", ok, err)
	}
	if loaded.Server != cfg.Server || loaded.TokenFile != cfg.TokenFile || loaded.GRPCWeb != cfg.GRPCWeb {
		t.Errorf("读回不一致: %+v", loaded)
	}

	if err := saveToken(loaded.TokenFile, "  token-value\n"); err != nil {
		t.Fatalf("saveToken: %v", err)
	}
	tok, err := readToken(loaded.TokenFile)
	if err != nil || tok != "token-value" {
		t.Fatalf("readToken = %q, %v", tok, err)
	}
	st, _ = os.Stat(loaded.TokenFile)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("token 权限应为 0600，得到 %v", st.Mode().Perm())
	}
	if st, _ := os.Stat(filepath.Dir(loaded.TokenFile)); st.Mode().Perm() != 0o700 {
		t.Errorf(".argocd 权限应为 0700，得到 %v", st.Mode().Perm())
	}
}

func TestLoadConfigMissingUsesDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg, path, ok, err := loadConfig()
	if err != nil {
		t.Fatalf("缺文件不应报错: %v", err)
	}
	if ok {
		t.Fatal("缺文件时 ok 应为 false")
	}
	if cfg.Server != "" || !cfg.GRPCWeb {
		t.Errorf("默认值不对: %+v", cfg)
	}
	if !strings.HasSuffix(path, filepath.Join("argo-token", "config.json")) {
		t.Errorf("config 路径不对: %s", path)
	}
	if !strings.HasSuffix(cfg.TokenFile, filepath.Join(".argocd", "token")) {
		t.Errorf("默认 token 路径不对: %s", cfg.TokenFile)
	}
}

func TestWriteEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := &Config{
		Server:    "https://argocd.example.com",
		TokenFile: filepath.Join(home, ".argocd", "token"),
		GRPCWeb:   true,
	}
	path, err := writeEnv(cfg)
	if err != nil {
		t.Fatalf("writeEnv: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{
		"export ARGOCD_SERVER='argocd.example.com'",
		`export ARGOCD_OPTS='--grpc-web'`,
		"export ARGOCD_TOKEN_FILE='" + cfg.TokenFile + "'",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("env 缺少 %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "ARGOCD_AUTH_TOKEN") {
		t.Error("env 文件里不应出现 token 本身")
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("env 权限应为 0600，得到 %v", st.Mode().Perm())
	}

	cfg.GRPCWeb = false
	if _, err := writeEnv(cfg); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if !strings.Contains(string(data), `export ARGOCD_OPTS=''`) {
		t.Errorf("关闭 grpc-web 后应输出空 opts:\n%s", data)
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("plain"); got != "'plain'" {
		t.Errorf("shellQuote = %q", got)
	}
	if got := shellQuote("it's"); got != `'it'\''s'` {
		t.Errorf("单引号转义不对: %q", got)
	}
}

func TestSaveTokenRejectsEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".argocd", "token")
	if err := saveToken(path, "  \n"); err == nil {
		t.Fatal("空 token 应报错")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("被拒绝时不应创建文件")
	}
}
