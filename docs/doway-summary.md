# DOWAY AI 后处理

AI 后处理与录音转写独立：输入是本地已有转写文本，输出为标题、关键词、Markdown 摘要。
不重新上传原录音，不切换转写服务。自动模式只在之后的成功转写完成时排队，已有转写通过单条或批量入口处理。
当前没有独立的带时间戳章节功能；Markdown 摘要可能包含模板生成的章节标题。

## 账号与模型配置

3.7.9 的 `useServerAiModel` 分支通过登录会话领取模型配置：

```text
POST https://www.dowayai.com:8443/api/player/summary_prompt_v2
playerId, templateId, langCode, localeCode, modelName, sn, token
```

请求沿用 DOWAY 的签名和会话校验。响应 `code == 200` 后，`data` 包含：

```text
templateId, langCode, respLangCode, tips,
modelName, apiUrl, apiKey, apiKeyHeaderName, apiKeyHeaderPrefix, contentType
```

`apiKey` 是该账号请求返回的包装值，使用原 App 的 AES-256-CBC / PKCS7 包装协议解开；
包装参数放在被 Git 忽略的本地 `app_profile.json` 中。没有复制原 App 内置的共享模型供应商凭据。
实际请求使用响应中的模型、完整 HTTPS 地址和鉴权头；禁止重定向和自动请求重放，不记录模型凭据或响应中的敏感配置。

原 App 默认模型选择与设备区域有关，但服务端可以返回不同的实际模型，因此不能把请求中的模型名硬写到生成请求中。
`langCode` 控制输出语言，`localeCode` 表示界面语言，`zh` / `en` / `ja` 与转写语言设置独立。

当前使用原 App 的会议模板 `2000001`（version 3，服务端返回明文提示词），
在同一次模型请求中要求输出标题、关键词、Markdown 摘要 JSON，减少重复发送全文。
对已确认支持的 DashScope Qwen 3.5 Plus 模型，`summary_thinking` 显式控制思考模式；
其他模型不发送此扩展字段。[阿里云参数说明](https://help.aliyun.com/zh/model-studio/deep-thinking/)。
同一已验证模型使用 `response_format: {"type":"json_object"}` 请求结构化输出；
默认非思考模式支持 JSON 格式约束，思考模式仍需本地校验。[阿里云结构化输出说明](https://www.alibabacloud.com/help/en/model-studio/qwen-structured-output)。

## 本地队列与结果

- 摘要有独立的 `queued / running / done / error` 状态、并发设置及状态文件。
- 摘要失败不修改录音转写状态。关闭自动模式不会重算历史，重复点击相同输入的已完成任务不会再次请求。
- 修改摘要语言或思考模式只影响后续新任务，不会自动重算相同输入的已完成摘要。
- 请求前失败或服务端明确拒绝时可手动重试；请求结果不明时不会重发，包括手动重试和程序重启。
- 在录音锁内领取任务；输入摘要包括转写文本及片段。处理中输入改变时，不把新结果覆盖到另一份转写上。
- 生成标题只替换默认日期标题或之前自动生成的标题，保留用户手动标题和云端标题。
- 结果保存在录音的 metadata 与独立 `summary.md` 中；原录音及转写文件保留。
- 程序退出时取消并等待运行任务；用量登记未确认时保留结果和警示，不重复登记。
- 已完整返回并缓存的旧结果，若仅最后一个 Markdown 字符串含未转义引号或换行，可在本地修复；拒绝损坏前缀、缺失结束结构及额外字段，不再次调用模型或登记用量。

## 逆向证据与验证边界

原本已解包的 3.7.7 主要使用静态模型配置，不足以证明新版账号配置路径。
3.7.9 的静态分析使用腾讯应用宝官方页面可核对文件大小与 MD5 的国内渠道包；
它与原 APKPure Google Play 包的签名不同。关键方法、字段及包装参数已在原 ARMv7 包中交叉确认。

本地证据目录（均被 Git 忽略）：

```text
artifacts/doway/3.7.9/candidate-j9p/README.analysis.md
artifacts/doway/3.7.9/candidate-j9p/decompiled/asm/doway/
  dowayCommon/dao/user_profile_dao.dart
  dowayView/openai/openai_mgr.dart
  dowayView/openai/template/template_mgr.dart
  dowayView/openai/templatePlus/model/template_prompt_response.dart
  dowayView/openai/templatePlus/template_aes_encryption_utils.dart
```

账号实测已确认 `summary_prompt_v2` 返回成功的模型配置，包括动态地址和鉴权字段。
2026-10-05 使用同一段人工合成的中英日文本，两次独立测试均取得标题、3 个关键词、Markdown 摘要，
并收到用量登记成功响应。保持模型默认模式的一次耗时约 84 秒；关闭思考的一次约 30 秒，
这是两次短文本测试的观测值，不是延迟保证。结果和用量 journal 只保存在本地被忽略的测试目录中。
后续结构化输出请求也取得严格有效的 JSON 和用量登记成功响应。另用一条真实录音验证了缓存格式修复、
标题更新、关键词、`summary.md` 保存及 TUI 显示；原音频和转写正文保持完整。
计费公式、额度扣减时机与服务端幂等保证无法从客户端代码确定；本地不将这些假设作为自动重试依据。
