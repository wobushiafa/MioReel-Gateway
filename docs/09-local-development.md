# 本地开发

---

## 1. 环境要求

| 工具 | 版本 | 必需性 | 用途 |
|---|---|---|---|
| Go | **1.25.5+**（`go.mod` 声明 `go 1.25.5`） | 必需 | 构建与测试服务端 |
| Node.js | 18+（实测 26.8.1） | 推荐 | 跑参考实现、录制黄金样本 |
| ffmpeg | 6+（实测 7.x） | 可选 | 媒体代理；验证解密播放 |
| git | 任意 | 可选 | 版本管理 |

检查：

```bash
go version
node --version
ffmpeg -version
```

---

## 2. 目录结构

```
media-gateway/
├─ server/                     Go 模块（module: mioreel-gateway/server）
│  ├─ types.go                 ★ 对外公共契约类型（客户端 API 的可执行形式）
│  ├─ cmd/gateway/main.go      进程入口
│  └─ internal/
│     ├─ api/                  HTTP 层：路由、鉴权、限流、请求 ID、panic 恢复
│     ├─ service/              业务编排：catalog / series / resolve / search / danmaku / media
│     ├─ resolver/             提流：筛选、排序、三态地址解析、no()/io() 解码
│     ├─ signer/               签名引擎：digest / packet / gorgon / helios / 三套摘要变体
│     ├─ upstream/             上游 HTTP 客户端 + 红果端点封装
│     ├─ device/               设备三元组注册与持久化
│     ├─ profile/              签名材料管理与热更新
│     ├─ cache/                TTL 缓存
│     ├─ observability/        日志、指标
│     └─ config/               配置加载
├─ tools/golden/               参考实现 + 样本工具（Node）
│  ├─ reference-hongguo.js     上游参考实现原件（只读，勿改）
│  ├─ hongguo_host.js          Node 宿主沙箱
│  ├─ recorder.js              播放链路报文录制器
│  ├─ recorder_discovery.js    发现类链路报文录制器
│  └─ out/                     实测产物
├─ configs/gateway.example.json
├─ docs/
└─ Makefile
```

---

## 3. 常用命令

全部通过 `Makefile`（在仓库根目录执行）：

```bash
make build       # 构建 → bin/media-gateway（-trimpath）
make test        # go test -count=1 ./...
make test-race   # 带竞态检测（CI 用这个）
make vet         # go vet ./...
make fmt         # gofmt -w .
make tidy        # go mod tidy
make run         # 构建并启动（默认 :18662）
make golden      # 录制播放链路样本 → tools/golden/out/
make clean       # 清理 bin/ dist/ coverage.out
```

或直接用 Go 工具链（在 `server/` 目录下）：

```bash
cd server
go build ./...
go test ./...
go test -race ./...
go vet ./...
```

> **CI 会因 gofmt 不合格而失败**（`.github/workflows/ci.yml`），提交前务必 `make fmt`。

---

## 4. 本地启动

### 4.1 最简（仅本地）

```bash
export MIOREEL_ALLOW_INSECURE_NO_AUTH=true
./bin/media-gateway
# 或带配置文件
./bin/media-gateway --config configs/gateway.json
```

启动日志：

```
level=INFO msg="mioreel-gateway listening" addr=:18662 version=1.0.0
```

### 4.2 带鉴权

```bash
export MIOREEL_AUTH_TOKENS="dev-token-1,dev-token-2"
./bin/media-gateway
```

### 4.3 冒烟

```bash
curl -s http://127.0.0.1:18662/api/v1/health | jq .

# 找一个剧
curl -s "http://127.0.0.1:18662/api/v1/catalog?category=real-drama&page=1" \
  -H "Authorization: Bearer dev-token-1" | jq '.data.list[0]'

# 拿分集
SID=$(curl -s "http://127.0.0.1:18662/api/v1/catalog?category=real-drama&page=1" \
  -H "Authorization: Bearer dev-token-1" | jq -r '.data.list[0].series_id')
curl -s -X POST http://127.0.0.1:18662/api/v1/series \
  -H "Authorization: Bearer dev-token-1" -H "Content-Type: application/json" \
  -d "{\"series_id\":\"$SID\"}" | jq '.data.episodes[0]'

# 解析直链
curl -s -X POST http://127.0.0.1:18662/api/v1/resolve \
  -H "Authorization: Bearer dev-token-1" -H "Content-Type: application/json" \
  -d "{\"series_id\":\"$SID\",\"episode_no\":1}" | jq '.data | {play_url, decryption_key, expires_at}'
```

> 首次调用会触发**访客设备注册**（`log.snssdk.com/service/2/device_register/`），需要出网。注册结果落在 `data/device.json`。

---

## 5. 黄金向量工作流（改签名代码必看）

### 5.1 现有向量文件

`server/internal/signer/testdata/golden_vectors.json`，由 `signer_test.go` 消费：

| 集合 | 条数 | 内容 | 对应测试 |
|---|---|---|---|
| `selectors` | **16** | `selector, query, bodyHashHex, epoch, materialHex, nonce, protoHex, normalJniFalse, normalJniTrue` | `TestComponentsAll16Selectors` |
| `signs` | **4** | `url, bodyBase64, epoch, device, randoms, gorgonRand, heliosRand, counter, naturalSelector, naturalDigestHex, protoHex, headers, realHeaders` | `TestSignHeadersGoldenParity` |

`selectors` 覆盖 **全部 16 个 selector**，即三条摘要分支（改造 SHA-1 / 改造 MD5 / `thirdHead`）都被验证到 —— 这是 [`03-algorithm-spec.md`](03-algorithm-spec.md) 铁律 2 的落地保障。

### 5.2 跑测试

```bash
cd server
go test ./internal/signer/ -run TestComponentsAll16Selectors -v
go test ./internal/signer/ -run TestSignHeadersGoldenParity -v
```

失败时错误信息会直接指出哪个 selector / 哪个头不一致。

### 5.3 什么时候需要重新生成向量

只有当**上游算法真的变了**（材料轮换导致 `material` 变化，或红果改了摘要算法）才需要重新生成。

向量是**一次性生成物**：仓库内没有生成脚本，需要用参考实现导出。做法：

1. 用 `tools/golden/recorder.js` 录制参考实现的请求；
2. 从 `wire-samples.json` 中取出 query / body / epoch / 6 个头；
3. 按 `golden_vectors.json` 的字段结构填入 `selectors` 与 `signs`。

> ⚠️ **不要为了让测试变绿而手工改期望值。** 向量是「参考实现产出的事实」，改了它就失去了校验意义。正确做法是**把参考实现移植到 Go**，让 Go 与参考实现一致 —— 若两者不一致，是 Go 侧移植错了。

### 5.4 端到端对照（真实网络）

```bash
# 1) 跑参考实现，拿到真实直链与密钥
node tools/golden/recorder.js tools/golden/out

# 2) 用产出的直链 + 密钥验证解密播放
node -e "const s=require('./tools/golden/out/smoke.json');console.log(s.play_url);console.log(s.decryption_key)"
ffmpeg -hide_banner -loglevel error \
       -decryption_key <decryption_key> -user_agent "<UA>" \
       -i "<play_url>" -frames:v 3 -f null -
# 期望：exit 0，无任何错误输出
```

对照组（去掉 `-decryption_key`）会得到 `Invalid NAL unit size` —— 这是"密钥没生效"的典型症状。

---

## 6. 录制样本工具

| 脚本 | 产出 | 覆盖 |
|---|---|---|
| `tools/golden/recorder.js` | `out/wire-samples.json`、`out/smoke.json` | 设备注册 → 详情 → video_model → 直链 → HEAD 探活 |
| `tools/golden/recorder_discovery.js` | `out/discovery-samples.json` | home / category / suggestions / search / rankings / recommendations / feed |

```bash
node tools/golden/recorder.js tools/golden/out
node tools/golden/recorder_discovery.js tools/golden/out
```

`hongguo_host.js` 是 Node 侧的**宿主沙箱**，模拟 Dart 注入给 JS 的运行时接口：

| 注入项 | 说明 |
|---|---|
| `fetch(url, {method, headers, body, redirect:'manual', timeoutMs})` | 必须支持 `redirect:'manual'`（3xx 要原样交回）与 `timeoutMs` |
| `crypto.{getRandomValues, base64, utf8, md5, decryptBlock}` | `decryptBlock` = **AES-128-ECB**；其余返回**字节数组** |
| `globalThis.__native_save_device(json)` | 设备三元组落盘 |
| `globalThis.__hongguoEntropy` | 随机源兜底 |

---

## 7. 修改代码时的约定

### 7.1 契约类型

`server/types.go` 是**对外契约**。改动它必须同步改 `docs/02-client-api.md` 与 `docs/07-source-browsing.md`。

> ⚠️ **`Source` 是类型别名（`type Source = string`）而不是独立类型。**
> 内部 service 层按普通 `string` 使用它。改成 `type Source string` 会导致 `resolve.go` 编译失败。

### 7.2 32 位溢出

签名相关代码里所有位运算一律用 `uint32`；JS 语义对照见 [`03-algorithm-spec.md`](03-algorithm-spec.md) §0 铁律 1。

### 7.3 敏感信息

**绝不写入日志/指标**：`play_url`、`decryption_key`、6 个签名头、`Authorization`、`hmac_secret`。

---

## 8. 新增一个视频源

网关按多源设计（`Source` 字段 + 路由表）：

1. 在 `internal/signer` 中实现该源的签名（若与红果不同）；
2. 在 `internal/resolver` 中实现提流（上游模型 → `[]ExtractedStream`）；
3. 在 `internal/upstream` 中封装该源的上游端点；
4. 在 `internal/service` 中注册 `source → service` 路由；
5. 在 `internal/api` 中暴露端点；
6. 补文档：`docs/02-client-api.md`（契约）+ `docs/07-source-browsing.md`（上游映射）。

---

## 9. 常见问题

| 症状 | 原因 | 处理 |
|---|---|---|
| 启动退出码 2 | 没配 Token 且没开 `allow_insecure_no_auth` | 配 `MIOREEL_AUTH_TOKENS` 或本地开 `allow_insecure_no_auth` |
| 启动退出码 1 | 配置文件读不到或 JSON 语法错 | 检查 `-config` 路径与 JSON |
| `resolve` 全部 40901 | 签名材料失效 | 更新 `profile_url` / `profile_file`，见 [`08-configuration.md`](08-configuration.md) §5 |
| `resolve` 全部 50201 | 出网被墙 / DNS 异常 | 服务器上 `curl -I https://reading.snssdk.com` 自检 |
| `resolve` 50401 | 超时 | 调大 `default_resolve_timeout_ms`；或上游慢，查 `resolve_duration_ms` 指标 |
| 测试报 `V5_DIGEST_BINDING` | 移植的字节序/移位错了 | 对照 `03-algorithm-spec.md` §6.3 与 `reference-hongguo.js` 对应行 |
| CI 报 gofmt 失败 | 格式不符 | `make fmt` |
| `catalog` 第 2 页 40001 | 翻页游标过期（2h TTL）或跳页 | 从第 1 页顺序翻 |
| `search` 第 2 页 40001 | 搜索游标过期（15min）或跳页 | 同上 |

---

## 10. 相关文档

- 接口契约 → [`02-client-api.md`](02-client-api.md)
- 算法规范 → [`03-algorithm-spec.md`](03-algorithm-spec.md)
- 找片接口 → [`07-source-browsing.md`](07-source-browsing.md)
- 配置 → [`08-configuration.md`](08-configuration.md)
- 部署 → [`10-deployment.md`](10-deployment.md)
