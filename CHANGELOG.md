# Changelog

## [0.1.2] - 2026-11-26

### 修复

- 从 Windows 同步过来的配置能用了：`token_file` 里的 `~\` 也当作家目录（以前只认 `~/`），`~\\.argocd\\token` 在本机落到 `~/.argocd/token`，不会指向一个不存在的文件。多端同步时路径本来就该随机器走，这里两头都兜住
- 读到配置就顺手核对 `~/.argocd/env`：内容对不上（比如刚从别的机器同步下来、还是那边的路径）就按本机配置重写，拉完配置不用手动再保存一次。时间戳不参与比较，没变化不重写
- shell 包装函数多一行兜底：`ARGOCD_TOKEN_FILE` 指向的文件读不到时退回 `$HOME/.argocd/token`，旧配置留下的反斜杠路径不会再让 `argocd` 找不到 token

## [0.1.1] - 2026-11-23

### 修复

- 粘贴进来的 token 先清洗：去掉 `Bearer ` 前缀、`ARGOCD_AUTH_TOKEN=`、外面的引号和折行/空格。Argo CD 只认正好三段的 JWT，带着这些杂质会被当成没登录，报「userinfo 说未登录」；存盘的也是洗干净的 token（`saveToken` / `readToken` 一起改）
- 验证失败时不再只说「userinfo 说未登录」：Argo CD 对不认的 token 也回 200 + `loggedIn:false`，现在再拿 `/api/v1/applications` 问一次，把服务器的真实说法（`invalid session: signature is invalid`、token 已撤销、没权限…）显示出来，才能知道该换 token 还是改权限
- 项目角色 token 这类能调接口但 userinfo 不给身份的，applications 通了就算验证通过，不再误拒

## [0.1.0] - 2026-11-23

首个版本。

- TUI：服务器、token（掩码粘贴）、token 文件路径、grpc-web 开关；保存前用
  `/api/v1/session/userinfo` 验证，401/403 时不写任何文件
- 配置 `config.json`、token 文件、shell env 文件都按 0600 原子写入，目录 0700
- env 里 `ARGOCD_SERVER` 只写主机名；`http://` 服务器自动加 `--plaintext`
- CLI：`status`、`check`、`set`（token 走 stdin）、`clear`、`env`、`skill`、`version`
- `argo-token env` 打印 zsh 包装函数：token 只注入到调用 argocd 的那一个进程
- agent skill 随二进制内嵌，`argo-token skill` 写入 `~/.claude/skills/argo-token/SKILL.md`
