package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func formModel(server, token string) model {
	m := newModel()
	m.fields[0].SetValue(server)
	m.fields[1].SetValue(token)
	return m
}

func TestSaveCmdWritesAfterVerify(t *testing.T) {
	home := setTestHome(t)
	srv := fakeArgo(t, "good-token")

	m := formModel(srv.URL, "good-token")
	msg := m.saveCmd(true)()
	got, ok := msg.(savedMsg)
	if !ok {
		t.Fatalf("应返回 savedMsg，得到 %T", msg)
	}
	if got.err != nil {
		t.Fatalf("保存失败: %v", got.err)
	}
	if !got.verified || got.id.User != "alice" || got.masked == "" {
		t.Errorf("结果不对: %+v", got)
	}

	tokenPath := filepath.Join(home, ".argocd", "token")
	tok, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("token 没写进去: %v", err)
	}
	if strings.TrimSpace(string(tok)) != "good-token" {
		t.Errorf("token 内容不对: %q", tok)
	}
	if st, _ := os.Stat(tokenPath); st.Mode().Perm() != 0o600 {
		t.Errorf("token 权限应为 0600: %v", st.Mode().Perm())
	}

	envData, err := os.ReadFile(filepath.Join(home, ".argocd", "env"))
	if err != nil {
		t.Fatalf("env 没写进去: %v", err)
	}
	if !strings.Contains(string(envData), "export ARGOCD_SERVER='"+serverHost(srv.URL)+"'") {
		t.Errorf("env 里应带主机名（不带协议）:\n%s", envData)
	}
	if !strings.Contains(string(envData), "--plaintext") {
		t.Errorf("http 服务器应自动加 --plaintext:\n%s", envData)
	}

	cfg, _, okCfg, err := loadConfig()
	if err != nil || !okCfg {
		t.Fatalf("配置没写入: ok=%v err=%v", okCfg, err)
	}
	if cfg.Server != srv.URL || !cfg.GRPCWeb {
		t.Errorf("配置内容不对: %+v", cfg)
	}
}

func TestSaveCmdRejectedWritesNothing(t *testing.T) {
	home := setTestHome(t)
	srv := fakeArgo(t, "good-token")

	m := formModel(srv.URL, "bad-token")
	msg := m.saveCmd(true)()
	got, ok := msg.(savedMsg)
	if !ok || got.err == nil {
		t.Fatalf("坏 token 应失败，得到 %+v", msg)
	}
	for _, p := range []string{
		filepath.Join(home, ".argocd", "token"),
		filepath.Join(home, ".argocd", "env"),
	} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("被拒绝时不应写 %s", p)
		}
	}
}

func TestSaveCmdForceWithoutVerify(t *testing.T) {
	home := setTestHome(t)

	m := formModel("https://argocd.example.com", "offline-token")
	msg := m.saveCmd(false)()
	got, ok := msg.(savedMsg)
	if !ok || got.err != nil {
		t.Fatalf("跳过验证保存失败: %+v", msg)
	}
	if got.verified {
		t.Error("没验证时 verified 应为 false")
	}
	if _, err := os.Stat(filepath.Join(home, ".argocd", "token")); err != nil {
		t.Errorf("token 应已写入: %v", err)
	}
}

func TestSaveCmdEmptyTokenNoExisting(t *testing.T) {
	setTestHome(t)

	m := formModel("https://argocd.example.com", "")
	msg := m.saveCmd(true)()
	got, ok := msg.(savedMsg)
	if !ok || got.err == nil || !strings.Contains(got.err.Error(), "token 是空的") {
		t.Fatalf("空 token 应拒绝，得到 %+v", msg)
	}
}

func TestSaveCmdReusesExistingToken(t *testing.T) {
	home := setTestHome(t)
	tokenPath := filepath.Join(home, ".argocd", "token")
	if err := saveToken(tokenPath, "kept-token"); err != nil {
		t.Fatal(err)
	}
	srv := fakeArgo(t, "kept-token")

	m := formModel(srv.URL, "")
	msg := m.saveCmd(true)()
	got, ok := msg.(savedMsg)
	if !ok || got.err != nil {
		t.Fatalf("应沿用现有 token: %+v", msg)
	}
	tok, err := readToken(tokenPath)
	if err != nil || tok != "kept-token" {
		t.Errorf("沿用的 token 被改了: %q %v", tok, err)
	}
}
