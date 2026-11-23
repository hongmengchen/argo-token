# argo-token

给这台机器上的 `argocd` CLI 配置 Argo CD 服务器和只读 token 的小工具。

启动 TUI，填服务器、粘贴 token，验证通过后写入本机 0600 文件；`~/.zshrc` 里的
`argocd()` 包装函数每次调用都把 token 注入到那一个进程。之后 `argocd app list`
直接用，不用每次 `argocd login`。

- 地址、token、存放路径都在本机，**仓库里不出现任何真实域名或凭据**
- token 只写进 `0600` 文件，输出里永远只有掩码
- 验证失败（401/403）绝不覆盖原来能用的 token
- 不依赖 `argocd login`，也不把 token 放进全局环境

## 安装

```bash
go build -o argo-token . && install -m 0755 argo-token ~/.local/bin/
```

还需要官方 CLI：

```bash
brew install argocd        # macOS
argo-token version         # 本工具版本
argocd version --client    # CLI 版本
```

## TUI

```bash
argo-token
```

```
 argo-token  v0.1.0  配置 Argo CD CLI
  填好服务器、粘贴 token，验证通过后写入本机；argocd 每次调用自己带 token。

 ❯ 服务器      https://argocd.example.com
   token       ••••••••••••••
   保存到      ~/.argocd/token
   grpc-web    [开]  ctrl+t 切换（服务器在反向代理后面时开）

   ↑↓ 切字段  ·  enter 下一个/保存验证  ·  ctrl+s 跳过验证保存  ·  ctrl+t grpc-web  ·  esc 退出
```

- token 输入是掩码，可以直接粘贴；已有 token 时留空表示沿用
- 服务器可以只写域名，自动补 `https://`
- 服务器在 Traefik / nginx 后面时 `--grpc-web` 必须开（默认开），env 里会带上
- 保存成功会显示登录身份和服务器版本；失败会说明是 token 被拒还是网络不通

## shell 集成

把下面这段放进 `~/.zshrc`（`argo-token env` 会打印同样的内容）：

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

`~/.argocd/env` 由工具生成，只有地址、`--grpc-web`、token 路径，没有 token 本身。
函数每次调用都会重新 source 它，所以在 TUI 里改完配置不用重开 shell。

`ARGOCD_SERVER` 只写主机名（`argocd` CLI 不认 `https://` 前缀，会拼成 `https://https//…`）；
服务器用 `http://` 时自动加 `--plaintext`。

## 命令

```bash
argo-token                   # TUI（默认）
argo-token status            # 当前配置（不回显 token）
argo-token status --json
argo-token check             # 用已保存的 token 验证
argo-token check --json
argo-token set -server https://argocd.example.com    # token 从 stdin 读
argo-token clear             # 删 token 文件，配置保留
argo-token env               # 打印 shell 片段
argo-token skill             # 写 agent skill
argo-token version
```

`set` 的选项：

| 参数 | 作用 |
| --- | --- |
| `-server <url>` | Argo CD 地址（第一次写入必填） |
| `-token-file <path>` | token 存哪，默认 `~/.argocd/token` |
| `-token <value>` | 直接给值（不推荐，会留在 shell 历史里；默认 stdin） |
| `-no-verify` | 不联网验证就写入（离线时用） |
| `-no-grpc-web` | 不设 `--grpc-web` |
| `-json` | 输出 JSON |

退出码：

| 码 | 含义 |
| --- | --- |
| `0` | 写入成功 / 验证通过 / 查询成功 |
| `1` | 服务器拒绝 token（401/403），没有写入任何文件 |
| `2` | 用法错误、网络不通、配置缺失 |

## 文件

| 文件 | 内容 | 权限 |
| --- | --- | --- |
| `~/.config/argo-token/config.json`（macOS：`~/Library/Application Support/argo-token/`） | 服务器、token 路径、grpc-web | 0600 |
| `~/.argocd/token` | 只有 token | 0600 |
| `~/.argocd/env` | `ARGOCD_SERVER`（主机名）/ `ARGOCD_OPTS` / `ARGOCD_TOKEN_FILE` | 0600 |

写入都是「同目录临时文件 + rename」，不会留下半个文件。

## 安全

- 这是给**只读账号**用的：写操作会返回 `PermissionDenied`，属于预期
- token 只在真正调用 `argocd` 的进程里出现（`ARGOCD_AUTH_TOKEN`），不 export 到全局
- `status` / `check` / TUI 输出里的 token 一律是掩码
- `argo-token clear` 随时删本地 token；吊销请去 Argo CD 的 Accounts 页面

## 常见问题

| 现象 | 处理 |
| --- | --- |
| `Unauthenticated` / 401 | token 抄错、过期或被撤销；`argo-token` 重填 |
| 连不上 | 地址、网络/代理、证书；先 `argo-token check` |
| gRPC 相关报错 | 反向代理后面必须 `--grpc-web`；确认 env 里 `ARGOCD_OPTS` |
| `PermissionDenied` | 只读角色，写操作本来就不允许 |
| `argocd: command not found` | 先装 argocd CLI，本工具只负责配置 |

## 开发

```bash
gofmt -l . && go vet ./... && go test ./... && go build -o argo-token .
```

测试全部用 `httptest` 假服务和临时 `HOME`，不碰真实的 `~/.argocd`。

## License

MIT
