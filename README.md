# MioReel-Gateway

[MioReel](https://github.com/wobushiafa/MioReel) 客户端的配套**短剧播放直链解析网关**。Go 服务端负责签名、解析与解密，客户端只做两件事：

1. **找片** —— 调网关拿分类 / 榜单 / 推荐 / 搜索 / 分集 / 弹幕
2. **播放** —— 调网关拿播放直链 + AES 密钥，交给播放器

> 客户端**不需要实现任何密码学**。签名（`x-gorgon` / `x-medusa` / `x-helios` …）、地址解码、AES 密钥提取全部在服务端完成。
>
> 配套客户端仓库：**[wobushiafa/MioReel](https://github.com/wobushiafa/MioReel)**

---

## 一、快速开始

```bash
make build                                   # → bin/media-gateway
cp configs/gateway.example.json configs/gateway.json
export MIOREEL_AUTH_TOKENS="dev-token"
./bin/media-gateway --config configs/gateway.json
```

默认监听 `:18662`（可在 `configs/gateway.json` 中修改 `listen_addr`）。启动后检查健康状态：

```bash
curl -s http://127.0.0.1:18662/api/v1/health | jq .
```

完整开发流程见 [`docs/09-local-development.md`](docs/09-local-development.md)。

---

## 二、客户端怎么拿到播放直链（3 步）

```
① GET  /api/v1/catalog?category=real-drama&page=1
        → 剧目列表：series_id / title / cover / episode_count

② POST /api/v1/series      { "series_id": "7701678491724418073" }
        → 分集表：episodes[] { no, vid, title }

③ POST /api/v1/resolve     { "series_id": "7701678491724418073", "episode_no": 1 }
        → 可播放直链：
          {
            "play_url":       "https://v5-reading-video.qznovelvod.com/.../xxx",
            "play_headers":   { "User-Agent": "com.phoenix.read/71332 ..." },
            "decryption_key": "982735c47051044342ede4ef5d3e8cb5",
            "expires_at":     1794808528211
          }
```

拿到之后交给播放器（**解密密钥必须一起传**）：

```bash
# ffmpeg —— 已实测：带密钥 exit=0 无错，不带则 NAL 全部损坏
ffmpeg -decryption_key <decryption_key> -user_agent "<UA>" -i "<play_url>" out.mp4

# mpv
mpv --demuxer-lavf-o=decryption_key=<decryption_key> \
    --http-header-fields="User-Agent: <UA>" "<play_url>"
```

---

## 三、接口请求与调用说明

### 3.1 鉴权与通用规范

- **Base URL**：`http://<host>:18662`（生产环境建议通过反代提供 HTTPS）
- **鉴权方式**（除 `/api/v1/health`、`/metrics`、`/api/v1/media/{ticket}` 外均需鉴权）：
  - 方式一（推荐）：`Authorization: Bearer <token>`
  - 方式二：`X-API-Key: <token>`
  - 可选 HMAC 防重放签名（当服务端配置了 `hmac_secret` 时）：额外附带 `X-Timestamp`（Unix 秒）、`X-Nonce`（长度 ≥ 16）、`X-Signature`（`HMAC-SHA256(secret, METHOD + "\n" + PATH + "\n" + timestamp + "\n" + nonce + "\n" + sha256_hex(body))`）。
- **请求追踪**：可自定义 `X-Request-Id` 请求头，响应会原样回显（未传时网关自动生成 24 位 hex）。
- **统一响应包络**：

```json
{
  "code": 0,
  "message": "ok",
  "request_id": "01HQ8Z...",
  "data": {}
}
```

> 业务错误时 HTTP 状态码仍为 `200`，请以 `code === 0` 判断成功（`/api/v1/health`、`/api/v1/media/{ticket}`、`/metrics` 除外）。详细错误码见 [`docs/05-error-codes.md`](docs/05-error-codes.md)。

### 3.2 接口速查表

| 方法 | 路径 | 鉴权 | 说明 |
|---|---|---|---|
| `GET` | `/api/v1/health` | 无 | 健康检查（服务状态、签名材料、上游设备状态） |
| `GET` | `/api/v1/catalog` | Token | 分类剧目列表（支持 `sort=recommend` / `hot`） |
| `GET` | `/api/v1/rankings/boards` | Token | 获取官方榜单列表 |
| `GET` | `/api/v1/rankings` | Token | 获取指定榜单剧目 |
| `GET` | `/api/v1/recommendations` | Token | 按题材/分类获取推荐剧目 |
| `GET` | `/api/v1/feed` | Token | 首页推荐流 |
| `GET` | `/api/v1/search` | Token | 关键词搜索剧目 |
| `POST` | `/api/v1/series` | Token | 获取剧集详情与分集列表（含 `vid`） |
| `POST` | `/api/v1/resolve` | Token | 解析单集播放直链与 AES-128 解密密钥 |
| `GET` | `/api/v1/danmaku` | Token | 按时间段拉取分集弹幕 |
| `GET` | `/api/v1/media/{ticket}` | Ticket | Web 端预解密媒体代理（默认关闭） |
| `GET` | `/metrics` | 无 | Prometheus 监控指标 |

### 3.3 各接口请求示例

以下示例假设网关地址为 `http://127.0.0.1:18662`，Token 为 `dev-token`：

#### 1. 健康检查 `GET /api/v1/health`

```bash
curl -s http://127.0.0.1:18662/api/v1/health
```

#### 2. 分类剧目列表 `GET /api/v1/catalog`

Query 参数：
- `category`：分类，可选 `real-drama`（真人剧，默认）/ `comic-drama`（漫剧）/ `ai-drama`（AI 剧）/ `all`
- `sort`：排序，可选空（默认目录）/ `recommend`（推荐）/ `hot`（热门榜）
- `page`：页码，从 `1` 开始，默认 `1`
- `limit`：每页条数，默认 `40`，上限 `100`

```bash
curl -s "http://127.0.0.1:18662/api/v1/catalog?category=real-drama&page=1&limit=20" \
  -H "Authorization: Bearer dev-token"
```

#### 3. 榜单列表与榜单内容 `GET /api/v1/rankings/boards` & `GET /api/v1/rankings`

```bash
# 获取可用榜单
curl -s "http://127.0.0.1:18662/api/v1/rankings/boards" \
  -H "Authorization: Bearer dev-token"

# 获取指定榜单剧目（board 取自 boards 返回的 id）
curl -s "http://127.0.0.1:18662/api/v1/rankings?board=hongguo-app:all:ranklist_hot_sc&page=1&limit=20" \
  -H "Authorization: Bearer dev-token"
```

#### 4. 分类推荐与首页推荐流 `GET /api/v1/recommendations` & `GET /api/v1/feed`

```bash
# 按题材推荐（genre 参数）
curl -s "http://127.0.0.1:18662/api/v1/recommendations?genre=real-drama&page=1&limit=20" \
  -H "Authorization: Bearer dev-token"

# 首页推荐流（scope 参数）
curl -s "http://127.0.0.1:18662/api/v1/feed?scope=all&page=1&limit=20" \
  -H "Authorization: Bearer dev-token"
```

#### 5. 搜索剧目 `GET /api/v1/search`

Query 参数：`q`（关键词，必填）、`page`（页码，从 `1` 起）。

```bash
curl -s "http://127.0.0.1:18662/api/v1/search?q=%E9%87%8D%E7%94%9F&page=1" \
  -H "Authorization: Bearer dev-token"
```

#### 6. 获取剧集详情与分集列表 `POST /api/v1/series`

JSON 请求体：`{"series_id": "<剧集ID>", "source": "hongguo"}`（`source` 可省略，默认 `hongguo`）。

```bash
curl -s -X POST "http://127.0.0.1:18662/api/v1/series" \
  -H "Authorization: Bearer dev-token" \
  -H "Content-Type: application/json" \
  -d '{"series_id":"7701678491724418073"}'
```

#### 7. 解析播放直链 `POST /api/v1/resolve`

JSON 请求体支持两种定位方式（二选一）：
- `series_id` + `episode_no`（第几集，从 `1` 起）
- `vid`（分集 ID，直接来自 `/api/v1/series` 返回的 `episodes[].vid`）

可选参数：`quality`（目标短边像素，`0` 为最高）、`prefer_codec`（如 `["h264","hevc","mp4"]`）、`timeout_ms`、`avoid_url`（跳过上次失败的直链）。

```bash
curl -s -X POST "http://127.0.0.1:18662/api/v1/resolve" \
  -H "Authorization: Bearer dev-token" \
  -H "Content-Type: application/json" \
  -d '{"series_id":"7701678491724418073","episode_no":1}'
```

#### 8. 获取弹幕 `GET /api/v1/danmaku`

Query 参数：`danmaku_id`（来自 `/resolve` 返回的 `danmaku_id`，形如 `hongguo:{series_id}:{vid}`）、`start_ms`（起始毫秒）、`duration_ms`（片段时长毫秒）。

```bash
curl -s "http://127.0.0.1:18662/api/v1/danmaku?danmaku_id=hongguo:7701678491724418073:7701678491724418074&start_ms=0&duration_ms=60000" \
  -H "Authorization: Bearer dev-token"
```

更多字段细节与接入示例见 [`docs/02-client-api.md`](docs/02-client-api.md)、[`docs/04-client-integration.md`](docs/04-client-integration.md) 与 [`docs/07-source-browsing.md`](docs/07-source-browsing.md)。

---

## 四、已实测确认的关键事实

以下结论均在本机跑通并留有证据（`tools/golden/out/`），**不是推测**：

| 结论 | 证据 |
|---|---|
| 直链是 **bearer token**，约 5 分钟过期 | `expires_at` 实测 ≈ `now + 300s` |
| 直链**不绑定 UA** | 不带 UA / 空 UA / 浏览器 UA，HEAD 均返回 `200 video/mp4` |
| 直链**必须不带错误 Referer** | 带 `Referer: https://example.com/` → **403**；缺省或 `https://novelquickapp.com/` → 200 |
| CDN **开放 CORS** + 支持 Range | `access-control-allow-origin: *`，`206` |
| 片源是 **AES-128 加密 MP4** | 文件内命中 `encv` / `enca` / `senc` / `sinf` / `schm` / `tenc` box |
| **不给密钥就解不出画面** | `ffmpeg` 带 `-decryption_key` → exit 0 无错；不带 → `Invalid NAL unit size` |
| 实际下发编码可能是 **HEVC** | `ffprobe` 实测 `codec_name=hevc, 1920x1080` + `aac` |
| 服务端**不需要代理视频带宽** | 直链任意客户端可直接播（UA 无关、CORS 开放） |

---

## 五、架构

```
┌──────────────┐   HTTP/JSON    ┌───────────────────────────────────────────┐
│  MioReel     │ ─────────────▶ │  MioReel-Gateway (Go)                     │
│  客户端       │                │                                           │
│              │ ◀───────────── │  api/      路由 · 鉴权 · 限流 · 请求 ID     │
│  播放器       │  直链+密钥      │  service/  catalog · discovery · series · │
│  (mpv/ffmpeg) │                │            resolve · search · danmaku ·   │
└──────────────┘                │            media                           │
        │                        │  resolver/ 提流 · 三态地址解析 · no()/io() │
        │                        │  signer/   签名引擎（6 个头）               │
        │                        │  upstream/ 上游 HTTP（域名白名单）          │
        │                        │  device/   设备会话    profile/ 材料热更新  │
        │                        │  cache/    TTL 缓存   observability/ 指标   │
        │  直连 CDN（不过网关）     └──────────────────────┬────────────────────┘
        └──────────────────────────────────────────────────▶│ api5-*.fqnovel.com
                                     v9-reading-video.     │ reading.snssdk.com
                                       qznovelvod.com
```

视频流**永远不经过网关**。网关只回传直链，因此网关带宽成本≈0，也不是播放链路的瓶颈。
（唯一例外是 Web 客户端的预解密代理 `/api/v1/media/{ticket}`，见 [`02-client-api.md`](docs/02-client-api.md) §14。）

---

## 六、文档索引

| # | 文档 | 读者 | 内容 |
|---|---|---|---|
| 01 | [`architecture.md`](docs/01-architecture.md) | 全体 | 组件职责、数据流、部署拓扑、缓存、限流、指标 |
| **02** | **[`client-api.md`](docs/02-client-api.md)** | **客户端** | **API 契约、字段表、时序、鉴权、端点速查** |
| 03 | [`algorithm-spec.md`](docs/03-algorithm-spec.md) | 服务端 | 签名/解密算法逐条规范（含三套摘要分支） |
| 04 | [`client-integration.md`](docs/04-client-integration.md) | 客户端 | mpv / ffmpeg / VLC / Web / 移动端 接入 |
| 05 | [`error-codes.md`](docs/05-error-codes.md) | 全体 | 错误码全表 + 排障决策树 |
| 06 | [`wire-samples.md`](docs/06-wire-samples.md) | 服务端 | 真实报文样例（query / 6 个签名头 / 响应） |
| **07** | **[`source-browsing.md`](docs/07-source-browsing.md)** | **客户端** | **获取视频源：分类/搜索/分集/弹幕 + 上游红果端点映射** |
| 08 | [`configuration.md`](docs/08-configuration.md) | 运维 | 配置项全表、签名材料热更新 |
| 09 | [`local-development.md`](docs/09-local-development.md) | 服务端 | 环境、命令、黄金向量工作流、排障 |
| 10 | [`deployment.md`](docs/10-deployment.md) | 运维 | 构建、部署拓扑、监控告警、故障处置 |

---

## 七、仓库结构

```
MioReel-Gateway/
├─ .github/workflows/ci.yml    # CI：gofmt / go vet / go test -race / go build
├─ .editorconfig  .gitattributes  .gitignore
├─ Makefile                    # build / test / test-race / vet / fmt / tidy / run / golden
├─ README.md
├─ configs/gateway.example.json
├─ data/                       # 运行时持久化（device.json / media_cache，Git 忽略）
├─ deploy/                     # systemd 与部署脚本
├─ docs/                       # 开发文档（10 篇）
├─ scripts/                    # Linux 打包脚本
├─ server/                     # Go 服务端模块
│  ├─ go.mod
│  ├─ types.go                 # ★ 对外公共契约类型
│  ├─ cmd/gateway/main.go      # 进程入口
│  └─ internal/                # api service resolver signer upstream device profile cache observability config
└─ tools/golden/               # 参考实现 + 黄金样本工具
   ├─ reference-hongguo.js     # 上游参考实现原件（只读，勿改）
   ├─ hongguo_host.js          # Node 宿主沙箱（模拟 Dart 注入的 JS 运行时）
   ├─ recorder.js              # 播放链路报文录制器 → out/wire-samples.json
   ├─ recorder_discovery.js    # 发现类链路录制器 → out/discovery-samples.json
   └─ out/                     # 实测产物
```

`tools/golden/` 是**服务端实现的验证基准**：Node 侧跑参考实现产出样本，Go 侧产出同样字节即可判定一致。

```bash
node tools/golden/recorder.js tools/golden/out
node tools/golden/recorder_discovery.js tools/golden/out
```

---

## 八、相关项目

- 客户端：**[MioReel](https://github.com/wobushiafa/MioReel)** —— 与本网关配合使用的客户端应用
