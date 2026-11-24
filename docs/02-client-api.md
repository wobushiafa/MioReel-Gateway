# 客户端 API 契约

> **本文档是客户端团队的主文档。** 目标：看完能独立实现「拿到播放直链并播起来」。

- Base URL：`https://<your-gateway-host>`（部署后由运维提供）
- 协议：HTTP/1.1 + JSON，请求体 `Content-Type: application/json`
- 编码：UTF-8
- 鉴权：见 [§8](#8-鉴权与安全)，默认 `Authorization: Bearer <token>`

---

## 1. 设计原则（客户端只需要记住这几条）

1. **客户端不做任何密码学。** 签名、地址解码、AES 密钥提取全部在服务端完成。
2. **直链是 bearer token，约 5 分钟过期。** 拿到就尽快用；过期重新调 `resolve` 即可。
3. **必须同时使用 `play_url` + `play_headers` + `decryption_key` 三件套。**
   - 少 `decryption_key` → 播放器能读到容器但**画面是乱码**（实测 NAL 全部损坏）。
   - `play_headers` 里的 `User-Agent` 实测**非必需**（不带也能 200），但建议照传以获得最稳的连通性。
4. **不要向 CDN 发送 `Referer`。** 实测：发错 Referer → `403`；缺省或 `https://novelquickapp.com/` → `200`。网关返回的 `play_headers` **不含** `Referer`，客户端也**不要自己加**。
5. **播放链路不经过网关。** 网关只回传直链，客户端播放器直连 CDN。
6. **编码可能是 HEVC。** 实测下发流为 `hevc 1920x1080` + `aac`。老设备/部分浏览器不支持 HEVC，服务端 `variants` 数组会同时给出可用清晰度，客户端可按需降级。

---

## 2. 通用响应包络

**所有**端点（`/health` 除外）统一返回：

```json
{
  "code": 0,
  "message": "ok",
  "request_id": "01HQ8Z...",
  "data": { }
}
```

| 字段 | 类型 | 说明 |
|---|---|---|
| `code` | int | `0` = 成功；非 0 见 [§7](#7-错误码) |
| `message` | string | 人类可读描述，成功时为 `"ok"`；失败时含可排障信息 |
| `request_id` | string | 请求追踪 ID，排障时提供给运维 |
| `data` | object | 业务数据；失败时为 `null` |

失败时 HTTP 状态码仍为 `200`，**客户端必须读 `code` 而非 HTTP 状态码**。
（`/health` 与 `/media` 除外，见各自说明。）

### 2.1 请求追踪 ID

所有响应都带 `X-Request-Id` 头（24 位 hex，网关生成）。客户端也可自带：

```http
X-Request-Id: client-trace-20261116-001
```

网关会原样回显。排障时把这个值给运维，可在服务端日志里精确定位该请求。

### 2.2 哪些端点需要鉴权

| 端点 | 鉴权 |
|---|---|
| `/api/v1/health` | **不需要**（探针用） |
| `/metrics` | **不需要**（请在反代层限制来源 IP） |
| `/api/v1/media/{ticket}` | 不需要 Token —— 票据本身即凭据（HMAC 签名） |
| 其余全部 | **需要** Token（+ 可选 HMAC） |

---

## 3. `GET /api/v1/health` — 健康检查

```json
{
  "status": "ok",
  "version": "1.0.0",
  "uptime_ms": 4821300,
  "signing": {
    "profile_id": "646637794",
    "sdk_version": "v04.10.03-ml-android",
    "rotated_at": "2026-11-16T03:12:00Z"
  },
  "upstream": {
    "device_ready": true,
    "last_success_at": "2026-11-16T05:35:28Z",
    "consecutive_failures": 0
  }
}
```

`status` 取值：`ok` / `degraded` / `down`。
客户端一般只在诊断页面使用；`degraded` 表示签名材料已轮换或上游不稳定，但服务仍可用。

> **本端点不需要鉴权**（便于探针），但会暴露 `profile_id`。不希望公开时请在反代层限制来源 IP。
> `status` 判定规则：`degraded` = 材料降级 **或** 连续失败 ≥3；`down` = 连续失败 ≥10 **且** 设备未就绪。

---

## 4. `GET /api/v1/catalog` — 分类剧目列表

> 上游红果端点映射、分类语义、分页游标规则见 [`07-source-browsing.md`](07-source-browsing.md) §2–§4。

**Query 参数**

| 参数 | 必填 | 默认 | 说明 |
|---|---|---|---|
| `category` | 否 | `real-drama` | `real-drama` 真人剧 / `comic-drama` 漫剧 / `ai-drama` AI 剧 |
| `page` | 否 | `1` | 页码，从 1 起 |
| `limit` | 否 | `40` | 每页条数，上限 100 |

**响应 `data`**

```json
{
  "page": 1,
  "page_count": 99,
  "total": 6,
  "list": [
    {
      "series_id": "7701678491724418073",
      "title": "二婚嫁对人，前夫悔破防",
      "cover": "https://p3-novel.byteimg.com/novel-pic/xxxx~tplv-shrink:640:0.image",
      "episode_count": 80,
      "remark": "全80集",
      "intro": "……",
      "tags": ["都市", "甜宠"],
      "year": "2024"
    }
  ]
}
```

> `page_count` 是上游给出的软上限（实测恒为 99），**不是真实总页数**。客户端应以 `list` 是否为空作为终止条件。

---

## 5. `POST /api/v1/series` — 剧集详情 + 分集表

> 上游双轨取数（App API 主路 + 网页兜底）见 [`07-source-browsing.md`](07-source-browsing.md) §5.3。

**请求体**

```json
{ "series_id": "7701678491724418073" }
```

**响应 `data`**

```json
{
  "series_id": "7701678491724418073",
  "title": "二婚嫁对人，前夫悔破防",
  "cover": "https://p3-novel.byteimg.com/novel-pic/xxxx~tplv-shrink:640:0.image",
  "intro": "……",
  "total_episodes": 80,
  "accessible_episodes": 80,
  "episodes": [
    { "no": 1, "vid": "7701678491724418073", "title": "第1集", "available": true },
    { "no": 2, "vid": "7701678491724418074", "title": "第2集", "available": true }
  ]
}
```

`vid` 是**解析直链的真正凭据**，客户端应持久化它 —— 之后可以跳过 `series` 直接调用 `resolve`：

```json
{ "vid": "7701678491724418073", "quality": 1080 }
```

---

## 6. `POST /api/v1/resolve` — ★ 解析播放直链

### 6.1 请求体

两种入参方式，**二选一**：

```jsonc
// 方式 A：按剧集号（客户端面向用户的自然选择）
{ "series_id": "7701678491724418073", "episode_no": 1 }

// 方式 B：直接给 vid（客户端已缓存分集表时）
{ "vid": "7701678491724418073" }
```

可选字段：

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `quality` | int | `0`（自动） | 目标短边像素：`1080` / `720` / `480`。`0` 表示取可用最高 |
| `prefer_codec` | string[] | `["h264","hevc","mp4"]` | 编码偏好顺序。**建议保留 `h264` 优先**（兼容性最好） |
| `timeout_ms` | int | `15000` | 客户端等待上限；服务端会据此裁剪上游重试 |

### 6.2 响应 `data`

```json
{
  "source": "hongguo",
  "series_id": "7701678491724418073",
  "series_title": "二婚嫁对人，前夫悔破防",
  "episode_no": 1,
  "episode_id": "7701678491724418073",
  "episode_title": "第1集",

  "play_url": "https://v5-reading-video.qznovelvod.com/9fd7ad299396469a6548479331e566ab/6afae9ff/video/tos/cn/tos-cn-v-6fcc8e/xxx",
  "play_headers": {
    "User-Agent": "com.phoenix.read/71332 (Linux; U; Android 16; zh_CN; 25053RT47C; Build/BP2A.250605.031.A3; Cronet/TTNetVersion:04657795 2026-01-23 QuicVersion:c67e9834 2025-09-08)"
  },
  "decryption_key": "982735c47051044342ede4ef5d3e8cb5",
  "encryption": "aes-128-cbc",

  "media_type": "video/mp4",
  "size_bytes": 9131576,

  "quality": 1080,
  "width": 1080,
  "height": 1920,
  "codec": "hevc",
  "duration_ms": 0,

  "expires_at": 1794808528211,
  "expires_in_ms": 300000,

  "variants": [
    {
      "url": "https://v5-reading-video.qznovelvod.com/.../xxx",
      "quality": 1080,
      "width": 1080,
      "height": 1920,
      "codec": "hevc",
      "decryption_key": "982735c47051044342ede4ef5d3e8cb5",
      "expires_at": 1794808528211
    }
  ],

  "danmaku_id": "hongguo:7701678491724418073:7701678491724418073"
}
```

### 6.3 字段说明（重点）

| 字段 | 客户端怎么用 |
|---|---|
| `play_url` | 交给播放器的媒体地址。**不要拼接、不要改写** |
| `play_headers` | 播放器附加的 HTTP 头。逐条透传给播放器，**不要加 `Referer`** |
| `decryption_key` | **AES-128 内容密钥（32 位 hex）**。为空字符串表示该流未加密，可直接播 |
| `encryption` | `aes-128-cbc` / `none`。客户端据此决定是否传解密参数 |
| `expires_at` | Unix 毫秒时间戳。**客户端应在此之前重新 resolve** |
| `expires_in_ms` | 剩余有效期（实测 300000 = 5 分钟） |
| `variants[]` | 同集的所有可用清晰度。首项 = 已选中的那条。**降级时改用下一项的 `url` + `decryption_key`** |
| `codec` | `h264` / `hevc` / `mp4`。客户端可据此判断设备能否解码 |
| `danmaku_id` | 弹幕关联 ID，形如 `hongguo:{series_id}:{vid}`。不需要弹幕可忽略 |

### 6.4 客户端播放调用

```bash
# ffmpeg —— 实测可用（带密钥 exit=0，无密钥画面损坏）
ffmpeg -decryption_key 982735c47051044342ede4ef5d3e8cb5 \
       -user_agent "com.phoenix.read/71332 (Linux; U; Android 16; zh_CN; 25053RT47C; ...)" \
       -i "https://v5-reading-video.qznovelvod.com/.../xxx" \
       -c copy out.mp4

# mpv
mpv --demuxer-lavf-o=decryption_key=982735c47051044342ede4ef5d3e8cb5 \
    --http-header-fields="User-Agent: com.phoenix.read/71332 (...)" \
    "https://v5-reading-video.qznovelvod.com/.../xxx"
```

各平台完整示例见 [`04-client-integration.md`](04-client-integration.md)。

---

## 7. 错误码

| `code` | 含义 | 客户端建议动作 |
|---|---|---|
| `0` | 成功 | — |
| `40001` | 参数非法（`series_id`/`vid`/`episode_no` 格式错误） | 不重试，提示用户 |
| `40401` | 剧集/分集不存在 | 刷新分集表 |
| `40901` | 上游拒绝该请求（`BUSINESS_REJECTED`） | 不重试（上游明确拒绝） |
| `42201` | 该集暂无兼容片源（编码/清晰度都不满足） | 试 `variants` 或降级 `quality` |
| `42901` | 触发限流 | 退避重试（建议 2s 起） |
| `50001` | 服务端内部错误（含签名失败） | 重试一次；持续失败则上报 `request_id` |
| `50201` | 上游网络错误 / DNS / 超时 | 退避重试（指数退避，上限 3 次） |
| `50301` | 上游全部路由均失败 | 退避重试；持续失败提示"上游繁忙" |
| `50401` | 解析超时（超过 `timeout_ms`） | 退避重试 |

响应 `message` 会附带上游原始错误码，便于排障：

```json
{
  "code": 50301,
  "message": "上游视频模型接口全部路由失败: routesExhausted, last=MISSING_MODEL",
  "request_id": "01HQ8Z...",
  "data": null
}
```

完整错误码语义与排障决策树见 [`05-error-codes.md`](05-error-codes.md)。

---

## 8. 鉴权与安全

### 8.1 认证

默认使用静态 Token：

```http
Authorization: Bearer <token>
```

也支持自定义头 `X-API-Key: <key>`。**二选一**，由运维下发配置。

### 8.2 直链泄漏风险（重要）

`play_url` 本身是有效凭据 —— **任何拿到它的人都能直接下载该集**（实测：无需 UA、无需 Referer、CDN 开放 CORS）。

因此：

- 必须启用鉴权，**不要把网关暴露为匿名可调用**。
- 建议为网关配置 **IP 白名单 / mTLS**（若客户端是自有 App）。
- 不要把 `play_url` 写进客户端本地日志、崩溃上报、埋点。
- 直链 5 分钟后失效，这是唯一的天然缓解，**不要依赖它做访问控制**。

### 8.3 请求签名（可选，高安全场景）

网关可开启 HMAC 模式，客户端需带上：

| 头 | 计算方式 |
|---|---|
| `X-Timestamp` | Unix 秒 |
| `X-Nonce` | 随机串（≥16 字符） |
| `X-Signature` | `HMAC-SHA256(secret, method + "\n" + path + "\n" + timestamp + "\n" + nonce + "\n" + sha256(body))`，小写 hex |

---

## 9. 完整调用时序

```
客户端                                    网关                              上游
  │                                        │                                │
  │─ GET /api/v1/catalog ─────────────────▶│                                │
  │                                        │── POST bookmall/tab ──────────▶│
  │◀─ { list:[{series_id, episode_count}] }│◀── 200 ───────────────────────│
  │                                        │                                │
  │─ POST /api/v1/series ─────────────────▶│                                │
  │                                        │── POST video_detail ─────────▶│
  │                                        │◀── { video_data:{ video_list } }│
  │◀─ { episodes:[{no, vid}] } ────────────│                                │
  │                                        │                                │
  │─ POST /api/v1/resolve ────────────────▶│                                │
  │                                        │─ device_register (首次/过期) ──▶│
  │                                        │── POST video_model (签名) ─────▶│
  │                                        │◀── { video_model: {video_list} }│
  │                                        │  解码 main_url / spade_a       │
  │                                        │  HEAD 探活 → 选流              │
  │◀─ { play_url, play_headers, ──────────│                                │
  │      decryption_key, expires_at }      │                                │
  │                                        │                                │
  │─ 播放器直连 CDN（不经过网关）────────────────────────────────────────────▶│
```

---

## 10. 缓存策略

### 服务端侧

`resolve` 结果按 `(series_id, vid, quality)` 缓存，TTL = `expires_at - now - 30s`（约 4.5 分钟）。同一集重复调用直接命中缓存，**不产生上游请求**。

客户端可放心对同一集重复调用 `resolve`（服务端做了去重）。

### 客户端侧

| 建议 | TTL | 理由 |
|---|---|---|
| 缓存 `resolve` 结果 | 60 秒 | 同一集切回来不用重新请求 |
| 缓存 `series` 分集表 | 15 分钟 | 与上游一致 |
| 缓存 `catalog` | 5 分钟 | 列表变化慢 |

**过期处理**：播放报 403/无效数据时，先检查 `expires_at` 是否已过；已过则重新 `resolve` 再播。
注意过期后旧 URL 仍然 200（CDN 不校验时间），但**内容仍是加密的** —— 真正的失效信号是密钥/地址与当前集不匹配。

---

## 11. 限流

| 维度 | 默认 |
|---|---|
| 单 Token QPS | 20 |
| 单 Token 并发 | 8 |
| 单 IP QPS | 50 |

超限返回 `42901`，并带 `Retry-After` 头（秒）。客户端应遵循 `Retry-After`。

---

## 12. 调用示例

### curl

```bash
curl -sS -X POST https://<gateway>/api/v1/resolve \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"series_id":"7701678491724418073","episode_no":1,"quality":1080}'
```

### TypeScript / JavaScript

```ts
export interface ResolvedPlay {
  play_url: string;
  play_headers: Record<string, string>;
  decryption_key: string;
  encryption: 'aes-128-cbc' | 'none';
  quality: number;
  codec: string;
  expires_at: number;   // unix ms
  variants: Array<{ url: string; quality: number; codec: string; decryption_key: string }>;
}

export async function resolvePlay(
  opts: { seriesId: string; episodeNo?: number; vid?: string; quality?: number },
  token: string,
): Promise<ResolvedPlay> {
  const res = await fetch(`${GATEWAY}/api/v1/resolve`, {
    method: 'POST',
    headers: { 'Authorization': `Bearer ${token}`, 'Content-Type': 'application/json' },
    body: JSON.stringify({
      series_id: opts.seriesId,
      episode_no: opts.episodeNo,
      vid: opts.vid,
      quality: opts.quality ?? 0,
    }),
  });
  const body = await res.json();
  if (body.code !== 0) throw new Error(`resolve failed: ${body.code} ${body.message}`);
  return body.data as ResolvedPlay;
}
```

### Go

```go
type ResolveRequest struct {
    SeriesID  string `json:"series_id,omitempty"`
    EpisodeNo int    `json:"episode_no,omitempty"`
    VID       string `json:"vid,omitempty"`
    Quality   int    `json:"quality,omitempty"`
}

type ResolveData struct {
    PlayURL       string            `json:"play_url"`
    PlayHeaders   map[string]string `json:"play_headers"`
    DecryptionKey string            `json:"decryption_key"`
    Encryption    string            `json:"encryption"`
    Quality       int               `json:"quality"`
    Codec         string            `json:"codec"`
    ExpiresAt     int64             `json:"expires_at"`
    Variants      []Variant         `json:"variants"`
}
```

完整类型见 [`server/types.go`](../server/types.go)。

### Python

```python
import requests

def resolve_play(series_id: str, episode_no: int, token: str) -> dict:
    r = requests.post(
        f"{GATEWAY}/api/v1/resolve",
        headers={"Authorization": f"Bearer {token}"},
        json={"series_id": series_id, "episode_no": episode_no},
        timeout=20,
    )
    body = r.json()
    if body["code"] != 0:
        raise RuntimeError(f"resolve failed: {body['code']} {body['message']}")
    return body["data"]
```

---

## 13. `GET /api/v1/danmaku` — 弹幕

弹幕按**剧集**（不是按剧）组织，`danmaku_id` 取自 `resolve` 返回的 `danmaku_id`。

### 13.1 Query

| 参数 | 必填 | 默认 | 说明 |
|---|---|---|---|
| `danmaku_id` | 是 | — | 形如 `hongguo:{series_id}:{vid}` |
| `start_ms` | 否 | `0` | 本片段起点（毫秒） |
| `duration_ms` | 否 | `300000` | 片段长度（毫秒），上限 86400000 |

### 13.2 响应 `data`

```json
{
  "episode_id": "hongguo:7701678491724418073:7701680675979545625",
  "start_ms": 0,
  "next_ms": 30000,
  "items": [
    { "id": "7xxxxxxxx", "time_ms": 1234, "text": "太好看了", "color": "#FFFFFF" }
  ]
}
```

| 字段 | 说明 |
|---|---|
| `start_ms` / `next_ms` | 本段起点 / 下一段起点。**客户端循环拉取时用 `next_ms` 作为下一次的 `start_ms`** |
| `items[].time_ms` | 相对剧集起点的毫秒偏移 |
| `items[].color` | `#RRGGBB` |
| `items` | 单次最多 **90 条** |

> 弹幕是**按 30 秒一段**返回的。客户端应循环拉取以实现滚动弹幕。

---

## 14. `GET /api/v1/media/{ticket}` — 浏览器媒体代理（可选）

**只有 Web / 浏览器客户端需要它。** 桌面与移动端直接用 `resolve` 的直链 + 密钥即可，不需要网关承担带宽。

### 14.1 为什么需要

片源是 **ISO-BMFF 加密封装**（`encv`/`enca`/`senc`/`tenc`），浏览器（`<video>` 与 MSE 都）无法直接消费。
本端点由网关用 ffmpeg **预解密并转封装**后返回普通 MP4。

### 14.2 用法

1. 调 `resolve` —— 响应里会多一个 `media_ticket` 字段（启用代理时才有）。
2. 直接把票据交给 `<video>`：

```html
<video src="https://<gateway>/api/v1/media/<media_ticket>" controls autoplay></video>
```

### 14.3 行为

| 项 | 说明 |
|---|---|
| 鉴权 | 不需要 Token —— 票据本身即凭据（HMAC-SHA256 签名，防篡改） |
| 支持 Range | 是（`Accept-Ranges`），浏览器可分段取 |
| Content-Type | `video/mp4` |
| Cache-Control | `private, max-age=60` |
| 解密方式 | 网关调用 `ffmpeg -decryption_key <key> -user_agent <ua> -i <直链> -c copy -movflags +faststart` |
| 缓存 | 解密后的 MP4 落盘到 `media_cache_dir`，按 `sha256(vid\|key)` 命名，同一集并发只解一次 |
| 票据有效期 | 与直链一致（≤5 分钟），过期 → `40101`「媒体票据已过期」 |

### 14.4 代价

- 网关承担**全部视频带宽**（直链本身是零成本的，代理不是）。
- 需要在服务器安装 ffmpeg。
- 首次访问某集时会等待 ffmpeg 完成解密（短剧单集约 10~90 MB，耗时数秒到数十秒）。

> 容量评估与限流建议见 [`10-deployment.md`](10-deployment.md) §5.1。

---

## 15. `GET /metrics` — Prometheus 指标

Prometheus 文本格式，**无鉴权**（务必在反代层限制来源 IP）。

| 指标 | 类型 | 标签 | 含义 |
|---|---|---|---|
| `resolve_total` | counter | `code` | 解析请求总数（按网关错误码） |
| `resolve_duration_ms` | histogram | — | 解析耗时，桶 50/100/250/500/1000/2500/5000/10000/15000/30000 ms |
| `upstream_request_total` | counter | `route`, `code` | 上游请求数 |
| `upstream_fallback_total` | counter | — | 上游路由降级次数 |
| `profile_rotated_timestamp` | gauge | — | 签名材料最近轮换时间（Unix 秒） |
| `cache_hit_ratio` | gauge | — | resolve 缓存命中率（0~1） |

完整说明与告警建议见 [`10-deployment.md`](10-deployment.md) §6。

---

## 16. 端点速查

| 方法 | 路径 | 用途 | 鉴权 | 客户端必需 |
|---|---|---|---|---|
| GET | `/api/v1/health` | 健康检查 | 否 | 否 |
| GET | `/api/v1/catalog` | 分类剧目列表 | 是 | 是 |
| POST | `/api/v1/series` | 剧集详情 + 分集表 | 是 | 是 |
| **POST** | **`/api/v1/resolve`** | **解析播放直链** | 是 | **是** |
| GET | `/api/v1/search?q=` | 搜索 | 是 | 否 |
| GET | `/api/v1/danmaku?danmaku_id=` | 弹幕 | 是 | 否 |
| GET | `/api/v1/media/{ticket}` | 浏览器媒体代理 | 票据 | 仅 Web 客户端 |
| GET | `/metrics` | Prometheus 指标 | 否 | 否 |
