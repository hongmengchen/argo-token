package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrAuth 表示服务器明确拒绝了 token（401/403），跟网络不通区分开：
// 被拒绝不该覆盖原来能用的 token，网络问题则可以重试。
var ErrAuth = errors.New("token 被服务器拒绝")

// Identity 是 /api/v1/session/userinfo + /api/version 的结果。
type Identity struct {
	User          string
	Issuer        string
	ServerVersion string
}

// verify 用 Bearer token 请求 Argo CD 的 REST 网关（不依赖本机 argocd，
// 也不受 grpc-web 影响——REST 走普通 HTTPS，Traefik 后面照样通）。
func verify(ctx context.Context, server, token string) (Identity, error) {
	base := normalizeServer(server)
	if base == "" {
		return Identity{}, fmt.Errorf("服务器地址是空的")
	}
	if u, err := url.Parse(base); err != nil || u.Host == "" {
		return Identity{}, fmt.Errorf("服务器地址不合法: %s", server)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return Identity{}, fmt.Errorf("token 是空的")
	}

	client := &http.Client{Timeout: 12 * time.Second}

	body, status, err := getJSON(ctx, client, base+"/api/v1/session/userinfo", token)
	if err != nil {
		return Identity{}, err
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return Identity{}, fmt.Errorf("%w（HTTP %d）", ErrAuth, status)
	case status != http.StatusOK:
		return Identity{}, fmt.Errorf("userinfo 返回 HTTP %d: %s", status, snippet(body))
	}
	var info struct {
		LoggedIn bool   `json:"loggedIn"`
		User     string `json:"user"`
		Iss      string `json:"iss"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return Identity{}, fmt.Errorf("userinfo 返回的不是 JSON（服务器地址对吗？）: %s", snippet(body))
	}
	if !info.LoggedIn || info.User == "" {
		return Identity{}, fmt.Errorf("%w：userinfo 说未登录", ErrAuth)
	}

	id := Identity{User: info.User, Issuer: info.Iss}
	// 版本查不到不影响 token 有效性，忽略错误。
	if body, status, err := getJSON(ctx, client, base+"/api/version", token); err == nil && status == http.StatusOK {
		var v map[string]any
		if json.Unmarshal(body, &v) == nil {
			for _, k := range []string{"Version", "version"} {
				if s, ok := v[k].(string); ok && s != "" {
					id.ServerVersion = s
					break
				}
			}
		}
	}
	return id, nil
}

func getJSON(ctx context.Context, client *http.Client, u, token string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("连不上 %s: %w", u, unwrapNetErr(err))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// unwrapNetErr 把 *url.Error 剥掉一层，提示里少点 `Get "https://…"` 的噪音。
func unwrapNetErr(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return uerr.Err
	}
	return err
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	if s == "" {
		return "(空响应)"
	}
	return s
}
