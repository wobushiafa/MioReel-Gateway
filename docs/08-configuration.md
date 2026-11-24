# 配置参考

> 实现位置：`server/internal/config/config.go`
> 模板：`configs/gateway.example.json`

---

## 1. 加载顺序

```
命令行 -config <path>
        ↓ 未提供
环境变量 MIOREEL_CONFIG_FILE
        ↓ 未提供
内置默认值（config.Default()）
        ↓ 若指定了配置文件：JSON 覆盖默认值
        ↓ 环境变量 MIOREEL_* 覆盖 JSON（逐项）
最终配置
```

**环境变量优先级最高。** 容器部署时通常用环境变量注入敏感项（token / secret），配置文件只放非敏感项。

---

## 2. 配置项全表

### 2.1 监听与版本

| JSON key | 环境变量 | 类型 | 默认 | 说明 |
|---|---|---|---|---|
| `listen_addr` | `MIOREEL_LISTEN_ADDR` | string | `:18662` | 监听地址。`127.0.0.1:18662` 只允许本机访问 |
| `version` | — | string | `1.0.0` | 版本号，仅用于 `/api/v1/health` 回显 |

### 2.2 鉴权与安全

| JSON key | 环境变量 | 类型 | 默认 | 说明 |
|---|---|---|---|---|
| `auth_tokens` | `MIOREEL_AUTH_TOKENS` | string[]（逗号分隔） | `[]` | 可接受的 Token 列表。**为空时服务拒绝启动**（除非 `allow_insecure_no_auth=true`） |
| `allow_insecure_no_auth` | `MIOREEL_ALLOW_INSECURE_NO_AUTH` | bool | `false` | 仅供本地开发。**严禁在公网实例开启** |
| `hmac_secret` | `MIOREEL_HMAC_SECRET` | string | `""` | 非空时**强制**要求请求带 HMAC 签名头 |
| `hmac_max_skew_seconds` | — | int64 | `300` | 签名时间戳允许的时钟偏差；同时决定 nonce 重放窗口（2×该值） |
| `ip_allowlist` | `MIOREEL_IP_ALLOWLIST` | string[]（逗号分隔） | `[]` | 支持单 IP 与 CIDR（如 `10.0.0.0/8`）。**为空表示不限制** |
| `trust_proxy_headers` | `MIOREEL_TRUST_PROXY_HEADERS` | bool | `false` | 是否信任 `X-Forwarded-For` / `X-Real-IP`。**仅在网关前面确有反向代理时开启**，否则客户端可伪造 IP 绕过白名单与限流 |

> **Token 比较是常量时间**（`subtle.ConstantTimeCompare`），且内存中只保留 Token 的 SHA-256 前 8 字节作为标识（用于限流桶 key），不保留原文。

### 2.3 限流

| JSON key | 环境变量 | 类型 | 默认 | 说明 |
|---|---|---|---|---|
| `token_qps` | `MIOREEL_TOKEN_QPS` | float | `20` | 单 Token 每秒请求数（令牌桶，桶容量 = QPS） |
| `token_concurrency` | `MIOREEL_TOKEN_CONCURRENCY` | int | `8` | 单 Token 最大并发在途请求数 |
| `ip_qps` | `MIOREEL_IP_QPS` | float | `50` | 单 IP 每秒请求数 |

触发限流返回 `42901`，并带 `Retry-After: 2`。

### 2.4 签名材料（热更新）

| JSON key | 环境变量 | 类型 | 默认 | 说明 |
|---|---|---|---|---|
| `profile_id` | `MIOREEL_PROFILE_ID` | string | `""` | 签名材料 id（base64）。**最高优先级** |
| `profile_material` | `MIOREEL_PROFILE_MATERIAL` | string | `""` | 签名材料密钥（base64，32 字节）。**最高优先级** |
| `profile_sdk_version` | `MIOREEL_PROFILE_SDK_VERSION` | string | `v04.10.03-ml-android` | **只支持这一个值**，其他值会导致签名直接失败 |
| `profile_file` | `MIOREEL_PROFILE_FILE` | string | `""` | 本地文件路径，文件内含 id / material |
| `profile_url` | `MIOREEL_PROFILE_URL` | string | `""` | 远程地址，用于拉取最新材料 |
| `profile_refresh_interval` | `MIOREEL_PROFILE_REFRESH_INTERVAL` | duration | `10m` | 后台刷新间隔。**仅当配置了 file 或 url 时生效** |

**加载优先级（`profile/manager.go`）：**

```
环境变量 (profile_id + profile_material)
        ↓ 未配置
远程 URL (profile_url)
        ↓ 未配置
本地文件 (profile_file)
        ↓ 未配置
内置兜底（编译进二进制）
```

### 2.5 内置兜底材料

```json
{
  "profile_id": "NjQ2NjM3Nzk0",
  "profile_material": "sjwqRwog0QXcP4xEOPgnLtRtbIn/3N7dUQhpjSbpSJ0="
}
```

| 项 | 值 | 说明 |
|---|---|---|
| `profile_id` 解 base64 | `646637794` | 9 字节 ASCII |
| `profile_material` 解 base64 | 32 字节 | 签名密钥派生材料 |
| `profile_sdk_version` | `v04.10.03-ml-android` | 唯一支持值 |

内置材料**永久存在**，保证「配置服务挂了 → 网关仍能用上一份材料工作」。代价是：上游轮换材料后，必须更新配置才能恢复。

### 2.6 设备与持久化

| JSON key | 环境变量 | 类型 | 默认 | 说明 |
|---|---|---|---|---|
| `device_store_path` | `MIOREEL_DEVICE_STORE_PATH` | string | `data/device.json` | 设备三元组（`iid`/`device_id`/`cdid`）的持久化文件 |

> 设备三元组**参与签名**，多副本部署时必须共享同一份（见 [`10-deployment.md`](10-deployment.md) §4）。

### 2.7 超时

| JSON key | 环境变量 | 类型 | 默认 | 说明 |
|---|---|---|---|---|
| `default_resolve_timeout_ms` | — | int | `15000` | `resolve` 默认超时。客户端可用请求体 `timeout_ms` 覆盖（下限 1000ms） |

### 2.8 媒体代理（可选，仅 Web 客户端）

| JSON key | 环境变量 | 类型 | 默认 | 说明 |
|---|---|---|---|---|
| `media_proxy_enabled` | `MIOREEL_MEDIA_PROXY_ENABLED` | bool | `false` | 是否启用 `GET /api/v1/media/{ticket}` 预解密代理 |
| `media_ticket_secret` | `MIOREEL_MEDIA_TICKET_SECRET` | string | 随机 32 字节 | 票据 HMAC 密钥。为空则进程启动时随机生成（**重启后旧票据全部失效**） |
| `media_ffmpeg_path` | `MIOREEL_MEDIA_FFMPEG_PATH` | string | `ffmpeg` | ffmpeg 可执行文件路径（需在 PATH 中或给绝对路径） |
| `media_cache_dir` | `MIOREEL_MEDIA_CACHE_DIR` | string | `data/media_cache` | 已解密 MP4 的落盘缓存目录 |

> **只有 Web / 浏览器客户端需要它** —— 桌面与移动端直接用 `resolve` 返回的直链 + 密钥即可，不需要网关承担带宽。

---

## 3. 配置文件示例

```json
{
  "listen_addr": ":18662",
  "version": "1.0.0",

  "auth_tokens": ["sk_live_xxxxxxxxxxxx", "sk_ops_yyyyyyyyyyyy"],
  "allow_insecure_no_auth": false,
  "hmac_secret": "",
  "hmac_max_skew_seconds": 300,
  "ip_allowlist": ["10.0.0.0/8", "203.0.113.7"],
  "trust_proxy_headers": false,

  "token_qps": 20,
  "token_concurrency": 8,
  "ip_qps": 50,

  "profile_sdk_version": "v04.10.03-ml-android",
  "profile_file": "",
  "profile_url": "",
  "profile_refresh_interval": "10m",

  "device_store_path": "data/device.json",
  "default_resolve_timeout_ms": 15000,

  "media_proxy_enabled": false,
  "media_ticket_secret": "",
  "media_ffmpeg_path": "ffmpeg",
  "media_cache_dir": "data/media_cache"
}
```

启动：

```bash
./bin/media-gateway --config configs/gateway.json
# 或
MIOREEL_CONFIG_FILE=configs/gateway.json ./bin/media-gateway
```

---

## 4. 启动期校验

`cmd/gateway/main.go` 有两条硬性检查：

| 条件 | 行为 |
|---|---|
| `auth_tokens` 为空 **且** `allow_insecure_no_auth=false` | 打印错误到 stderr，**退出码 2**，不启动 |
| `auth_tokens` 为空 **且** `allow_insecure_no_auth=true` | 打印 WARN 日志后启动（`running with MIOREEL_ALLOW_INSECURE_NO_AUTH=true; do not expose this instance publicly`） |

配置文件解析失败（不存在 / JSON 语法错）→ 打印错误，退出码 1。

> 这保证了「忘记配 Token」不会变成一个匿名可用的公网网关。

---

## 5. 材料热更新

### 5.1 行为

- 只有配置了 `profile_file` 或 `profile_url` 时才启动后台刷新器（`profile_refresh_interval`，默认 10 分钟）。
- 刷新**失败不会中断服务**：沿用上一份可用材料，并把状态标记为 `degraded`。
- 刷新成功且材料发生变化 → 更新 `profile_rotated_timestamp` 指标。

### 5.2 远程文件格式

`profile_url` 指向的 JSON：

```json
{
  "profile_id": "NjQ2NjM3Nzk0",
  "profile_material": "sjwqRwog0QXcP4xEOPgnLtRtbIn/3N7dUQhpjSbpSJ0=",
  "profile_sdk_version": "v04.10.03-ml-android"
}
```

本地文件（`profile_file`）格式相同。

### 5.3 校验规则

加载时校验，任一不满足则**拒绝该份材料**并保留旧材料：

| 规则 | 失败码 |
|---|---|
| `profile_material` base64 解码后长度 == 32 字节 | `SIGNING_CONFIGURATION_INVALID` |
| `profile_id` base64 解码后长度 ∈ [1, 128] | `SIGNING_CONFIGURATION_INVALID` |
| `profile_sdk_version` == `v04.10.03-ml-android` | `LOCAL_PROFILE_UNSUPPORTED` |

### 5.4 验证材料是否生效

```bash
curl -s http://127.0.0.1:18662/api/v1/health | jq '.signing'
# {
#   "profile_id": "646637794",
#   "sdk_version": "v04.10.03-ml-android",
#   "rotated_at": "2026-11-16T03:12:00Z"
# }
```

`rotated_at` 变化即表示新材料已生效。

---

## 6. 配置项速查（按场景）

| 场景 | 必改项 |
|---|---|
| 本地开发 | `allow_insecure_no_auth=true`（或给个 `auth_tokens`） |
| 公网部署 | `auth_tokens` + `ip_allowlist`；`trust_proxy_headers` 仅在有反代时开 |
| 高安全客户端 | `hmac_secret` + `allow_insecure_no_auth=false` |
| 多副本 | 共享 `device_store_path`（同一文件或同一份内容） |
| Web 客户端 | `media_proxy_enabled=true` + 固定 `media_ticket_secret` + 安装 ffmpeg |
| 上游材料轮换 | `profile_url` 指向可热更新的 JSON |

---

## 7. 相关文档

- 接口契约 → [`02-client-api.md`](02-client-api.md)
- 找片接口 → [`07-source-browsing.md`](07-source-browsing.md)
- 本地开发 → [`09-local-development.md`](09-local-development.md)
- 部署 → [`10-deployment.md`](10-deployment.md)
