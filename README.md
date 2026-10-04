# X NOTE

一个 Mac 原生终端录音库。配对后自动连接、下载、转写；关闭界面后可由后台服务继续工作。

## 使用

```sh
git clone git@github.com:sicko7947/xnote.git
cd xnote
scripts/build.sh
./dist/xnote
```

默认目录：`~/Documents/XNote`。无需 Python。已在 Apple Silicon Mac 和 X NOTE
HD5GA00725 上验证原生蓝牙连接、下载及 Codex Dictate 转写。

- **Tab / Shift+Tab** 切换区域和按钮；**/** 搜索正文、标题和说话人；**?** 查看帮助。
- **n** 列出匹配的时间片段；**g** 输入时间跳转；**[ / ]** 上下片段；**m** 操作；列表里 **q** 退出。
- 列表和详情显示已下载音频时长，不必先播放；底部始终标明实际播放的录音。
- **设置 → 常规**：中文 / English / 日本語、自动流程、转写方式、录音语言。
- 设置 → 账号：DOWAY 登录和云端同步；无需登录即可使用本地流程。
- **Enter / 双击** 查看正文，**Esc** 回录音库，**空格** 播放所选 / 暂停 / 继续；「操作」只放当前录音的重命名、转写、文件夹和删除。
- 底部 **播放所选**：点击进度条定位，←/→ 跳转 10 秒，空格暂停，+ 切换倍速。
- 设置 → 高级 → 开机自动同步：安装当前用户的 LaunchAgent，退出界面后继续同步。
- 本地回收站可以恢复；删除设备原件需要输入 `DELETE`，不会删除本地副本。
- 自动模式包含设备中已有录音。断线自动重连、部分文件续传；不自动删除设备文件。
- 真正的转写失败不会无限重试消耗额度；菜单里可重试。空转写单独标为“未识别到语音”。

## 转写方式

- **codex**：调用已有的 Codex Dictate 本机代理 `127.0.0.1:8377`。代理独立管理登录，
  xnote 不读取 Codex 凭据。实际音频会由代理发送到它配置的服务。
- **api**：配置 HTTPS multipart 转写地址、模型和 API Key 环境变量名。
  支持纯文字及 `segments`（start/end/speaker/text）。设置内可选择 `whisper-1` 时间戳或
  `gpt-4o-transcribe-diarize` 说话人预设。后台 LaunchAgent 的环境与交互 shell 不同，
  需确保该环境变量对后台进程可用；`doctor` 检查当前进程的配置。
- **offline**：调用本地 `whisper-cli`（whisper.cpp）、模型文件和 `ffmpeg`。
  这些可选模型/工具不包含在 binary 中。Go 版这一模式尚未做真实模型验收。
- **DOWAY**：邮箱登录及云端列表已有实现，云端已有标题和结构化转写的读取/导入已实现，真实账号登录仍未通过验收。
  登录后以显式云端 ID 或唯一的精确音频 MD5 匹配自动关联，每 5 分钟更新；不猜日期。
  手动标题保留，旧转写归档到 history。不能自动匹配的录音可在云端列表明确选择并导入。
  云端上传、额度转写和分享尚未完成。

长录音自动分段，优先在较安静处切分；已成功片段缓存，失败后重试可继续。
Codex 当前只返回文字：长录音的 `≈` 是分段起点，并非逐句时间戳，不会伪造说话人。
支持说话人的 API 片段显示时间与标签；跨请求的说话人标签保持独立，避免错误合并。
点击转写里的时间点可直接播放对应位置。API 说话人模式已做请求/解析测试，尚未用真实 API Key 验收。

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

顶部把 **设备连接、自动重连、自动处理** 分开显示。开启自动处理不代表蓝牙已经连接。
按 **c**（或设置 → 连接详情）查看记住的设备、当前连接、最近状态更新时间和后台同步是否安装。
后台或程序运行时会自动重连；自动处理开关只控制下载和转写，不关闭重连。
超过 45 秒没有状态更新时显示同步未运行，不沿用旧的“已连接”。

- 常规：语言、自动处理、转写方式和录音语言均可保存；Ctrl+S 保存，Esc 取消。
- Codex：转写方式页面会检查本机代理连接；当前返回文字，不能提供真正的说话人识别。
- 自带 API / 离线：需要配置服务与 Key，或本地模型及依赖；真实可用性需要实际转写验证。
- DOWAY：需有效登录会话才能读取云端数据；新的云端转写提交仍未完成认证协议验收。
- 开机后台同步：需要安装用户级 LaunchAgent。安装状态和当前蓝牙连接是不同的状态。

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
xnote --data /absolute/library watch
xnote service install
xnote service status
xnote service stop
```

`--data` 是全局参数，放在子命令前。JSON 输出在 stdout，错误在 stderr。
搜索是大小写不敏感的关键词 AND 匹配，支持中文；不需要向外部服务发送搜索文本。

```text
~/Documents/XNote/
  config.json
  recordings/2026/09/HD5GA00725-20260929104048/
    audio.mp3
    transcript.md
    metadata.json
    segments.json          # 后端片段或明确标注的分段起点
  .work/                 # 后台状态、锁、登录会话、尚未执行的命令
```

搜索 JSON 的 `matches` 提供命中片段、说话人和时间；`duration_seconds` 是音频时长。
目录 ID 固定；改标题不会更改路径。录音年月来自设备文件名，并按本机时区解释。
`metadata.json` 保存状态、来源、转写和音频路径；`transcript.md` 可直接被 AI 或全文搜索工具读取。
下载中的 `audio.mp3.partial` 不会被提交转写。同步完成的音频先校验 MP3，再公开到录音库。
项目只维护 Go CLI / TUI；macOS 音频播放通过少量 Objective-C / cgo 调用系统框架。

## 构建与检查

需要 Go 1.27.1 或更新版本、Xcode Command Line Tools；蓝牙和播放器链接 macOS 系统框架。

```sh
scripts/build.sh
go test -race ./...
go vet ./...
```

产物：`dist/xnote`、`dist/xnote.sha256`。本机 ad-hoc 签名，尚未做 Developer ID
签名/Apple 公证；对外发布前仍需完成这些发布步骤。当前只验收 macOS arm64。

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
它是应用请求签名配置，不是用户登录凭据。账号登录会话始终保存在录音库的 `.work/`。
不要把填写后的配置、个人录音、转写、账号会话或包含私有配置的构建产物提交到仓库。

## 项目结构

```text
cmd/xnote/       CLI 入口
internal/xnote/  设备、同步、转写、存储、TUI 和 Go 测试
scripts/        macOS 构建脚本及蓝牙权限说明
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
