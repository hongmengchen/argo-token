package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeArgo(t *testing.T, token string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/session/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"Unauthenticated","code":16}`))
			return
		}
		w.Write([]byte(`{"loggedIn":true,"user":"alice","iss":"argocd"}`))
	})
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"Version":"v3.4.10"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestVerifyOK(t *testing.T) {
	srv := fakeArgo(t, "good-token")
	id, err := verify(context.Background(), srv.URL, "good-token")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if id.User != "alice" || id.Issuer != "argocd" || id.ServerVersion != "v3.4.10" {
		t.Errorf("identity 不对: %+v", id)
	}
}

func TestVerifyRejectsToken(t *testing.T) {
	srv := fakeArgo(t, "good-token")
	_, err := verify(context.Background(), srv.URL, "bad-token")
	if err == nil {
		t.Fatal("坏 token 应该报错")
	}
	if !errors.Is(err, ErrAuth) {
		t.Errorf("应可识别为 ErrAuth: %v", err)
	}
}

func TestVerifyNotLoggedIn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/session/userinfo":
			// Argo CD 对不认的 token 也回 200 + loggedIn:false
			w.Write([]byte(`{"loggedIn":false,"user":""}`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"invalid session: signature is invalid","code":16}`))
		}
	}))
	t.Cleanup(srv.Close)
	_, err := verify(context.Background(), srv.URL, "x")
	if !errors.Is(err, ErrAuth) {
		t.Errorf("未登录应算 ErrAuth: %v", err)
	}
	// 拒绝原因要能从服务器带回来，别只说「未登录」
	if !strings.Contains(err.Error(), "signature is invalid") {
		t.Errorf("错误里应带服务器的说法: %v", err)
	}
}

// 有的 token 能调接口但 userinfo 不给身份：applications 通了就算验证通过
func TestVerifyFallsBackWhenUserinfoHasNoUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/session/userinfo":
			w.Write([]byte(`{"loggedIn":false,"user":"","iss":"argocd"}`))
		case "/api/v1/applications":
			w.Write([]byte(`{"items":[]}`))
		default:
			w.Write([]byte(`{"Version":"v3.4.10"}`))
		}
	}))
	t.Cleanup(srv.Close)
	id, err := verify(context.Background(), srv.URL, "proj-token")
	if err != nil {
		t.Fatalf("token 能用就不该拒绝: %v", err)
	}
	if id.User != "" || id.Issuer != "argocd" || id.ServerVersion != "v3.4.10" {
		t.Errorf("identity 不对: %+v", id)
	}
}

func TestVerifyReportsPermissionDenied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/session/userinfo" {
			w.Write([]byte(`{"loggedIn":false,"user":""}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"permission denied","code":7}`))
	}))
	t.Cleanup(srv.Close)
	_, err := verify(context.Background(), srv.URL, "x")
	if !errors.Is(err, ErrAuth) || !strings.Contains(err.Error(), "没有列应用的权限") {
		t.Errorf("403 应说明是权限问题: %v", err)
	}
}

// 粘贴带的 Bearer 前缀、引号、折行都要清掉——Argo CD 只认正好三段的 JWT
func TestNormalizeToken(t *testing.T) {
	cases := map[string]string{
		"eyJ.a.b":                   "eyJ.a.b",
		"  eyJ.a.b\n":               "eyJ.a.b",
		"Bearer eyJ.a.b":            "eyJ.a.b",
		"bearer=eyJ.a.b":            "eyJ.a.b",
		`"eyJ.a.b"`:                 "eyJ.a.b",
		"'eyJ.a.b'":                 "eyJ.a.b",
		"eyJ.a.b\n":                 "eyJ.a.b",
		"ARGOCD_AUTH_TOKEN=eyJ.a.b": "eyJ.a.b",
		"eyJ.aa\nbb.cc":             "eyJ.aabb.cc",
		"Bearer \"eyJ.a.b\"":        "eyJ.a.b",
	}
	for in, want := range cases {
		if got := normalizeToken(in); got != want {
			t.Errorf("normalizeToken(%q) = %q，想要 %q", in, got, want)
		}
	}
}

func TestVerifyTrailingSlash(t *testing.T) {
	srv := fakeArgo(t, "tok")
	if _, err := verify(context.Background(), srv.URL+"/", "tok"); err != nil {
		t.Fatalf("末尾斜杠不应影响: %v", err)
	}
}

func TestVerifyBadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>not argo</html>"))
	}))
	t.Cleanup(srv.Close)
	_, err := verify(context.Background(), srv.URL, "tok")
	if err == nil || errors.Is(err, ErrAuth) {
		t.Errorf("非 JSON 应是普通错误: %v", err)
	}
	if !strings.Contains(err.Error(), "不是 JSON") {
		t.Errorf("提示应说明不是 JSON: %v", err)
	}
}

func TestVerifyEmptyInputs(t *testing.T) {
	if _, err := verify(context.Background(), "", "tok"); err == nil {
		t.Error("空服务器应报错")
	}
	if _, err := verify(context.Background(), "https://argocd.example.com", "  "); err == nil {
		t.Error("空 token 应报错")
	}
}
