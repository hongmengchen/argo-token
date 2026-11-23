# Changelog

## [0.1.0] - 2026-11-23

首个版本。

- TUI：服务器、token（掩码粘贴）、token 文件路径、grpc-web 开关；保存前用
  `/api/v1/session/userinfo` 验证，401/403 时不写任何文件
- 配置 `config.json`、token 文件、shell env 文件都按 0600 原子写入，目录 0700
- env 里 `ARGOCD_SERVER` 只写主机名；`http://` 服务器自动加 `--plaintext`
- CLI：`status`、`check`、`set`（token 走 stdin）、`clear`、`env`、`skill`、`version`
- `argo-token env` 打印 zsh 包装函数：token 只注入到调用 argocd 的那一个进程
- agent skill 随二进制内嵌，`argo-token skill` 写入 `~/.claude/skills/argo-token/SKILL.md`
