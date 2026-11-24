# 架构

## 1. 全景

```
┌─────────────────────────────────────────────────────────────────────┐
│  客户端（任意语言 / 任意平台）                                        │
│                                                                     │
│   UI ──► Gateway Client ──► POST /api/v1/resolve                   │
│                                  │                                  │
│                                  ▼                                  │
│                            拿到 play_url + play_headers             │
│                                  + decryption_key                   │
│                                  │                                  │
│                                  ▼                                  │
│                         播放器 (mpv / ffmpeg / ExoPlayer / …)        │
│                                  │                                  │
└────────────────────────────────┼────────────────────────────────────┘
                                 │  直连 CDN，不过网关
                                 ▼
              v{5,9,11}-reading-video.qznovelvod.com
                                 ▲
                                 │  服务端解析出的 URL
┌────────────────────────────────┴────────────────────────────────────┐
│  mioreel-gateway (Go)                                             │
│                                                                     │
│  api/ ── 鉴权 / 限流 / 参数校验 / 统一响应包络                        │
│   ├── CatalogHandler    GET  /api/v1/catalog                        │
│   ├── SeriesHandler     POST /api/v1/series                         │
│   ├── ResolveHandler    POST /api/v1/resolve      ★ 核心            │
│   ├── SearchHandler     GET  /api/v1/search                         │
│   └── MediaHandler      GET  /api/v1/media/{ticket}  （可选/Web）    │
│                                                                     │
│  service/                                                          │
│   └── ResolveService ──► resolver ──► signer ──► upstream          │
│                                                                     │
│  resolver/   提流：筛选/排序/三态地址解析/no()/io()/域名收敛          │
│  signer/     签名：6 个头（纯函数，随机源可注入）                     │
│  upstream/   HTTP：手动重定向 / 域名白名单 / 超时 / 响应体上限        │
│  device/     设备会话：device_register + 持久化（iid/device_id/cdid）│
│  profile/    签名材料：内置兜底 + 外部下发热更新                     │
│  cache/      resolve 结果缓存（TTL = expires_at - 30s）             │
└────────────────────────────────┬────────────────────────────────────┘
                                 │ HTTPS
                                 ▼
   api5-normal-sinfonlineb.fqnovel.com / api5-normal.fqnovel.com
   reading.snssdk.com / log.snssdk.com
```

---

## 2. 组件职责

| 组件 | 职责 | 不做什么 |
|---|---|---|
| `api/` | HTTP 契约、鉴权、限流、校验、响应包络 | 不含业务逻辑 |
| `service/ResolveService` | 编排：取设备 → 取模型 → 提流 → 探活 → 选流 | 不直接碰字节 |
| `resolver/` | 从 `video_model` 提取候选流、解码地址、算密钥 | 不发请求 |
| `signer/` | 产出 6 个签名头 | 不管业务 |
| `upstream/` | 发请求、管重定向/白名单/超时 | 不解析业务体 |
| `device/` | 设备三元组生命周期 | — |
| `profile/` | 签名材料管理（内置 + 热更新） | — |
| `cache/` | resolve 结果缓存 | — |

**分层原则**：`signer` 与 `resolver` 必须是**纯函数/无网络**的，便于用黄金样本做单元测试（见 `03-algorithm-spec.md` §15）。

---

## 3. 关键数据流

### 3.1 resolve 主链路

```
POST /api/v1/resolve { series_id, episode_no }
  │
  ├─1. 参数校验 ───────────────────────────→ 40001
  │
  ├─2. cache.Get(series_id, vid, quality) ──→ 命中则直接返回
  │
  ├─3. device.Get()  ─── 无设备/过期 ──────→ POST log.snssdk.com/service/2/device_register
  │                                          → 持久化 iid/device_id/cdid
  │
  ├─4. signer.Sign(url, body, epoch, device) → 6 个头
  │
  ├─5. upstream.Post(video_model 路由, body, headers)
  │      └─ 路由降级：4 条路由轮换，可恢复错误才换
  │
  ├─6. resolver.Extract(model, vid, quality) → []variant
  │      ├─ 编码/清晰度筛选与排序
  │      ├─ 三态地址解析（key_seed / https / base64）
  │      ├─ no() 提取 AES-128 内容密钥
  │      └─ lt() 域名收敛
  │
  ├─7. 探活 + 选流（HEAD，必须 200/206 且 content-type: video/*）
  │
  ├─8. 组装响应 { play_url, play_headers, decryption_key, variants, expires_at }
  │
  └─9. cache.Set(TTL = expires_at - now - 30s)
```

### 3.2 为什么网关不代理视频流

实测确认（见 `README.md` §2）：

- 直链**不绑定 UA**（不带 UA 也 200）
- CDN **开放 CORS**（`access-control-allow-origin: *`）
- 直链是 **bearer token**，5 分钟后自然失效

因此客户端可直接从 CDN 取流，网关带宽成本≈0，且网关不成为播放链路的瓶颈/单点。

---

## 4. 部署拓扑

### 4.1 单机（够用）

```
                ┌─────────────────────────┐
  客户端 ──────►│  gateway (Go, :18662)   │
                │  · /api/*               │
                │  · 内置 profile 兜底     │
                └──────────┬──────────────┘
                           │
                    上游 API（出网）
```

- 状态：设备三元组 + resolve 缓存（可落盘，也可只放内存）
- 建议：2 副本起（直链解析是 CPU 密集，签名引擎不便宜）

### 4.2 多副本

必须外置的共享状态：

| 状态 | 是否必须共享 | 说明 |
|---|---|---|
| 设备三元组 | **是** | `device_id` 参与签名，各副本应使用同一套，否则上游可能风控。建议用 Redis/DB 统一存放 |
| resolve 缓存 | 否 | 纯性能优化，各副本独立即可 |
| profile（签名材料） | **是** | 必须全副本一致 → 走配置中心 |
| `counter`（签名自增计数） | 否 | 参考实现是进程内自增；每副本独立即可 |

### 4.3 反向代理示例（Nginx）

```nginx
location /api/ {
    proxy_pass http://127.0.0.1:18662;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Real-IP $remote_addr;
}
# 客户端直连 CDN，网关不参与媒体流量 —— 无需为 /media 配大带宽
```

---

## 5. 签名材料热更新

**唯一高频失效项**是签名材料（`profile.id` / `profile.material`）。算法稳定，材料会轮换。

### 5.1 加载顺序

```
1. 内置默认值（编译进二进制，作为最后兜底，永不失效）
2. 外部配置源（按优先级）
     a. 环境变量 MIOREEL_PROFILE_ID / MIOREEL_PROFILE_MATERIAL
     b. 配置中心 / KV（HTTP 拉取，支持定时刷新）
     c. 本地配置文件
3. 启动时加载 + 后台定时刷新（默认 10 分钟）
4. 刷新失败沿用旧值 + 告警，**不阻断服务**
```

### 5.2 校验

加载时必须校验：

- `material` base64 解码后长度 == 32
- `id` base64 解码后长度 ∈ [1, 128]
- `sdkVersion` == `v04.10.03-ml-android`（否则签名一定失败）

校验不过 → 保留上一份可用配置。

### 5.3 暴露给运维

```http
GET /api/v1/health
→ { "signing": { "profile_id": "646637794", "rotated_at": "2026-11-16T03:12:00Z" } }
```

### 5.4 失效表现与处置

材料失效时，上游会返回业务错误码（不是网络错误），表现为 `resolve` 大面积 `40901` / `50301`。
处置：更新配置 → 触发刷新 → 观察 `/health` 的 `signing.rotated_at`。

---

## 6. 缓存

### 6.1 resolve 结果缓存

| 项 | 值 |
|---|---|
| Key | `sha256(source ‖ vid ‖ quality ‖ prefer_codec)` |
| TTL | `expires_at - now - 30s`（约 4.5 分钟） |
| Value | 完整 `data` 结构 |
| 命中 | 直接返回，**不产生上游请求** |

> 提前 30 秒是为了避免客户端拿到"刚过期"的链接。TTL 必须 ≤ `expires_at - now`。

### 6.2 上游模型缓存（服务端内部）

`video_model` 响应按 `(iid, device_id, cdid, vid)` 缓存，最多 8 条 LRU，TTL ≤ 5 分钟。
这能显著降低上游压力（同一集多次解析只打一次上游）。

### 6.3 设备缓存

`device.Get()` 内部维护；进程内 + 持久化。参考实现还会校验 `iid`/`device_id` 是 10–24 位数字，不合法则重新注册。

---

## 7. 限流与配额

| 维度 | 默认 | 响应 |
|---|---|---|
| 单 Token QPS | 20 | `42901` + `Retry-After` |
| 单 Token 并发 | 8 | `42901` + `Retry-After` |
| 单 IP QPS | 50 | `42901` + `Retry-After` |
| 单次 resolve 超时 | 15s（客户端可传 `timeout_ms` 覆盖） | `50401` |

---

## 8. 可观测性

### 8.1 日志字段

每次 resolve 打一条结构化日志：

```json
{
  "ts": "2026-11-16T05:35:28.211Z",
  "request_id": "01HQ8Z...",
  "level": "info",
  "event": "resolve",
  "source": "hongguo",
  "series_id": "7701678491724418073",
  "episode_no": 1,
  "upstream_route": "api5-normal-sinfonlineb.fqnovel.com",
  "upstream_ms": 812,
  "variants": 2,
  "selected_quality": 1080,
  "selected_codec": "hevc",
  "cached": false,
  "elapsed_ms": 1043,
  "error": ""
}
```

### 8.2 指标

`GET /metrics`（Prometheus 文本格式，**无鉴权** —— 请在反代层限制来源 IP）。

| 指标 | 类型 | 标签 | 用途 |
|---|---|---|---|
| `resolve_total` | counter | `code` | 解析请求总数，按网关错误码分组 → 算成功率 |
| `resolve_duration_ms` | histogram | — | 解析耗时；桶边界 50/100/250/500/1000/2500/5000/10000/15000/30000 ms → 算 P50/P95/P99 |
| `upstream_request_total` | counter | `route`, `code` | 上游各路由健康度 |
| `upstream_fallback_total` | counter | — | 上游路由降级次数 |
| `profile_rotated_timestamp` | gauge | — | 签名材料最近轮换时间（Unix 秒） |
| `cache_hit_ratio` | gauge | — | resolve 缓存命中率（0~1） |

抓取配置与告警建议见 [`10-deployment.md`](10-deployment.md) §6。

### 8.3 请求追踪

所有响应带 `X-Request-Id`（24 hex，网关生成）；客户端可通过同名请求头指定，网关原样回显，用于把客户端日志与服务端日志对齐。

### 8.4 敏感信息

**绝不写入日志**：`play_url`、`decryption_key`、`x-gorgon`/`x-medusa` 等签名头、`Authorization`。

参考实现里也有一条脱敏正则（`authorization|cookie|set-cookie|decryption_key|contentKey|access_token|refresh_token|client_secret`），说明这是行业共识做法。网关应统一做字段级脱敏。

---

## 9. 安全清单

| 风险 | 措施 |
|---|---|
| 直链泄漏 = 视频泄漏 | 强制鉴权；可选 IP 白名单 / mTLS；不做匿名访问 |
| 网关被当作免费代理 | 不提供任意 URL 代理；`/media` 端点（若启用）只允许服务端签发的 ticket |
| 签名材料泄漏 | 材料只下发服务端，不下发客户端；日志脱敏 |
| 上游返回恶意 URL | `lt()` 域名白名单 + https 强制 + 端口限制 |
| 解密密钥泄漏 | 不入日志；不进客户端持久化（除非明确需要） |
| 客户端 Referer 错误 | 网关返回的 `play_headers` **不含** `Referer`，客户端不得自行添加 |

---

## 10. 扩展点

网关按"多源"设计，首版只实现 `hongguo`：

```
/api/v1/resolve  { "source": "hongguo", ... }
```

新增源时：

1. 实现 `resolver` 接口（输入剧集标识，输出 `[]Variant`）
2. 实现对应的 `signer`（若源的签名体系不同）
3. 在 `service` 层注册 `source → resolver` 路由表
4. `source` 字段透传给客户端，客户端无需改动

---

## 11. 相关文档

| 文档 | 内容 |
|---|---|
| [`02-client-api.md`](02-client-api.md) | 客户端契约：字段表、时序、缓存、鉴权 |
| [`03-algorithm-spec.md`](03-algorithm-spec.md) | 签名与解密算法规范（服务端实现依据） |
| [`04-client-integration.md`](04-client-integration.md) | mpv / ffmpeg / VLC / Web / 移动端 接入 |
| [`05-error-codes.md`](05-error-codes.md) | 错误码全表 + 排障决策树 |
| [`06-wire-samples.md`](06-wire-samples.md) | 真实报文样例 |
| [`07-source-browsing.md`](07-source-browsing.md) | 获取视频源：分类 / 搜索 / 分集 / 弹幕 + 上游映射 |
| [`08-configuration.md`](08-configuration.md) | 配置项全表与热更新 |
| [`09-local-development.md`](09-local-development.md) | 本地开发、黄金向量工作流 |
| [`10-deployment.md`](10-deployment.md) | 部署、监控告警、故障处置 |
