# X NOTE

一个 Linux / macOS 终端录音库。使用设备已有绑定自动连接、下载录音；默认只下载，转写由设置或手动操作启用。TUI 或 `watch` 运行期间持续同步，退出程序后停止。

## 使用

```sh
git clone git@github.com:sicko7947/xnote.git
cd xnote
scripts/build.sh
./dist/xnote
```

默认目录：`~/Documents/XNote`。无需 Python。已在 Apple Silicon Mac 和 X NOTE
HD5GA00725 上验证原生蓝牙连接、下载及 Codex Dictate 转写。

### 终端内自动同步

macOS 需要开启蓝牙，并允许启动 XNote 的终端访问蓝牙。Linux 需要运行中的 BlueZ、
已开启的蓝牙适配器，以及播放用的 `mpv`。`ffmpeg` 用于可选离线转写。

```sh
scripts/build.sh
./dist/xnote config device_serial HD5GA00725  # 改成自己设备的序列号
./dist/xnote config automatic true
./dist/xnote config automatic_transcription false  # 默认只下载
./dist/xnote config provider doway  # 按设置选择，不自动切换服务
./dist/xnote doctor
./dist/xnote
```

需要手机 DOWAY 释放蓝牙连接，并让录音器处于开机、可广播状态；充电本身不证明设备正在广播。
`c` 查看连接详情；`./dist/xnote status` 输出供 Agent 使用的 JSON。

在仓库目录的 tmux 中，可以在旁边新开一个 pane 运行 TUI：

```sh
tmux split-window -h -c "$PWD" './dist/xnote'
```

只需要同步时可运行 `./dist/xnote watch`，用 Ctrl+C 停止。TUI 中按 `q` 退出。
tmux detach 后，只要该 pane 的进程仍在运行就会继续同步；退出 XNote 或关闭该 pane 后停止。
设备离开后持续重试，启动时蓝牙未就绪也会等待。电脑睡眠时不能同步。

Key 放在录音库的 `.env`，CLI、TUI 和 `watch` 都会读取，已导出的环境变量优先：

```sh
cp .env.example ~/Documents/XNote/.env
chmod 600 ~/Documents/XNote/.env
# 在本机编辑 ELEVENLABS_API_KEY，不要把真实 Key 写入仓库或命令历史。
```

也可用 `XNOTE_ENV_FILE=/absolute/private.env` 指定文件。支持 `KEY=value`、单/双引号和
`export KEY=value`，值按字面读取，不执行 shell 命令、不展开变量。修改环境文件后重新启动 XNote。

- **Tab / Shift+Tab** 切换区域和按钮；**/** 搜索正文、标题和说话人；**?** 查看帮助。
- **n** 列出匹配的时间片段；**g** 输入时间跳转；**[ / ]** 上下片段；**m** 操作；列表里 **q** 退出。
- 列表和详情显示已下载音频时长，不必先播放；底部始终标明实际播放的录音。
- **设置 → 常规**：中文 / English / 日本語、自动下载、自动转写、转写方式、录音语言、转写并发数。自动转写默认关闭。
- 设置 → 账号：DOWAY 登录和云端同步；无需登录即可使用本地流程。
- **Enter / 双击** 查看正文，**Esc** 回录音库，**空格** 播放所选 / 暂停 / 继续；「操作」只放当前录音的重命名、转写、文件夹和删除。
- 底部 **播放所选**：点击进度条定位，←/→ 跳转 10 秒，空格暂停，+ 切换倍速。
- 本地回收站可以恢复；删除设备原件需要输入 `DELETE`，不会删除本地副本。
- 自动下载包含设备中已有录音。断线自动重连、部分文件续传；不自动删除设备文件。
- 真正的转写失败不会无限重试消耗额度；菜单里可重试。空转写单独标为“未识别到语音”。

## 转写方式

常规设置按 **DOWAY → ElevenLabs → Codex Dictate → 自带 API → 离线** 排列，默认选择 DOWAY。
只使用当前选择的服务，不做自动 fallback。默认关闭自动转写及自动云端导入；登录不会立即导入旧转写。
下载和手动排队的转写可以同时工作；想自动处理新下载的录音，可开启“自动转写”。

转写默认同时处理 **4 条录音**，在 **设置 → 常规 → 转写并发数** 调整为 1–16，保存后立即生效。
降低并发数会等待已有请求完成；不会取消、重新提交正在处理的录音。顶部显示运行、排队和并发上限，
`c` 查看所有运行中的任务。**m → 转写全部待处理录音** 会先显示数量，再将确认时仍未转写的下载录音排队；
**m → 暂停转写队列 / 继续转写队列** 控制新任务启动，让运行中的任务完成，不改变自动转写设置。
手动排队优先于自动处理。并发上限是客户端同时处理的录音数，远端服务仍可能限流。

```sh
xnote config transcription_concurrency 4
xnote config transcription_paused true   # 等待运行中的任务完成
xnote config transcription_paused false  # 继续队列
```

- **DOWAY**：邮箱登录后可手动转写本机已下载录音；包含额度验证、上传、任务提交、轮询及结果恢复。需要在常规设置选择录音语言，并在 DOWAY 设置明确允许原 App 的上传方式。
  该方式在处理期间提供可直接下载的录音链接，持链接即可读取；终态后清理上传副本。本地录音保留。上传许可默认关闭。
  设置 → 账号可手动同步或明确选择云端记录导入；开启自动转写后每 5 分钟同步已完成的云端结果。
  仅以明确云端 ID 或唯一的精确音频 MD5 关联，不猜日期；手动标题保留，旧转写归档到 history。
  原 App 未提供已验证的全语种自动识别请求；中文、英文、日文等明确语言已接入。中断后保留任务，不盲目重复提交。[协议和验证范围](docs/doway-transcription.md)。
- **ElevenLabs**：Scribe v2，读取 `ELEVENLABS_API_KEY`，返回说话人和真实时间片段。
  Key 可放在录音库私有 `.env`；选择 `xnote config provider elevenlabs`。
  录音语言设为 Auto 时省略语言提示，由服务自动识别；`xnote config transcription_language auto`。
  在服务支持范围内直接流式上传原始录音，保留整段说话人标记，避免先转为多段 WAV 的额外流量。
- **Codex Dictate**：可选，调用已有的本机代理 `127.0.0.1:8377`。
  代理独立管理登录，XNote 不读取 Codex 凭据；音频由代理发送到它配置的服务。
  当前只返回文字；长录音中的 `≈` 表示分段起点，并非逐句时间戳，不会伪造说话人。
- **自带 API**：配置 HTTPS multipart 地址、模型和 Key 环境变量名。
  支持纯文字及 `segments`（start/end/speaker/text），可选择 `whisper-1` 时间戳或
  `gpt-4o-transcribe-diarize` 说话人预设。真实 API Key 尚待验收。
- **离线**：需要本地 `whisper-cli`（whisper.cpp）、模型和 `ffmpeg`。
  可选模型/工具不包含在 binary 中，Go 版真实模型尚待验收。

`doctor` 只检查本机依赖、配置和服务连接；不验证远端额度。缺少依赖时保留待处理队列。
已提交请求的鉴权、额度或网络失败会显示错误，修复后手动重试，避免重复收费。
HTTP 429 明确拒绝请求时，按服务端等待时间暂停该服务的新任务，最多自动重试两次；其他失败不自动重放。
需要客户端分段的服务会缓存成功片段，重试可继续；不同请求的说话人标签保持独立。
点击转写时间点可跳转播放。默认只下载时不会提交录音或导入云端转写。

## AI 标题与摘要

**设置 → AI 标题与摘要** 配置 DOWAY 文字后处理。它使用已有的 DOWAY 登录，与转写服务独立，
ElevenLabs、Codex Dictate 等已完成的转写也能生成标题、关键词及 Markdown 摘要。
摘要语言默认**跟随录音语言**，按该条转写的文字脚本判断中文、英文或日文；也可固定为其中一种，
或跟随界面语言。摘要并发默认 **2**，可调为 **1–8**，独立于转写并发。
支持的 Qwen 模型默认使用快速模式；可打开“深度思考”提高复杂内容的分析深度，通常需要更多时间。
其他模型保留服务端的模式，不向不支持的模型发送该参数。
摘要语言属于请求的一部分：改动它会将已有摘要标记为待处理，重新排队即按新设置重算，不再静默复用旧语言的结果。

- 选中已转写录音，**m → 生成 AI 摘要**（或 `i`）；请求前失败或服务端明确拒绝时可在同一入口手动重试，请求结果不明时不会重发。
- **m → 生成全部待摘要录音**（菜单内 `I`）先确认数量，再批量排队；已有相同内容的成功摘要不会重复生成。
- 开启“新转写完成后自动生成摘要”，只处理之后完成的新转写，不回填历史录音。新录音库默认关闭该开关。
- 详情同时显示 AI 标题、关键词、摘要和原始转写。默认日期标题会替换为生成标题；手动或云端标题保留，AI 建议标题仍可在详情查看。
- `summary.md` 单独导出；原录音、`transcript.md` 和逐句时间轴保留。摘要失败不影响已完成的转写。
- 当前没有独立的带时间戳章节功能；Markdown 摘要可能包含模板生成的章节标题。

```sh
xnote config automatic_summary true
xnote config summary_concurrency 2
xnote config summary_thinking false   # 支持的 Qwen 模型；true 开启深度思考
xnote config summary_language auto     # 默认：跟随每条录音；zh-CN / en / ja 固定；空值跟随界面
xnote summarize RECORDING_ID          # TUI 或 watch 运行时处理
```

DOWAY 模型配置通过当前登录会话向服务端取得，模型及鉴权以该响应为准；不自动切换其他 AI 服务。
生成和用量登记各自保存进度；已经取得的结果会保留，登记未确认时显示警示，不自动重复生成或重复登记。
[协议与验证范围](docs/doway-summary.md)。

界面使用 [tview](https://github.com/rivo/tview) 表格、表单、下拉框和鼠标事件。
主界面是一张录音表：录制时间、标题 / 摘录、时长、处理状态。
**1 全部 / 2 已转写 / 3 待处理** 可点击或直接按数字切换；回收站在设置 → 高级。
单击或 ↑↓ 选录音，**Enter / 双击** 打开正文，**Esc** 回到原来的选中行。
**空格** 播放所选 / 暂停 / 继续；底部播放器显示实际播放的录音，空格暂停 / 继续。
没有自定义或云端标题时，带引号的文字是转写摘录，只用于显示，不修改文件名。
支持 80×24 起的终端；正文单独呈现，后台刷新不会改变当前选中项。

## 键盘操作

底部只有一条操作栏，按钮和快捷键共用同一入口；打开设置或搜索时，这一行改为当前操作提示。其他功能收在 **m 更多** 和 **? 帮助**。搜索和输入框内按键只编辑文字。

| 按键 | 操作 |
| --- | --- |
| ↑↓ / PageUp / PageDown | 选择录音或滚动正文 |
| Enter / Esc | 查看录音 / 返回；输入重命名或跳转时间后 Enter 确认 |
| Tab / Shift+Tab | 顺序 / 反向切换搜索、分类、播放器和按钮 |
| 1 / 2 / 3 | 全部 / 已转写 / 待处理 |
| / 或 Ctrl+F | 搜索；Enter 或 ↓ 看结果 |
| 空格 | 播放所选 / 暂停 / 继续；聚焦按钮时执行按钮 |
| ← / →、g、+ / - | 快退快进 10 秒、输入时间跳转、调整倍速 |
| F2（或 r） / d / t / f | 重命名 / 下载 / 转写 / 打开录音文件夹 |
| m / s / ? | 录音操作 / 设置 / 完整帮助 |
| n、[ / ] | 时间片段列表、上一片段 / 下一片段 |

分类和按钮获得焦点后，←→ 移动、Enter 执行、↑↓ 返回内容区。
菜单支持 ↑↓ + Enter，也可按左侧标出的字母或数字。**Delete / Backspace（Mac 的删除键）**：本地录音先确认再移入回收站；尚未下载的设备录音会打开设备删除确认。
**m → 删除设备原件**：打开设备原件删除确认，必须输入 `DELETE`；仅排队，连接成功执行后才算删除。
**b** 打开本地回收站，**u** 恢复；不会用本地删除替代设备删除。
设置表单用 Tab 移动、Enter 展开选项、空格勾选、Ctrl+S 保存；
下拉框展开时 Esc 先收起选项，Tab 收起并移动到下一项。
常规设置的自动处理复选框位于选项下方，方框在文字左侧；Tab 聚焦，空格切换，也可以点击整行。修改后 Ctrl+S 保存，Esc 取消。

## 连接和设置的状态

顶部区分 **设备连接、自动重连、自动下载、自动转写**。开启自动下载不代表蓝牙已经连接。
按 **c**（或设置 → 连接详情）查看记住的设备、当前连接和最近状态更新时间。
TUI 或 `watch` 运行时会自动重连；自动下载与自动转写各自控制，不关闭重连。
超过 45 秒没有状态更新时显示同步未运行，不沿用旧的“已连接”。

- 常规：语言、自动下载、自动转写、转写方式和录音语言均可保存；Ctrl+S 保存，Esc 取消。
- Codex：转写方式页面会检查本机代理连接；当前返回文字，不能提供真正的说话人识别。
- 自带 API / 离线：需要配置服务与 Key，或本地模型及依赖；真实可用性需要实际转写验证。
- DOWAY：需有效登录会话、对应录音器和明确录音语言；是否允许转写由服务端额度验证决定。
- 同步随 TUI 或 `watch` 进程运行；`status`、`list` 等一次性 CLI 命令不会启动持续同步。

## 自己的公开分享链接

Cloudflare Worker + 私有 R2，使用免费分配的 `workers.dev` 地址，无需域名或数据库。
网页支持 Markdown、播放器、说话人时间线、Summary 和可选 Mindmap；Agent 可以直接 `curl` 读取 JSON / Markdown。

```sh
scripts/build.sh
cd web
bun install --frozen-lockfile
bunx wrangler login
bun run setup
```

部署一次后，在录音「更多 → 公开分享」里发布并复制链接；默认仅文字，可选包含音频。
重复发布更新原链接，「取消分享」撤销访问。AI 设置中 Summary 默认开启、Mindmap 默认关闭；
沿用现有 DOWAY AI 队列生成，生成完再发布。分享网站不调用模型。

```sh
xnote config mindmap_enabled true    # 可选
xnote summarize RECORDING_ID        # 需要 TUI / watch 处理队列
xnote share RECORDING_ID --audio
xnote share show RECORDING_ID
xnote share revoke RECORDING_ID
```

[部署、安全边界与 curl 接口](docs/public-sharing.md)。管理密钥自动存入私有录音库 `.env`，不会进入仓库。

## AI / CLI

```sh
xnote search '项目 预算' --json
xnote list --json
xnote cloud list
xnote cloud sync
xnote cloud show CLOUD_UID
xnote cloud import LOCAL_ID CLOUD_UID
xnote show HD5GA00725-20260709220314 --json
xnote status
xnote doctor
xnote transcribe RECORDING_ID
xnote summarize RECORDING_ID
xnote download RECORDING_ID
xnote --data /absolute/library watch
```

`--data` 是全局参数，放在子命令前。JSON 输出在 stdout，错误在 stderr。
搜索是大小写不敏感的关键词 AND 匹配，支持中文；不需要向外部服务发送搜索文本。

```text
~/Documents/XNote/
  config.json
  .env                   # 可选私有 Key，0600 权限；不会显示在 config/doctor 中
  recordings/2026/09/HD5GA00725-20260929104048/
    audio.mp3
    transcript.md
    summary.md             # 可选 AI 标题、关键词和摘要
    metadata.json
    segments.json          # 后端片段或明确标注的分段起点
  .work/                 # 同步状态、锁、登录会话、尚未执行的命令
```

搜索 JSON 的 `matches` 提供命中片段、说话人和时间；`duration_seconds` 是音频时长。
目录 ID 固定；改标题不会更改路径。录音年月来自设备文件名，并按本机时区解释。
`metadata.json` 保存状态、来源、转写和音频路径；`transcript.md` 可直接被 AI 或全文搜索工具读取。
下载中的 `audio.mp3.partial` 不会被提交转写。同步完成的音频先校验 MP3，再公开到录音库。
项目只维护 Go CLI / TUI；macOS 音频播放通过少量 Objective-C / cgo 调用系统框架。

## 构建与检查

需要 Go 1.27.1 或更新版本。Linux 使用 BlueZ 和外部 `mpv`；macOS 需要 Xcode Command Line Tools，蓝牙和播放器链接系统框架。

```sh
scripts/build.sh
go test -race ./...
go vet ./...
```

产物：`dist/xnote`、`dist/xnote.sha256`。本机 ad-hoc 签名，尚未做 Developer ID
签名/Apple 公证；对外发布前仍需完成这些发布步骤。Linux 不运行 codesign。
Linux 已验证构建、mpv 生命周期和 Codex / ElevenLabs 真实示例音频转写；
已连接 HD5GA00725 并读取 104 条录音目录，但实体传输出现重复尾包，完整下载尚未通过验收。
Linux 命令和音频通知使用 BlueZ `AcquireNotify` 保持接收顺序；这并未消除实机重复包。
遇到超出预期大小的数据会丢弃不可信的本地片段；设备原录音不受影响。Wi-Fi 仍待验收。

实现拆分：`device.go` 设备协议、`engine.go` 同步队列、`store.go` 文件存储、
`transcribe.go` 转写、`ui.go` 界面、`player_darwin.*` 本机播放器。
磁盘文件是事实来源；进程锁避免重复蓝牙 worker；跨进程修改使用文件锁及原子替换。

## 可选 DOWAY 构建配置

普通构建无需密钥，可使用设备同步及 Codex / API / 离线转写。DOWAY 云端请求另需应用签名配置：

```sh
cp internal/xnote/app_profile.example.json internal/xnote/app_profile.json
# 在本机填写 signing_key，再重新构建。
scripts/build.sh
```

`app_profile.json` 被 Git 忽略，构建时嵌入 binary；缺少配置时云端请求会明确报错。
`signing_key` 用于应用请求签名；可选的 `template_aes_key`（32 字节）和 `template_aes_iv`（16 字节）
用于解包账号接口返回的模板与模型配置。它们不是用户登录凭据或模型供应商 API Key。
账号登录会话始终保存在录音库的 `.work/`；模型凭据只使用登录后服务端返回的配置。
不要把填写后的配置、个人录音、转写、账号会话或包含私有配置的构建产物提交到仓库。

## 项目结构

```text
cmd/xnote/       CLI 入口
internal/xnote/  设备、同步、转写、存储、TUI 和 Go 测试
scripts/        Linux / macOS 构建脚本及蓝牙权限说明
docs/          传输调查和已知限制
```

## 性能与验收边界

下载和转写并行，但同一条 BLE 音频流不发送多个文件请求。App 的 Wi-Fi 高速通道
已定位到 TCP 8475；本设备本次探测没有能力回复，尚不能启用。
详见 [性能调查](docs/transfer-performance.md)。

待验收：长录音服务限制、实体断线续传、Mac 睡眠唤醒和隔夜运行、其他设备固件。
Mac 睡眠时不能进行实时同步。新 binary 不代表已完整复刻 DOWAY 所有功能。

## 本机研究资料

`artifacts/` 保存本机 APK、解包和反汇编资料，被 Git 忽略，不上传到仓库，也不参与 Go 构建。
仓库不使用 Git LFS；普通 `git clone` 即可获取完整源码。
