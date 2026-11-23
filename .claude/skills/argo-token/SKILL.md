---
name: argo-token
description: 给本机 argocd CLI 配置 Argo CD 服务器和只读 token（TUI 填写、写入 0600 文件、shell 包装函数自动注入）。当用户说 argocd 连不上/未登录/Unauthenticated、要换 Argo CD token、要把 argocd 地址写进本机、要看当前 Argo 配置或验证 token 是否还有效时使用。包含 argo-token 的 CLI 用法、退出码、文件位置与安全约束。
---

# argo-token

`argo-token` 是这台机器上的小工具：把 Argo CD 服务器地址和 token 存到本机 0600 文件里，
并生成一个给 shell 用的 env 文件。`~/.zshrc` 里的 `argocd()` 包装函数在每次调用时把 token
注入到那一个进程（`ARGOCD_AUTH_TOKEN`），不放进全局环境。

**给用户配置时优先让他自己运行 `argo-token`**（TUI 需要真终端）；agent 在非交互环境里用 `set`。

## 铁律

1. **不要回显 token。** 不要把完整 token 写进回复、日志、提交或临时文件；工具自己的输出已经是掩码
   （如 `eyJhbG…k9x2`）。用户贴给 agent 的 token 只用于一次性写入，不要复述。
2. **不要提交配置和 token。** `~/.argocd/` 与 `config.json` 都在本机，仓库里出现真实域名/用户/凭据一律不行；
   示例统一用 `argocd.example.com`、`alice`。
3. **只读账号别试着写。** 只读角色对 `app sync` / `delete` / `patch` 会返回 `PermissionDenied`，
   这不是配置问题，原样告诉用户即可。
4. **验证失败不落盘。** token 被 401/403 拒绝时，工具不会覆盖原来能用的 token；网络不通时也不写，
   除非用户明确要求跳过验证（TUI 里 `ctrl+s`，CLI 里 `--no-verify`）。报错里带服务器的原话
   （`invalid session: signature is invalid`、已撤销、没权限…），按它判断是换 token 还是补 RBAC。
5. **粘贴的 token 会先清洗。** `Bearer ` 前缀、`ARGOCD_AUTH_TOKEN=`、外层引号、折行空格都会去掉；
   Argo CD 只认正好三段的 JWT，带杂质会报「userinfo 说未登录」。

## 1. 给用户配置（首选）

```bash
argo-token          # TUI：服务器、token、token 文件路径，enter 保存并验证
```

写入后提示用户重开 shell（或 `source ~/.zshrc`），之后 `argocd app list` 即可用。
服务器在反向代理后面时 `--grpc-web` 必须开（TUI 默认开；env 里会带上）。

TUI 操作：`↑↓` 切字段 · `enter` 下一个/保存并验证 · `ctrl+s` 跳过验证保存 · `ctrl+t` grpc-web · `esc` 退出。
token 输入是掩码，可以直接粘贴；已有 token 时留空表示沿用。

## 2. CLI（agent 用）

```bash
argo-token status            # 服务器、token 文件、掩码、grpc-web、env 路径
argo-token status --json     # 给脚本解析
argo-token check             # 用已保存的 token 连服务器验证
argo-token check --json      # {ok, user, issuer, server_version} 或 {ok:false, error}

# 非交互写入：token 从 stdin 读，避免进 shell 历史
printf '%s' "$NEW_TOKEN" | argo-token set -server https://argocd.example.com

# 可选参数
argo-token set -server URL -token-file ~/.argocd/token -no-verify -no-grpc-web -json
argo-token clear             # 删 token 文件，配置保留
argo-token env               # 打印 ~/.zshrc 片段（不含 token）
```

退出码：

| 码 | 含义 | 处理 |
| --- | --- | --- |
| `0` | 写入成功 / 验证通过 / 查询成功 | 报结果 |
| `1` | 服务器拒绝 token（401/403），**没有写入任何文件** | 让用户换 token，别重试同一个 |
| `2` | 用法错误、网络不通、配置缺失 | 看 stderr；网络问题可重试 |

## 3. 文件位置

| 文件 | 内容 | 权限 |
| --- | --- | --- |
| `~/.config/argo-token/config.json`（macOS 在 `~/Library/Application Support/argo-token/`） | 服务器、token 路径、grpc-web 开关 | 0600 |
| `~/.argocd/token` | 只有 token | 0600 |
| `~/.argocd/env` | `ARGOCD_SERVER` / `ARGOCD_OPTS` / `ARGOCD_TOKEN_FILE`，由工具生成 | 0600 |

仓库里不出现这三类文件的真实内容。

## 4. shell 集成

`argo-token env` 打印的片段（`~/.zshrc` 里一份即可）：

```zsh
argocd() {
  [ -r "$HOME/.argocd/env" ] && source "$HOME/.argocd/env"
  local tok="${ARGOCD_TOKEN_FILE:-$HOME/.argocd/token}"
  if [ -r "$tok" ]; then
    ARGOCD_AUTH_TOKEN="$(<"$tok")" command argocd "$@"
  else
    [[ "$1 $2" != "version --client" ]] && \
      print -u2 "⚠ 还没配置 token：运行 argo-token 填写后再试"
    command argocd "$@"
  fi
}
```

每天改配置不用重开 shell：函数每次调用都会重新 source env 文件。

## 5. 常见问题

| 现象 | 原因 / 处理 |
| --- | --- |
| `Unauthenticated` / `401` | token 抄错、被撤销或过期；`argo-token` 重填。报错里有服务器原话，照着判断 |
| `没有列应用的权限`（403） | token 有效但角色缺 `applications, get/list`；补 RBAC 或换角色 |
| `连不上 https://…` | 地址不对、本机网络/代理、证书问题；先 `argo-token check` |
| `--grpc-web` 相关报错 | 服务器在 Traefik/nginx 后面，必须开 grpc-web；`ctrl+t` 或 `-no-grpc-web` 反向确认 |
| `PermissionDenied` | 只读角色，写操作本来就不允许；不是 token 问题 |
| `argocd: command not found` | 先装 argocd CLI（brew install argocd），本工具只负责配置 |
| TUI 打不开 | 需要真终端；在脚本/CI 里用 `argo-token set` |
| 想换 token 文件位置 | TUI 改「保存到」，或 `set -token-file PATH`；env 会跟着更新 |

## 6. 在本仓库里改代码时

- 版本号在 `main.go` 的 `var version`。
- 改完跑 `gofmt -l .`、`go vet ./...`、`go test ./...`、`go build -o argo-token .`。
- 测试一律用 `httptest` 假服务和临时 HOME，**不要碰真实的 `~/.argocd`**。
- 不要往仓库里写公司内部信息（真实 Argo 域名、账号、token），示例用 `argocd.example.com`、`alice`。
