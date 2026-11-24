# 错误码与排障

> 实现位置：`server/internal/service/errors.go`
> 映射逻辑：`MapError()` / `mapCodeString()`

---

## 1. 网关错误码

**业务失败一律返回 HTTP 200**（除鉴权类），客户端必须读 `code` 而不是 HTTP 状态码。

| `code` | HTTP | 含义 | 可重试 | 客户端动作 |
|---|---|---|---|---|
| `0` | 200 | 成功 | — | — |
| `40001` | 200 | 参数非法 / 游标过期 / 跳页 | 否 | 修参数；翻页从第 1 页重来 |
| `40101` | **401** | 未认证、Token 无效、签名缺失/过期/重放 | 否 | 重新获取 Token |
| `40301` | **403** | IP 不在白名单 / 媒体代理未启用 | 否 | 检查白名单 |
| `40401` | 200 | 剧集或分集不存在 | 否 | 刷新分集表 |
| `40901` | 200 | 上游拒绝请求 | 否 | 换集或查材料 |
| `42201` | 200 | 该集暂无兼容片源 | 否 | 降级 `quality` 或换集 |
| `42901` | 200 | 限流 | **是** | 按 `Retry-After` 退避 |
| `50001` | 200 | 服务端内部错误（含签名异常） | 一次 | 上报 `request_id` |
| `50201` | 200 | 上游网络或响应异常 | **是** | 指数退避，上限 3 次 |
| `50301` | 200 | 上游全部路由失败 | **是** | 指数退避 |
| `50401` | 200 | 解析超时 / 请求被取消 | **是** | 退避重试 |

`40101` / `40301` 会带 HTTP 401/403，其余业务错误都是 HTTP 200 + `code` 非 0。

**限流响应额外带 `Retry-After` 头**（秒，默认 2）。

---

## 2. 上游错误码 → 网关码映射

网关在 `message` 中保留上游原始码，形如 `上游拒绝请求: … (upstream=BUSINESS_REJECTED)`。

| 上游 code | 网关 code | 触发场景 |
|---|---|---|
| `MISSING_EPISODE` | `40401` | 详情接口没有该集分集（App API 与网页兜底都失败） |
| `DETAIL_REJECTED` | `40901` | 详情接口业务码非 0 |
| `BUSINESS_REJECTED` | `40901` | 上游明确拒绝（重试无用） |
| `SECURITY_BLOCKED` | `40901` | 上游返回了不在白名单的地址 / 异常重定向 |
| `HTTP_403` | `40901` | 上游 403 —— **常见于签名材料失效** |
| `MISSING_MODEL` | `42201` | `video_model` 里没有该集模型 |
| `NO_STREAM` | `42201` | 没提取到任何流 |
| `MEDIA_REJECTED` | `42201` | 所有候选直链探活都失败 |
| `MEDIA_TYPE_INVALID` | `42201` | `content-type` 不是 `video/*` |
| `MEDIA_URL_EXPIRED` | `42201` | 候选 URL 已过期 |
| `MEDIA_HTTP_<n>` | `42201` | 探活返回非 200/206（`n` 为状态码） |
| `HTTP_429` | `42901`（+`Retry-After`） | 上游限流 |
| `ETIMEDOUT` / `TIMEOUT` | `50401` | 上游超时 |
| `CANCELLED` | `50401` | 请求已取消或超时 |
| `DISCOVERY_REJECTED` | `50201` | 上游发现类接口业务码非 0（默认分支） |
| `INVALID_RESPONSE` / `EMPTY_RESPONSE` / `RESPONSE_NOT_UTF8` | `50201` | 上游响应解析失败 |
| `HTTP_0` / `DNS` / `CONNECTION` / `HTTP_5xx` | `50201` | 网络层异常 |
| *（其余未知上游码）* | `50201` | 兜底 |
| *（`routesExhausted`）* | `50301` | 所有上游路由均失败 |

### 2.1 签名类错误（只出现在服务端日志，不透出细节）

| 内部错误 | 网关表现 | 含义 |
|---|---|---|
| `signer.SigningError` | `50001`「服务端签名异常，请上报 request_id」 | 移植错误或材料异常。**故意不暴露内部细节** |
| `signer.ErrMissingConfiguration` | `50001`「服务端签名配置缺失」 | 没有可用材料 |
| `V5_INPUT_SHAPE` / `V5_INPUT_BINDING` / `V5_DIGEST_BINDING` / `V5_EXTRACTION_SIZE` / `V5_PROTO_*` | 同上（`50001`） | 字节序 / 移位 / 字段号移植错误。详见 [`03-algorithm-spec.md`](03-algorithm-spec.md) §6.3 |
| `SIGNING_CONFIGURATION_INVALID` | 同上 | material 不是 32 字节 / id 长度越界 |
| `LOCAL_PROFILE_UNSUPPORTED` | 同上 | `sdk_version` 不是 `v04.10.03-ml-android` |
| `context.DeadlineExceeded` | `50401`「解析超时 (upstream=ETIMEDOUT)」 | 整体超时 |

---

## 3. 排障决策树

### 3.1 `resolve` 失败

```
resolve 返回 code != 0
│
├─ 40001 ──→ 参数问题
│     ├─ message 含"游标" ──→ 翻页从第 1 页重来
│     └─ message 含"10~30 位数字" ──→ 检查 series_id / vid 格式
│
├─ 40101 ──→ Token / HMAC 问题
│     ├─ "缺少认证凭据" ──→ 没带 Authorization 或 X-API-Key
│     ├─ "认证 Token 无效" ──→ Token 不匹配
│     ├─ "缺少或非法的请求签名头" ──→ HMAC 模式开了但头没带全
│     ├─ "请求签名已过期" ──→ 客户端时钟偏差 > hmac_max_skew_seconds
│     └─ "请求 Nonce 重放" ──→ 客户端重用了 nonce
│
├─ 40301 ──→ IP 不在白名单 / 媒体代理未启用
│
├─ 40401 ──→ 该集不存在或已下架。用 /series 确认
│
├─ 40901 ──→ 上游拒绝
│     ├─ upstream=HTTP_403 ──→ 签名材料可能已失效（最常见）
│     ├─ upstream=BUSINESS_REJECTED ──→ 上游明确拒绝该请求，重试无用
│     └─ upstream=SECURITY_BLOCKED ──→ 上游返回了异常地址
│
├─ 42201 ──→ 无可用片源
│     ├─ upstream=MISSING_MODEL ──→ 该集暂无片源（换集）
│     ├─ upstream=MEDIA_HTTP_403 ──→ 直链被拒，重试一次
│     └─ upstream=MEDIA_TYPE_INVALID ──→ 返回的不是视频
│
├─ 42901 ──→ 退避重试（遵循 Retry-After）
│
├─ 50201 / 50301 / 50401 ──→ 退避重试（指数退避，最多 3 次）
│     └─ 持续失败 ──→ 上报 request_id，查 /health 的 upstream 段
│
└─ 50001 ──→ 服务端签名或内部异常，带 request_id 上报
```

### 3.2 `resolve` 成功但播不了

```
resolve 返回 code == 0，播放失败
│
├─ 播放器报"容器能开但无画面/花屏"
│     └─ ▸▸ 最可能：没传 decryption_key ▸▸
│
├─ HTTP 403
│     ├─ 客户端自己加了 Referer ──→ 去掉
│     └─ 直链过期 ──→ 看 expires_at，重新 resolve
│
├─ HTTP 200 但浏览器播不了
│     └─ 片源是 CENC 加密 MP4（encv/enca/senc/tenc），浏览器不支持
│        → 必须走服务端预解密 /api/v1/media/{ticket}
│
├─ 老设备有声音没画面
│     └─ 编码是 HEVC，设备不支持 → 请求时传 prefer_codec:["h264"] 或降 quality
│
└─ 首帧很慢 / 卡顿
      └─ 正常（CDN 重定向链 + 大文件）。必要时服务端预热
```

### 3.3 `catalog` / `search` 失败

| code | 排查 |
|---|---|
| `40001` + "游标" | 翻页顺序错误或游标过期。**必须从第 1 页顺序翻**（catalog 2h / search 15min TTL） |
| `50201` + `upstream=DISCOVERY_REJECTED` | 上游发现接口拒绝。检查材料有效性 |
| `50301` | 上游路由全挂 |

---

## 4. 常见根因速查

| 现象 | 根因 | 定位方法 |
|---|---|---|
| 突然大面积 40901 | 签名材料轮换 | `/health` 看 `signing.rotated_at`；更新 `profile_url` |
| 突然大面积 50201 + DNS | 出网 DNS 故障 | 服务器上 `curl -I https://reading.snssdk.com` |
| 同一集反复 42201 | 该集上游没片源 | 换集验证；`upstream=MISSING_MODEL` |
| 间歇性 50301 | 某条上游路由挂了 | `upstream_fallback_total` 上升 |
| 403 但直链刚生成 | 客户端 Referer 错误 | 抓包看有没有 Referer 头 |
| 40101 突然增多 | Token 轮换但客户端没更新 | 对比 `auth_tokens` 配置 |
| 42901 增多 | 客户端重试风暴 | 检查客户端退避逻辑 |
| 签名头每次都不同 | 正常（`x-gorgon`/`x-helios`/`x-medusa` 含随机数） | — |
| `x-ladon` 与 `x-argus` 值不同 | 正常（同一 epoch 两种字节序） | — |

---

## 5. 服务端自检脚本

```bash
# 1) 端到端冒烟：跑参考实现，确认上游链路与直链仍然可用
node tools/golden/recorder.js tools/golden/out

# 2) 用产出的直链 + 密钥验证解密
node -e "const s=require('./tools/golden/out/smoke.json');console.log(s.play_url,s.decryption_key)"
ffmpeg -hide_banner -loglevel error \
       -decryption_key "<decryption_key>" -user_agent "<UA>" \
       -i "<play_url>" -frames:v 3 -f null -
# 期望：exit 0，无输出
```

---

## 6. 上报模板

客户端遇到问题请提供：

```
request_id: 01HQ8Z...            （响应头 X-Request-Id 同值）
time:       2026-11-16T05:35:28Z
code:       50301
message:    上游视频模型接口全部路由失败: routesExhausted, last=MISSING_MODEL
source:     hongguo
series_id:  7701678491724418073
episode_no: 1
client:     android/1.2.3
```

**不要提供**：`play_url`、`decryption_key`、签名头。

---

## 7. 相关文档

- 接口契约 → [`02-client-api.md`](02-client-api.md)
- 找片接口 → [`07-source-browsing.md`](07-source-browsing.md)
- 排障与部署 → [`10-deployment.md`](10-deployment.md) §8
