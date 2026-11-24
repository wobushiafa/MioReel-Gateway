# 部署与运维

---

## 1. 构建产物

```bash
make build
# → bin/media-gateway   （go build -trimpath -o ../bin/media-gateway ./cmd/gateway）
```

跨平台编译：

```bash
cd server
GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o ../dist/media-gateway-linux-amd64 ./cmd/gateway
GOOS=linux   GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o ../dist/media-gateway-linux-arm64 ./cmd/gateway
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o ../dist/media-gateway-windows-amd64.exe ./cmd/gateway
GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o ../dist/media-gateway-darwin-arm64 ./cmd/gateway
```

**静态二进制，无 CGO 依赖**，可直接扔进 scratch 镜像。

### 1.1 运行时目录

```
data/
├─ device.json         设备三元组（iid/device_id/cdid）—— 多副本必须共享
└─ media_cache/       已解密 MP4 缓存（仅启用媒体代理时产生）
```

---

## 2. systemd

```ini
# /etc/systemd/system/media-gateway.service
[Unit]
Description=mioreel-gateway
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=media-gw
WorkingDirectory=/opt/media-gateway
ExecStart=/opt/media-gateway/bin/media-gateway --config /etc/media-gateway/config.json
Restart=always
RestartSec=3
# 优雅退出：进程收到 SIGTERM 后最多 10s 完成在途请求
KillSignal=SIGTERM
TimeoutStopSec=15

Environment=MIOREEL_AUTH_TOKENS=sk_live_xxx,sk_ops_yyy
Environment=MIOREEL_PROFILE_URL=https://internal.example.com/media-gateway/profile.json
Environment=MIOREEL_PROFILE_REFRESH_INTERVAL=10m

NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ReadWritePaths=/opt/media-gateway/data
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now media-gateway
sudo journalctl -u media-gateway -f
```

---

## 3. 容器

### 3.1 Dockerfile

```dockerfile
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY server/ ./server/
RUN cd server && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/media-gateway ./cmd/gateway

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata ffmpeg && \
    mkdir -p /app/data && adduser -D -u 10001 gw
WORKDIR /app
COPY --from=build /out/media-gateway ./media-gateway
USER gw
EXPOSE 18662
VOLUME ["/app/data"]
ENTRYPOINT ["./media-gateway"]
```

> `ffmpeg` 只在**启用媒体代理**时才需要；纯桌面/移动客户端场景可以去掉以减小镜像。

### 3.2 docker-compose

```yaml
services:
  gateway:
    build: .
    ports: ["18662:18662"]
    environment:
      MIOREEL_AUTH_TOKENS: "sk_live_xxx"
      MIOREEL_PROFILE_URL: "https://internal.example.com/media-gateway/profile.json"
      MIOREEL_DEVICE_STORE_PATH: "/app/data/device.json"
    volumes:
      - gateway-data:/app/data
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://127.0.0.1:18662/api/v1/health"]
      interval: 30s
      timeout: 5s
      retries: 3

volumes:
  gateway-data:
```

---

## 4. 多副本部署

### 4.1 必须共享的状态

| 状态 | 是否必须共享 | 原因 |
|---|---|---|
| **设备三元组**（`device.json`） | **是** | `device_id` 参与签名（`packet()` 的 proto field5）。各副本用不同设备会让上游看到"一个用户多设备"，可能触发风控 |
| **签名材料** | **是** | 必须全副本一致 → 走 `profile_url` 指向同一份配置 |
| resolve 缓存 | 否 | 纯性能优化，各副本独立 |
| catalog/search 游标链 | 否 | 游标是客户端无状态的翻页凭据，副本间不需要一致（但**单个客户端的翻页请求应打到同一副本**，否则会 40001） |
| `counter`（签名自增计数） | 否 | 进程内自增 |

### 4.2 会话亲和性

**catalog 与 search 的翻页依赖服务端游标链**，所以：

- 客户端翻页必须命中同一副本 → 开启会话亲和（NGINX `ip_hash` / Traefik sticky session）；
- 或者把游标链抽到 Redis 共享存储。

```nginx
upstream media_gateway {
    server 10.0.0.11:18662;
    server 10.0.0.12:18662;
    ip_hash;          # 保证同一客户端命中同一副本
}
```

### 4.3 共享 device.json

最简单：NFS / 共享卷 + 只读打开的语义要小心。更稳妥的做法是**用一个专门的"设备注册服务"**，或者把 `device.json` 放到 Redis 并让 `device.Manager` 支持从 Redis 读（当前实现只支持文件）。

> 当前实现：`device_store_path` 是**单文件**。多副本请先解决这一项，否则签名材料与设备一致性都无法保证。

---

## 5. 反向代理

```nginx
upstream media_gateway {
    server 127.0.0.1:18662;
    keepalive 32;
}

server {
    listen 443 ssl http2;
    server_name gw.example.com;

    ssl_certificate     /etc/letsencrypt/live/gw.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/gw.example.com/privkey.pem;

    # 直链解析是 CPU 密集（签名 + 解密），给足超时
    proxy_read_timeout 120s;

    location /api/ {
        proxy_pass http://media_gateway;
        proxy_http_version 1.1;
        proxy_set_header Connection "";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    location /metrics {
        proxy_pass http://media_gateway;
        # /metrics 无鉴权 —— 务必限制来源 IP
        allow 10.0.0.0/8;
        deny all;
    }
}
```

> ⚠️ 用了反代后，**必须**在网关配置里设 `trust_proxy_headers=true`，否则网关拿到的是反代 IP（`127.0.0.1`），IP 白名单与限流会全部失效。

### 5.1 客户端不经过网关的流量

**视频流不经过网关**，所以 Nginx 也不需要为它配置任何带宽。唯一例外是启用媒体代理的 Web 客户端：

```nginx
location /api/v1/media/ {
    proxy_pass http://media_gateway;
    proxy_set_header Range $http_range;
    proxy_set_header If-Range $http_if_range;
    proxy_buffering off;          # 边转码边下发，不要整体缓冲
    proxy_read_timeout 300s;
}
```

---

## 6. 监控

### 6.1 指标端点

`GET /metrics` —— Prometheus 文本格式，**无鉴权**（务必在反代层限制来源 IP）。

| 指标 | 类型 | 标签 | 含义 |
|---|---|---|---|
| `resolve_total` | counter | `code` | 解析请求总数，按网关错误码分组 |
| `resolve_duration_ms` | histogram | — | 解析耗时直方图，桶边界 50/100/250/500/1000/2500/5000/10000/15000/30000 ms |
| `upstream_request_total` | counter | `route`, `code` | 上游请求数，按路由与上游状态码 |
| `upstream_fallback_total` | counter | — | 上游路由降级次数 |
| `profile_rotated_timestamp` | gauge | — | 签名材料最近轮换时间（Unix 秒） |
| `cache_hit_ratio` | gauge | — | resolve 缓存命中率（0~1） |

抓取配置：

```yaml
scrape_configs:
  - job_name: media-gateway
    static_configs:
      - targets: ["gw.example.com:18662"]
    metrics_path: /metrics
```

### 6.2 告警建议

| 告警 | 条件 | 含义 |
|---|---|---|
| 网关不可用 | `up == 0` 持续 2 min | 进程挂了或反代异常 |
| 解析成功率下降 | `rate(resolve_total{code="0"}[5m]) / rate(resolve_total[5m]) < 0.9` | 上游异常或材料失效 |
| 上游大量降级 | `rate(upstream_fallback_total[5m]) > 0.5` | 首选路由挂了 |
| 连续失败 | `consecutive_failures >= 3`（见 `/health`） | 状态转 `degraded` |
| 材料疑似过期 | `time() - profile_rotated_timestamp > 30*24*3600` | 长期未轮换，上游可能已换算法 |
| P99 延迟劣化 | `histogram_quantile(0.99, rate(resolve_duration_ms_bucket[5m])) > 5000` | 上游慢或超时配置过紧 |

### 6.3 健康检查

```bash
curl -s http://127.0.0.1:18662/api/v1/health
```

```json
{
  "status": "ok",
  "version": "1.0.0",
  "uptime_ms": 4821300,
  "signing": { "profile_id": "646637794", "sdk_version": "v04.10.03-ml-android", "rotated_at": "2026-11-16T03:12:00Z" },
  "upstream": { "device_ready": true, "last_success_at": "2026-11-16T05:35:28Z", "consecutive_failures": 0 }
}
```

`status` 判定（`internal/api/handlers.go`）：

| 条件 | status |
|---|---|
| 正常 | `ok` |
| 材料降级 **或** 连续失败 ≥ 3 | `degraded` |
| 连续失败 ≥ 10 **且** 设备未就绪 | `down` |

> `/api/v1/health` **不需要鉴权**（便于探针），但它会暴露 `profile_id` —— 若不希望公开，请限制反代来源 IP。

---

## 7. 日志

结构化 JSON（`slog`），输出到 stdout。

| 字段 | 说明 |
|---|---|
| `request_id` | 请求追踪 ID（24 hex）。客户端也可通过 `X-Request-Id` 请求头指定 |
| `event` | 事件名（`resolve` 等） |
| `source` / `series_id` / `episode_no` | 业务维度 |
| `upstream_route` | 命中的上游路由 |
| `upstream_ms` | 上游耗时 |
| `variants` | 候选流数量 |
| `selected_quality` / `selected_codec` | 选中的流属性 |
| `cached` | 是否命中缓存 |
| `elapsed_ms` | 总耗时 |
| `error` | 错误码 |

**不会记录**：`play_url`、`decryption_key`、签名头、Token。

客户端可用 `X-Request-Id` 头自带追踪 ID：

```http
X-Request-Id: client-trace-20261116-001
```

网关会原样回显在响应头 `X-Request-Id` 中。

---

## 8. 故障处置手册

| 现象 | 可能原因 | 处置 |
|---|---|---|
| 全量 `40901` | 签名材料失效 | 查 `/health` 的 `rotated_at`；更新 `profile_url` 指向的材料；等待 ≤10 分钟刷新，或重启立即生效 |
| 全量 `50201` | 出网被墙 / DNS 故障 | 在服务器上 `curl -I https://reading.snssdk.com`；检查出口与 DNS |
| 全量 `50301` | 上游全部路由失败 | 查 `upstream_request_total{route=...}`，逐路由看 code；`upstream_fallback_total` 上升说明首选路由挂了 |
| `50401` 增多 | 上游变慢 | 调大 `default_resolve_timeout_ms`；或减小 `timeout_ms` 让失败更快 |
| `42901` 增多 | 客户端并发过高 | 调 `token_qps` / `token_concurrency`；确认客户端没有重试风暴 |
| `40101` 增多 | Token 轮换了但客户端没更新 | 确认 `auth_tokens` 配置与客户端一致 |
| 媒体代理 502 | ffmpeg 不在 PATH 或解密失败 | 检查 `media_ffmpeg_path`；看 `media_cache_dir` 是否有残留 `.tmp.mp4` |
| 磁盘涨 | `data/media_cache` 累积 | 媒体代理目前**不自动清理缓存**，需加 cron 定期删 |

---

## 9. 升级流程

```bash
# 1. 拉代码并跑全量校验
git pull
make vet && make test-race

# 2. 构建
make build

# 3. 灰度（先起新实例，用少量流量验证）
MIOREEL_AUTH_TOKENS=... ./bin/media-gateway --config configs/gateway.json &
curl -s http://127.0.0.1:8081/api/v1/health

# 4. 切流
sudo systemctl restart media-gateway
```

**回滚**：保留上一个 `bin/media-gateway` 二进制，直接替换重启即可。网关**无数据库迁移**（只有 `device.json` 一个文件，格式向后兼容）。

> ⚠️ 升级前确认 `device.json` 存在且可读 —— 丢失它会重新注册访客设备（无害，但会短暂多打一次上游注册）。

---

## 10. 安全清单

- [ ] `auth_tokens` 已配置，且不是示例值
- [ ] `allow_insecure_no_auth=false`
- [ ] `ip_allowlist` 已设置（若客户端 IP 段固定）
- [ ] `trust_proxy_headers` 只在确有反代时开启
- [ ] `/metrics` 在反代层限制来源 IP
- [ ] `hmac_secret` 在高安全场景已启用
- [ ] `media_ticket_secret` 已固定（否则重启后票据全失效）
- [ ] 日志中确认没有 `play_url` / `decryption_key`
- [ ] `data/` 目录权限受限（设备三元组 + 解密缓存）

---

## 11. 相关文档

- 接口契约 → [`02-client-api.md`](02-client-api.md)
- 架构 → [`01-architecture.md`](01-architecture.md)
- 配置 → [`08-configuration.md`](08-configuration.md)
- 本地开发 → [`09-local-development.md`](09-local-development.md)
- 错误码 → [`05-error-codes.md`](05-error-codes.md)
