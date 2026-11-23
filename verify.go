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
	token = normalizeToken(token)
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
		return Identity{}, fmt.Errorf("%w（HTTP %d：%s）", ErrAuth, status, snippet(body))
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
		// userinfo 对不认的 token 也回 200 + loggedIn:false，光看它分不清
		// 是 token 不对还是服务器没把身份带出来。拿 applications 再问一次：
		// 401/403 的响应体里有服务器自己的说法（签名不对、已撤销、没权限…），
		// 而项目角色 token 能调接口但 userinfo 不给身份，能用就算通过。
		if err := probeApplications(ctx, client, base, token); err != nil {
			return Identity{}, err
		}
	}

	id := Identity{User: info.User, Issuer: info.Iss, ServerVersion: serverVersion(ctx, client, base, token)}
	return id, nil
}

// probeApplications 用 token 请求一个确实要登录的接口，确认它能不能用，
// 顺便把服务器拒绝的原因带回来。只看 userinfo 的话，坏 token 也是 200。
func probeApplications(ctx context.Context, client *http.Client, base, token string) error {
	body, status, err := getJSON(ctx, client, base+"/api/v1/applications?fields=metadata.name&limit=1", token)
	if err != nil {
		return err
	}
	switch status {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized:
		return fmt.Errorf("%w（HTTP 401：%s）", ErrAuth, snippet(body))
	case http.StatusForbidden:
		return fmt.Errorf("%w：能登录，但没有列应用的权限（HTTP 403：%s）", ErrAuth, snippet(body))
	default:
		return fmt.Errorf("applications 返回 HTTP %d: %s", status, snippet(body))
	}
}

// serverVersion 查服务器版本；查不到不影响 token 有效性，错误一律忽略。
func serverVersion(ctx context.Context, client *http.Client, base, token string) string {
	body, status, err := getJSON(ctx, client, base+"/api/version", token)
	if err != nil || status != http.StatusOK {
		return ""
	}
	var v map[string]any
	if json.Unmarshal(body, &v) != nil {
		return ""
	}
	for _, k := range []string{"Version", "version"} {
		if s, ok := v[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// normalizeToken 清掉粘贴带进来的杂质：Bearer 前缀、外面的引号、被行宽
// 折出来的换行和空格。JWT 只有 base64url 和点，不可能含空白；不清掉的话
// Argo CD 的 getToken 认为它根本不是 token（要求正好三段），于是当成没登录。
func normalizeToken(raw string) string {
	s := strings.TrimSpace(raw)
	for {
		lower := strings.ToLower(s)
		switch {
		case strings.HasPrefix(lower, "bearer"):
			s = strings.TrimSpace(strings.TrimPrefix(s[len("bearer"):], "="))
		case strings.HasPrefix(lower, "argocd_auth_token="):
			s = strings.TrimSpace(s[len("argocd_auth_token="):])
		case len(s) >= 2 && strings.HasPrefix(s, "\"") && strings.HasSuffix(s, "\""):
			s = strings.TrimSpace(strings.Trim(s, "\""))
		case len(s) >= 2 && strings.HasPrefix(s, "'") && strings.HasSuffix(s, "'"):
			s = strings.TrimSpace(strings.Trim(s, "'"))
		default:
			// JWT 里没有空白；折行、粘贴带进来的空格都去掉
			return strings.Join(strings.Fields(s), "")
		}
	}
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
