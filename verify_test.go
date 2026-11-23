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
		w.Write([]byte(`{"loggedIn":false,"user":""}`))
	}))
	t.Cleanup(srv.Close)
	_, err := verify(context.Background(), srv.URL, "x")
	if !errors.Is(err, ErrAuth) {
		t.Errorf("未登录应算 ErrAuth: %v", err)
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
