# 自己的公开分享站点

一个 Cloudflare Worker + 私有 R2 存储桶，无数据库、无常驻服务器、无前端框架。
使用 Cloudflare 免费分配的 `https://xnote-share.<你的子域>.workers.dev`，无需购买或绑定域名。
分享链接不设到期时间；域名、账号、部署和文件需要持续保留。免费额度及账户配额仍适用。

## 一次性部署

需要 Bun 1.4.2+、Node.js 22+（Wrangler 运行时）、Go，以及已经启用 R2 的 Cloudflare 账号。
R2 的开通与付款方式要求由 Cloudflare 决定；脚本不会代为开通付费计划。

在仓库根目录：

```sh
scripts/build.sh
cd web
bun install --frozen-lockfile
bunx wrangler login
bun run setup
```

自定义录音库可以使用 `bun run setup --data /absolute/library`。
脚本会列出/创建 `xnote-shares` 桶，部署 `xnote-share` Worker，生成管理密钥，
将密钥存入录音库 `.env`（0600），通过 Worker secret 配置同一个密钥，并设置 `share_url`。
密钥不输出到终端、不写入源码，不上传整个录音库。已有管理密钥会复用。
若遇到 R2 403，请重新 `bunx wrangler login` 并检查账号权限。
使用自己的 API Token 时，需要此账号的 Workers Scripts 编辑与 R2 编辑权限。
多账号用户可通过 `CLOUDFLARE_ACCOUNT_ID` 选择目标账号。

部署完成后重启已打开的 XNote，让它加载新增环境变量。
同一个录音库再次运行 setup 会复用密钥与桶。普通网页代码更新只需要：

```sh
cd web
bun install --frozen-lockfile
bun run deploy
```

不要开启桶的公开访问或 `r2.dev`。公开读取必须经过 Worker 的分享状态检查。

## 日常使用

TUI 的「更多」菜单支持「公开分享」「分享链接 / 复制」「取消分享」。
发布前可以勾选包含音频；默认只分享文字。Linux 复制链接需要 `wl-copy`、`xclip` 或 `xsel`；
macOS 使用 `pbcopy`，也可以直接从 URL 输入框选取。
「设置 → 分享站点」可以编辑站点 URL，密钥始终放在本地 `.env`。

```sh
xnote share RECORDING_ID             # 文字和已生成的选中 AI 输出
xnote share RECORDING_ID --audio     # 另外包含音频
xnote share show RECORDING_ID        # 查看状态和 URL
xnote share revoke RECORDING_ID      # 撤销整个分享
```

重复发布会更新原链接。撤销后再发布会创建全新的随机链接，原链接保持失效。
本地重命名、转写更新、生成总结或移入回收站不会自动改变已发布的快照：需显式更新或取消分享。
更新为文字分享会移除音频访问。成功撤销后所有公开入口都返回 404；已下载或已开始传输的副本无法收回。
单个音频上限 95 MiB，JSON 文档上限 2 MiB。超限会明确报错，不会截断内容。
网络中断后重新执行相同命令会复用分享 ID；未成功发布的音频片段可能保留在私有桶中。

## Summary / Mindmap

复用现有 DOWAY AI 文字后处理，而非在 Cloudflare 上运行模型。
「设置 → AI 标题与摘要」新增两个独立选项：

- 生成 Summary 摘要：默认开启。
- 生成 Mindmap 思维导图：默认关闭。

自动生成开关与输出选项独立，仍默认关闭。两个输出都关闭时不会排入新的 AI 任务。
已有排队任务保留入队时的输出选项。改变选项不会自动重算；可以手动再次生成需要的新组合。
两种输出在同一次模型请求中生成，复用原来的持久化任务记录与失败保护。

```sh
xnote config summary_enabled true
xnote config mindmap_enabled true
xnote summarize RECORDING_ID         # 入队，需要 TUI 或 xnote watch 运行
xnote share RECORDING_ID             # 生成完成后发布
```

总结需要有效的 DOWAY 会话与本地应用签名/模板配置，实际模型由 DOWAY 服务端返回。
模型名会随新结果记录；没有元数据的旧结果只显示 DOWAY。
Mindmap 保存为结构化 JSON 及 `mindmap.md` 大纲，以树形卡片呈现，不执行模型生成的 HTML 或脚本。
关闭某个输出后，新发布的分享不会包含它。转写文本/说话人/时间信息发生变化后，旧 AI 结果不再随分享发布。

## Agent / curl 接口

所有读取接口无需 Cookie、JavaScript、API Key 或登录。链接持有人有读取权限；随机 ID 不是身份验证。
页面正文直接存在于 HTML 中，`<link rel="alternate">` 与 HTTP `Link` 头标明数据入口。
下面的 `SHARE_URL` 是完整的 `/s/<随机ID>` 链接：

```sh
curl -fsS "$SHARE_URL.json"           # 标题、原文、segments、insights、audio
curl -fsS "$SHARE_URL.md"             # 完整 Markdown
curl -fsS "$SHARE_URL/transcript.md"  # 仅原文
curl -fsS "$SHARE_URL/summary.md"     # 仅摘要；未生成/未分享时 404
curl -fsS "$SHARE_URL/mindmap.md"     # 思维导图大纲；不存在时 404
curl -fsS -H 'Accept: application/json' "$SHARE_URL"
curl -fsS -H 'Accept: text/markdown' "$SHARE_URL"
curl -fsS -H 'Range: bytes=0-1023' "$SHARE_URL/audio"
```

JSON 的 `segments` 保留 `start`、`end`（秒）、`speaker`、`text` 和 `timing`。
`timing: "chunk"` 表示分段起点，只是近似时间，页面标注 `≈`；不会伪造逐句时间或说话人。
`insights.mindmap` 是 `{title, branches: [{title, points: [...]}]}`。
`audio` 为同源相对 URL，未分享音频时是 `null`。没有全库列表接口。

写接口只供可信 CLI 使用：`PUT /api/shares/:id` 发布 JSON；
`PUT /api/shares/:id/audio/:revision` 流式上传 MP3（需要 Content-Length）；
`DELETE /api/shares/:id` 撤销；均需要 `Authorization: Bearer <管理密钥>`。
ID 是 24 字节随机数的 48 位十六进制编码，音频版本是 16 字节随机数。
更新用 R2 条件写避免覆盖并发撤销；撤销保留一个无内容标记，阻止旧请求复活原链接。

## 安全边界

- 整个录音库默认私有。只上传显式分享的录音、白名单字段和所选输出，不上传设备序列号、本地路径或账号凭据。
- 访问链接即允许读取。需要按用户鉴权的私密协作不在此版本范围内。
- 管理密钥只放在本机 `.env` 和 Cloudflare secret，不能放入网页、Git、聊天或公开构建配置。
- Markdown 禁止原始 HTML、危险 URL 和外链图片；思维导图与时间线使用转义文本。
- 使用 CSP、no-referrer、no-store、noindex；不使用第三方脚本、字体、分析或公开搜索目录。
- 上传请求不跟随重定向，避免把管理密钥带给其他站点。
- 公共访问不会触发模型请求；Cloudflare 仍会按其存储和请求规则计量。
- 服务只支持一个所有者的管理密钥。密钥泄露时需在 Cloudflare 轮换 secret 并更新本机 `.env`。

## 本地开发和验证

```sh
cd web
# 私下创建 .dev.vars，写入 SHARE_ADMIN_TOKEN=<至少32字符随机值>
bun install --frozen-lockfile
bun test
bun run dev
```

本地 R2 与生产桶隔离。CLI 的 `share_url` 只允许 HTTPS；为 Wrangler 开发允许 localhost HTTP。
Go 测试覆盖发布/更新/撤销、密钥重定向保护、内容白名单、AI 输出选项、缓存隔离和并发锁。
Worker 测试覆盖鉴权、XSS、数据格式、Range、撤销与并发发布，以及默认不生成 mindmap。
