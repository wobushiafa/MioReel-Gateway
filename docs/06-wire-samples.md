# 报文样例

> 全部为**真实抓取**（`node tools/golden/recorder.js`），非手写构造。
> 原始数据见 `tools/golden/out/wire-samples.json`。
>
> 抓取时间：2026-11-16T05:30Z 左右；设备为一次性访客态（`iid`/`device_id` 为本次注册生成，用完即弃）。

---

## 1. 请求全链路（6 条）

| # | 方法 | 目标 | 签名头 |
|---|---|---|---|
| 1 | POST | `log.snssdk.com/service/2/device_register/` | 无（仅 UA） |
| 2 | GET | `reading.snssdk.com/reading/bookapi/bookmall/tab/v` | 6 个 |
| 3 | GET | `reading.snssdk.com/reading/bookapi/bookmall/cell/change/v` | 6 个 |
| 4 | POST | `reading.snssdk.com/novel/player/video_detail/v1/` | 6 个 + `x-ss-stub` + `lc: 101` |
| 5 | POST | `api5-normal-sinfonlineb.fqnovel.com/novel/player/multi_video_model/v1/` | 6 个 + `x-ss-stub` |
| 6 | HEAD | `v5-reading-video.qznovelvod.com/...`（直链探活） | 仅 UA |

> **#4 是拿分集表**（详情接口），**#5 是拿播放模型**（直链来源）。两者都要签名。

---

## 2. 设备注册（#1）

```http
POST https://log.snssdk.com/service/2/device_register/?aid=8662&app_name=novelread
Content-Type: application/json; charset=utf-8
User-Agent: com.phoenix.read/71332 (Linux; U; Android 16; zh_CN; 25053RT47C; Build/BP2A.250605.031.A3; Cronet/TTNetVersion:04657795 2026-01-23 QuicVersion:c67e9834 2025-09-08)
```

```json
{
  "magic_tag": "ss_app_log",
  "header": {
    "aid": 8662,
    "app_name": "novelread",
    "package": "com.phoenix.read",
    "app_version": "7.3.9.32",
    "version_code": 73932,
    "update_version_code": 73932,
    "manifest_version_code": 73932,
    "sdk_version": "6.18.0",
    "device_platform": "android",
    "os": "Android",
    "os_version": "16",
    "os_api": 36,
    "device_model": "25053RT47C",
    "device_brand": "Redmi",
    "device_manufacturer": "Xiaomi",
    "density_dpi": 520,
    "display_density": "mdpi",
    "resolution": "1280x2772",
    "language": "zh",
    "timezone": 8,
    "access": "wifi",
    "channel": "update_64",
    "cdid": "5ecfd14f-e6e9-42f8-89ac-bb8e9ab842aa",
    "openudid": "8837d5baab18e63f"
  },
  "_gen_time": 1794807025084
}
```

响应关键字段：`install_id_str` → `iid`，`device_id_str` → `device_id`。

---

## 3. 详情接口（#4）—— 拿分集表

```http
POST https://reading.snssdk.com/novel/player/video_detail/v1/?ac=wifi&channel=update_64&aid=8662&app_name=novelread&version_code=73932&version_name=7.3.9.32&device_platform=android&os=android&ssmix=a&device_type=25053RT47C&device_brand=Redmi&language=zh&os_api=36&os_version=16&manifest_version_code=73932&resolution=1280*2772&dpi=520&update_version_code=73932&host_abi=arm64-v8a&dragon_device_type=phone&pv_player=73932&compliance_status=0&need_personal_recommend=1&player_so_load=1&is_android_pad_screen=0&iid=4069772115858074&device_id=4069772115853978&cdid=5ecfd14f-e6e9-42f8-89ac-bb8e9ab842aa&ts=1794807027&_rticket=1794807027688

User-Agent: com.phoenix.read/71332 (Linux; U; Android 16; zh_CN; 25053RT47C; Build/BP2A.250605.031.A3; Cronet/TTNetVersion:04657795 2026-01-23 QuicVersion:c67e9834 2025-09-08)
Accept: application/json; charset=utf-8,application/x-protobuf
Content-Type: application/json; charset=UTF-8
x-xs-from-web: 0
x-ss-req-ticket: 1794807027688
x-tt-request-tag: t=0;n=0
sdk-version: 2
passport-sdk-version: 50561
x-vc-bdturing-sdk-version: 3.7.2.cn
x-ss-stub: F0C96E4FE1E592E0FD0AC01D7B512A42
x-gorgon: 840466184001d0ff4cd607861000d50671822f964ed63c43d863
x-khronos: 1794807027
x-ladon: avqU8w==
x-argus: 85T6ag==
x-helios: spvr3sKfjOuTIYi5Mb8dMQXibYuRALzVC/SI+Q4sSuDiPc47
x-medusa: 9pT6at7ftaC64fcpzCFURtH5NjzKvwABgEqFsGMfgUcHmOMtR6huh1ZLhu9E2GyBY8Xao6Kl4YaEPAnglP8VFlxCre8CKBr5dqlTfuzU/Srn0FfwCoGB/lqTNhr6G7EZ/H7dYpep97XJseM++RKnliffACdTTkPpB9DU9H+KG7dlj6xt+DGnAsa5pE4uOObteDvc1ETegzpZLovpxpMcodXp5NilChVwKFJx6dNBoZjY4RfiDJWyJjdiv+wSIPv9P8Df9T/Ef1pl
x-tt-dt:
lc: 101
```

```json
{"series_id":"7701678491724418073"}
```

---

## 4. 播放模型接口（#5）—— 直链来源 ⭐

```http
POST https://api5-normal-sinfonlineb.fqnovel.com/novel/player/multi_video_model/v1/?ac=wifi&channel=update_64&aid=8662&app_name=novelread&version_code=73932&version_name=7.3.9.32&device_platform=android&os=android&ssmix=a&device_type=25053RT47C&device_brand=Redmi&language=zh&os_api=36&os_version=16&manifest_version_code=73932&resolution=1280*2772&dpi=520&update_version_code=73932&host_abi=arm64-v8a&dragon_device_type=phone&pv_player=73932&compliance_status=0&need_personal_recommend=1&player_so_load=1&is_android_pad_screen=0&iid=4069772115858074&device_id=4069772115853978&cdid=5ecfd14f-e6e9-42f8-89ac-bb8e9ab842aa&ts=1794807027&_rticket=1794807027902

User-Agent: com.phoenix.read/71332 (Linux; U; Android 16; zh_CN; 25053RT47C; Build/BP2A.250605.031.A3; Cronet/TTNetVersion:04657795 2026-01-23 QuicVersion:c67e9834 2025-09-08)
Accept: application/json; charset=utf-8,application/x-protobuf
Content-Type: application/json; charset=UTF-8
x-xs-from-web: 0
x-ss-req-ticket: 1794807027902
x-tt-request-tag: t=0;n=0
sdk-version: 2
passport-sdk-version: 50561
x-vc-bdturing-sdk-version: 3.7.2.cn
x-ss-stub: 813FA23B2F03B790778C385BD94D7820
x-gorgon: 84044c8140010f3f6d21096f9dcbb442323e167450fe8de26139
x-khronos: 1794807027
x-ladon: avqU8w==
x-argus: 85T6ag==
x-helios: XXwGRF88uH/CSbGENxzknDvvdoI7IYvvZ3dFz/WutOx0Hf1Q
x-medusa: 9pT6at7ftaC64fcpzCFURtH5Njz6cAABvErrPR9xAKgDmBR/V0Z/IGqUDaHEHf7NH0W4llBXUBhxI+CfKSwwfcGkK+7G37T8ge1rQXecIoNj2boYMcc3+jOLuHxySKt56lUDyGgPdmpJk0vqmBLhUxwiaHKQeK7ddWQZp74roMqDNIhCMYhQz3GWt59RgZPJJiwfX9cLctux5vGA6ue+pm2FC6jP7s6HwTMfnvouHJFWE0A8267H1aMOnkhPw73+B+j//heo/z9o
x-tt-dt:
```

```json
{
  "biz_param": {
    "detail_page_version": 0,
    "device_level": 3,
    "disable_digg_stat": false,
    "need_all_video_definition": true,
    "need_mp4_align": false,
    "use_os_player": false,
    "use_server_dns": false,
    "video_platform": 1024
  },
  "mixed_video_id_map": {
    "1004": ["7701680675979545625"]
  }
}
```

### 4.1 query 参数逐项说明

| 参数 | 值 | 来源 |
|---|---|---|
| `ac` | `wifi` | 固定 |
| `channel` | `update_64` | 固定 |
| `aid` | `8662` | 固定（番茄小说 aid） |
| `app_name` | `novelread` | 固定 |
| `version_code` / `version_name` | `73932` / `7.3.9.32` | 固定 |
| `device_platform` | `android` | 固定 |
| `os` | `android` | 固定 |
| `ssmix` | `a` | 固定 |
| `device_type` / `device_brand` | `25053RT47C` / `Redmi` | 固定（伪装机型） |
| `language` | `zh` | 固定 |
| `os_api` / `os_version` | `36` / `16` | 固定 |
| `resolution` / `dpi` | `1280*2772` / `520` | 固定 |
| `host_abi` | `arm64-v8a` | 固定 |
| `dragon_device_type` | `phone` | 固定 |
| `pv_player` | `73932` | 固定 |
| `compliance_status` | `0` | 固定 |
| `need_personal_recommend` / `player_so_load` | `1` / `1` | 固定 |
| `is_android_pad_screen` | `0` | 固定 |
| **`iid`** | `4069772115858074` | **设备注册返回** |
| **`device_id`** | `4069772115853978` | **设备注册返回** |
| **`cdid`** | `5ecfd14f-…` | **本地生成** |
| **`ts`** | `1794807027` | Unix 秒 |
| **`_rticket`** | `1794807027902` | Unix 毫秒 |

> **`iid` / `device_id` / `cdid` / `ts` / `_rticket` 五项都参与签名**。
> 固定参数原样照抄即可，只有加粗的 5 项每次不同。

### 4.2 签名头对照

| 头 | 长度 | 说明 |
|---|---|---|
| `x-gorgon` | 48 hex（24 字节） | 前缀 `8404` + 2 字节 + 20 字节密文 |
| `x-khronos` | 10 | Unix 秒，等于 query 里的 `ts` |
| `x-ladon` | 8（base64） | 4 字节**大端** epoch |
| `x-argus` | 8（base64） | 4 字节**小端** epoch |
| `x-helios` | 56（base64） | 4 字节随机 + 34 轮 SM4 风格块 |
| `x-medusa` | 348（base64） | `header(20) ‖ meta(4) ‖ coded ‖ embed` |
| `x-ss-stub` | 32 hex | `MD5(body).toUpperCase()`，**body 为空时必须省略** |

> `x-ladon` 与 `x-argus` 在同一次请求里是**同一 epoch 的两种字节序**（大端/小端）。
> `x-helios` 与 `x-gorgon` 因为含随机数，**每次请求都不同**；`x-medusa` 也含随机数。

---

## 5. 解析结果（直链）

```json
{
  "play_url": "https://v5-reading-video.qznovelvod.com/9fd7ad299396469a6548479331e566ab/6afae9ff/video/tos/cn/tos-cn-v-6fcc8e/xxx",
  "play_headers": {
    "User-Agent": "com.phoenix.read/71332 (Linux; U; Android 16; zh_CN; 25053RT47C; Build/BP2A.250605.031.A3; Cronet/TTNetVersion:04657795 2026-01-23 QuicVersion:c67e9834 2025-09-08)"
  },
  "decryption_key": "982735c47051044342ede4ef5d3e8cb5",
  "quality": 1080,
  "expires_at": 1794808528211
}
```

直链路径结构：

```
https://v{5,9,11}-reading-video.qznovelvod.com/
  {32 hex}/          ← 资源 id
  {8 hex}/           ← 桶/分片 id
  video/tos/cn/tos-cn-v-6fcc8e/
  {base64ish}/       ← 资源名
  ?a=8662&ch=0&cr=9&dr=0&er=1&lr=default&cd=0%7C0%7C0%7C9
   &cv=1&bt=706&cs=4&ds=4&eid=59904
   &ft=…&mime_type=video_mp4&qs=0&rc=…&ult=983&btag=…
   &cdn_type=2&dy_q=1794805689&feature_id=…
   &l=202611161308098B14CCD9B0DE2CE7A4F6
   &n80=1&n80_dm=bjk4LXYtcmVhZGluZ3ZpZGVvLnF6bm92ZWx2b2QuY29t
   &n80_tk=e1dfe46d6e3d5db933e6732eb73f384e
   &pwid=297&req_cdn_type=r
```

> 查询参数里的 `dy_q`（时间戳）、`l`、`ft`、`n80_tk` 是 **CDN 侧的时效令牌**。
> 这解释了为什么直链会过期 —— 令牌到期后 CDN 侧不再放行新的请求。

---

## 6. 直链探测（#6）

```http
HEAD https://v5-reading-video.qznovelvod.com/9fd7ad299396469a6548479331e566ab/6afae9ff/...
```

响应：

```
HTTP/1.1 200 OK
content-type: video/mp4
content-length: 9131576
accept-ranges: bytes
access-control-allow-origin: *
server: Byte-nginx
```

---

## 7. 解密验证（客户端必须做对）

```bash
# ✅ 正确：带密钥
ffmpeg -hide_banner -loglevel error \
       -decryption_key 982735c47051044342ede4ef5d3e8cb5 \
       -user_agent "com.phoenix.read/71332 (Linux; U; Android 16; zh_CN; 25053RT47C; ...)" \
       -i "https://v5-reading-video.qznovelvod.com/9fd7ad299396469a6548479331e566ab/6afae9ff/..." \
       -frames:v 3 -f null -
# → exit 0，无输出

# ❌ 错误：不给密钥
ffmpeg ... -i "<同上 URL>" -frames:v 3 -f null -
# → Invalid NAL unit size (112013893 > 6584)
# → Error splitting the input into NAL units.
# → [aac @ …] Reserved bit set. / invalid band type
```

`ffprobe` 验证（带密钥）：

```
index=0
codec_name=hevc
codec_type=video
width=1080
height=1920
index=1
codec_name=aac
codec_type=audio
```

---

## 8. 绑定条件实测矩阵

对同一条直链分别发 HEAD：

| # | User-Agent | Referer | 结果 |
|---|---|---|---|
| A | App UA | — | **200** `video/mp4` |
| B | 不带 | — | **200** |
| C | 浏览器 UA | — | **200** |
| D | 空串 | — | **200** |
| E | App UA | `https://novelquickapp.com/` | **200** |
| F | App UA | `https://example.com/` | **403** `text/html` 160B |

**结论**：UA 不绑定；**Referer 必须缺省或等于 `https://novelquickapp.com/`**。

---

## 9. 文件结构实测

直链下载前 256 KB 扫描命中：

| box | 偏移 | 含义 |
|---|---|---|
| `ftyp` | 0x04 | `isom` brand |
| `moov` | 0x20 | 在文件头（faststart） |
| `stsd` | 0x1b9 | sample description |
| **`encv`** | **0x1c9** | **加密视频 sample entry** |
| `sinf` | 0x316 | protection scheme info |
| `schm` | 0x32a | scheme type |
| `tenc` | 0x346 | track encryption |
| `btrt` | 0x366 | bitrate |
| `senc` | 0x14756 | 样本 IV 表 |
| **`enca`** | **0x1ea04** | **加密音频 sample entry** |

→ 这是 **CENC / ISO 23001-7 风格的受保护 MP4**，浏览器无法直接播放（详见 `04-client-integration.md` §5）。

---

## 10. 重新采集

```bash
node tools/golden/recorder.js tools/golden/out
```

产出：
- `tools/golden/out/wire-samples.json` —— 完整请求 + 解析结果
- `tools/golden/out/smoke.json` —— 最小可用清单

> ⚠️ 每次采集都会注册一个新的访客设备（新的 `iid`/`device_id`/`cdid`），签名头与直链都会变。
> 用于**比对结构**而非**比对具体值**。
