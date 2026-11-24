# 客户端接入指南

> 前提：已读过 [`02-client-api.md`](02-client-api.md)，并能调通 `POST /api/v1/resolve`。

---

## 1. 通用原则

| 规则 | 说明 |
|---|---|
| 直链 5 分钟过期 | 播放前调用 `resolve`；失败或卡顿时重取 |
| 必须传 `decryption_key` | 少了就是花屏，不是报错 —— 最容易被误判为"网络问题" |
| **不要发 `Referer`** | 发错 → 403。网关返回的 `play_headers` 本就不含它，别自己加 |
| `play_headers` 逐条透传 | 不要只取 UA 而丢掉其他头（虽然目前只有 UA） |
| 视频流不走网关 | 播放器直连 CDN，别让网关做中转 |

---

## 2. 命令行播放器

### 2.1 ffmpeg / ffprobe —— 已实测

```bash
# ffprobe：先探一下流信息
ffprobe -v error -show_entries stream=index,codec_name,codec_type,width,height \
        -user_agent "<play_headers.User-Agent>" \
        -decryption_key "<decryption_key>" \
        "<play_url>"

# ffmpeg：转码或直存
ffmpeg -hide_banner -loglevel error \
       -decryption_key "<decryption_key>" \
       -user_agent "<play_headers.User-Agent>" \
       -i "<play_url>" -c copy out.mp4
```

**实测结果**（本机 `ffmpeg 7.x`）：

| 场景 | 结果 |
|---|---|
| 带 `-decryption_key` | `exit 0`，无错误输出，能正常解出帧 |
| 不带 `-decryption_key` | `Invalid NAL unit size (… > 6584)` / `Error splitting the input into NAL units` —— **花屏的根因** |

> `-decryption_key` 是 FFmpeg **mov/mp4 demuxer 的私有选项**，必须放在 `-i` **之前**。
> `-user_agent` 是 http 协议选项，同样在 `-i` 之前。
> 两者都**不是必需的**（实测 UA 无关紧要），但带上更稳。

### 2.2 mpv —— 推荐

```bash
mpv --demuxer-lavf-o=decryption_key=<decryption_key> \
    --http-header-fields="User-Agent: <UA>" \
    "<play_url>"
```

- `--demuxer-lavf-o` 把选项透传给 libavformat demuxer（此处即 `decryption_key`）
- `--http-header-fields` 传自定义头（多个头用 `\n` 分隔）
- 窗口播放器同理，把参数加到 mpv 的 `options` 数组里

### 2.3 VLC

VLC 没有直接传解密密钥的开关。两个可行路径：

1. 用 ffmpeg 解密后本地播放（推荐）：
   ```bash
   ffmpeg -decryption_key "<key>" -i "<url>" -c copy stream.mp4
   vlc stream.mp4
   ```
2. 用 `vlc://` + `--input-slave` 管道：
   ```bash
   vlc --demux=avformat "https://…"
   # 注意：此路径无法传 decryption_key，会花屏
   ```
   → 所以**路径 1 是唯一可用方案**。

---

## 3. 桌面应用

### 3.1 Electron / Tauri / C# WPF —— 调 ffmpeg

把 ffmpeg 作为子进程，参数见 §2.1。

**Electron 示例**

```js
const { spawn } = require('node:child_process');

function playStream({ play_url, play_headers, decryption_key }) {
  const args = [
    '-hide_banner', '-loglevel', 'error',
    '-decryption_key', decryption_key,
    '-user_agent', play_headers['User-Agent'] ?? '',
    '-i', play_url,
    '-c', 'copy', '-f', 'mp4', 'pipe:1',
  ];
  return spawn('ffmpeg', args);   // stdout 即是解密后的视频流
}
```

> **注意**：`-f mp4 pipe:1` 把解密后的 MP4 推到 stdout，浏览器可用 MSE 播放，见 §5。

### 3.2 Flutter / Dart —— 调 ffmpeg 或 libmpv

- **libmpv（media_kit）**：把 `demuxer-lavf-o=decryption_key=<key>` 和
  `http-header-fields=User-Agent: <UA>` 作为 mpv 属性设置。
  参考实现（`app.so`）用的正是这两项：`demuxer-lavf-o` / `http-header-fields`。
- **ffmpeg 子进程**：见 §2.1。

### 3.3 原生（macOS / iOS）

- **AVPlayer / AVURLAsset**：不暴露 ffmpeg 的 demuxer 私有选项 → **无法直接播**。
  路径：先 `ffmpeg -decryption_key … -c copy` 转成本地文件，或用
  `AVAssetResourceLoader` 自定义解密（工作量很大，不推荐）。
- **推荐**：统一走"服务端或本地 ffmpeg 转封装 → 本地文件/本地流"。

---

## 4. 移动端

### 4.1 Android（Media3 / ExoPlayer）

ExoPlayer 直接支持 CENC 但**不认 ffmpeg 的 `decryption_key`**。可行路径：

1. **服务端预解密转封装**（推荐，客户端最简单）
   网关提供 `/api/v1/media/{ticket}`，返回已解密的字节流，客户端按普通 MP4 播。
2. **本地 ffmpeg 转封装**
   Android 端集成 ffmpeg-kit（与原应用同款技术栈），执行：
   ```
   ffmpeg -decryption_key <key> -i <url> -c copy out.mp4
   ```
3. Media3 + 自定义 `HttpDataSource` + 自写解密 —— 需实现 `senc`/`tenc` 的 CENC 解析，
   工作量大且易错。

> **推荐路径 1**：移动端客户端只拿到普通 MP4，解密与转封装都在服务端完成。
> 这也顺带解决了 §5 的浏览器问题。

### 4.2 iOS

- **AVPlayer**：同 §3.3，需服务端预解密。
- **FFmpegKit / MobileVLCKit**：可传自定义选项，接近 FFmpeg CLI 用法。

---

## 5. 浏览器 —— 有硬限制，务必读

### 5.1 问题

片源是 **ISO-BMFF 加密封装**。实测文件内含：

```
encv   @ 0x1c9     ← 加密视频 sample entry（不是 avc1）
enca    @ 0x1ea04   ← 加密音频 sample entry
senc    @ 0x14756   ← 每样本 IV
sinf / schm / tenc  ← 保护方案信息
```

因此：

- `<video src="直链">` **不能播**（浏览器只认 `avc1`/`mp4a` 等未加密 entry）
- **MSE 也不行** —— MSE 无法直接消费 CENC 保护的 MP4（需要 CDM/EME，或自行拆包 + 解密 + 重封装）
- 好消息：CDN **开放 CORS**（`access-control-allow-origin: *`）且支持 Range（`206`），
  所以浏览器**能下载**，只是不能直接播。

### 5.2 三种可行方案

| 方案 | 做法 | 优点 | 缺点 |
|---|---|---|---|
| **A. 服务端预解密**（推荐） | 客户端请求 `/api/v1/media/{ticket}`，网关返回**已解密**的 MP4 字节流 | 客户端最简单，兼容所有浏览器 | 网关承担带宽 |
| B. 浏览器内解密 | fetch 整个文件 → WebCrypto 解密 → `mp4box.js` 重封装 → MSE | 网关零带宽 | 前端要实现 CENC + 重封装，复杂；大文件占内存 |
| C. 浏览器内解密 + 流式 | fetch Range 分片 → 增量解密 → 喂 MSE | 内存可控 | 难度最高，基本只有专业播放器能做 |

> **方案 A 的实现要点**：
> - `ticket` 由 `/api/v1/resolve` 一并下发（服务端签名的一次性票据，含 `vid` + `exp`）
> - 网关取到直链后，用 `decryption_key` 解密再流式回传（`Content-Type: video/mp4`）
> - 必须支持 `Range` 请求（浏览器会分段取）
> - 直链 5 分钟过期，网关应在过期前完成转封装或边转边发

### 5.3 浏览器端示例（方案 A）

```ts
// 1) 解析
const { data } = await postJSON('/api/v1/resolve', { series_id, episode_no });
const ticket = data.media_ticket;   // 方案 A 时由网关额外下发

// 2) 直接播（网关返回的是已解密 MP4）
const video = document.querySelector('video');
video.src = `${GATEWAY}/api/v1/media/${ticket}`;
video.play();
```

---

## 6. 下载（存本地）

```bash
# 直存解密后的 MP4
ffmpeg -hide_banner -loglevel error \
       -decryption_key "<key>" -user_agent "<UA>" \
       -i "<play_url>" -c copy "<标题>.mp4"

# 断点续传（先 HEAD 拿长度）
curl -sI -A "<UA>" "<play_url>" | grep -i content-length
```

> 直链 5 分钟过期，**大文件下载请先确认剩余有效期**，必要时让服务端提供长时下载票据。

---

## 7. 客户端缓存建议

| 数据 | 客户端 TTL | 说明 |
|---|---|---|
| `resolve` 结果 | 60s | 切集来回不必重求 |
| `series` 分集表 | 15 min | 与上游一致 |
| `catalog` 列表 | 5 min | — |

**过期兜底**：播放报 403 / 花屏 / 无数据时，先看 `expires_at`；已过则重新 `resolve`。

---

## 8. 常见问题

| 症状 | 原因 | 处理 |
|---|---|---|
| 播放器报错 / 无画面，容器能打开 | **缺 `decryption_key`** | 传给 ffmpeg `-decryption_key` 或 mpv `--demuxer-lavf-o=decryption_key=` |
| NAL 解析错误、花屏 | 同上，或密钥与当前集不匹配 | 重新 `resolve` |
| HTTP 403 | 客户端自己加了 `Referer` | 去掉 `Referer` |
| HTTP 403（直链） | 直链已过期 | 重新 `resolve` |
| 浏览器播不了 | CENC 加密 MP4，浏览器不支持 | 走 §5.2 方案 A |
| 老设备黑屏但有声音 | 编码是 HEVC，设备不支持 | 请求时降级 `quality`，或让服务端优先 `prefer_codec: ["h264"]` |
| 首帧很慢 | 直链是 CDN 重定向链 | 正常；必要时让服务端预热 |
| `resolve` 返回 40901 | 上游明确拒绝该集/该请求 | 换集或检查账号态 |

---

## 9. 客户端自检清单

接入完成后，逐项确认：

- [ ] `resolve` 返回的 `decryption_key` 非空时，播放确实用了它
- [ ] 播放请求**没有**带 `Referer`
- [ ] `play_headers` 里的头全部透传给播放器
- [ ] 直链过期（`expires_at` 过期）能自动重新 `resolve`
- [ ] 收到 429 时按 `Retry-After` 退避
- [ ] 收到非 0 `code` 时读的是 `code` 而不是 HTTP 状态码
- [ ] 日志/埋点里**没有** `play_url` 与 `decryption_key`
- [ ] 切集时旧播放器被正确释放（避免直链未过期但已换集）
