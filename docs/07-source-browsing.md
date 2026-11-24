# 获取视频源接口（Browse / Search / Feed）

> **本文档讲「客户端怎么找片」**：从红果（hongguo）拿到剧目列表、分类、搜索、详情分集、弹幕。
> 「客户端怎么拿到播放直链」见 [`02-client-api.md`](02-client-api.md)。
> 两份文档的上游都是同一个：**红果短剧 App API（`reading.snssdk.com`）**。

---

## 1. 一句话架构

网关把红果的**发现类**接口收敛成 4 个稳定的客户端端点：

```
客户端                    网关                              红果上游
  │  GET /catalog   ─────▶ │  GET /reading/bookapi/bookmall/tab/v        ┐
  │                       │  GET /reading/bookapi/bookmall/cell/change/v ├─ 签名
  │  POST /series   ─────▶ │  POST /novel/player/video_detail/v1/        │  (6 个头)
  │                       │  GET  https://hongguoduanju.com/detail?…    ┘  (网页兜底)
  │  GET /search    ─────▶ │  GET /reading/bookapi/search/tab/v
  │                       │  GET /reading/bookapi/search/suggest/v
  │                       │  GET /reading/bookapi/search/cue/v
  │  GET /danmaku   ─────▶ │  POST /novel/commentapi/comment/list/{vid}/v1/
```

**关键点：这些上游请求全部需要签名。** 客户端不需要（也不能）直连上游 —— 上游只接受 App UA + `x-gorgon`/`x-medusa`/`x-helios` 等 6 个签名头，且不开放 CORS。

---

## 2. 端点与上游映射

| 客户端端点 | 上游（hongguo App API） | 签名 | 说明 |
|---|---|---|---|
| `GET /api/v1/catalog` | `GET /reading/bookapi/bookmall/tab/v`<br>`GET /reading/bookapi/bookmall/cell/change/v` | ✅ | 分类剧目列表（游标分页） |
| `POST /api/v1/series` | `POST /novel/player/video_detail/v1/`<br>兜底 `GET https://hongguoduanju.com/detail?series_id=` | ✅ | 剧集详情 + 分集表 |
| `GET /api/v1/search` | `GET /reading/bookapi/search/tab/v` | ✅ | 搜索结果 |
| `GET /api/v1/danmaku` | `POST /novel/commentapi/comment/list/{vid}/v1/` | ✅ | 弹幕片段 |
| （未暴露）搜索联想 | `GET /reading/bookapi/search/suggest/v`<br>`GET /reading/bookapi/search/cue/v` | ✅ | 网关未开放，可按需加 |
| （未暴露）榜单 | `GET /reading/bookapi/plan/v` | ✅ | 网关未开放，可按需加 |

> 上游 host 一律 `reading.snssdk.com`（`device_register` 是 `log.snssdk.com`）。
> 全部走 `Ht()` 签名流程，详见 [`03-algorithm-spec.md`](03-algorithm-spec.md) §6–§8。

---

## 3. 分类语义

红果的分类是「genre id」，网关做了友好映射：

| 客户端 `category` | 红果 `selected_items` | 含义 |
|---|---|---|
| `real-drama`（默认） | `playlet` | 真人短剧 |
| `comic-drama` | `motion_comic` | 漫剧 |
| `ai-drama` | `ai_playlet` | AI 剧 |
| `all` | `all` | 全部短剧 |
| `playlet` / `motion_comic` / `ai_playlet` | 同名 | 直接使用原始 genre id |

红果的原始 genre 集合只有 4 个：`all` / `playlet` / `motion_comic` / `ai_playlet`。
网关在服务层做 `real-drama → playlet` 的转换，客户端不需要知道红果内部 id。

---

## 4. `GET /api/v1/catalog` — 分类剧目列表

### 4.1 Query

| 参数 | 必填 | 默认 | 取值 |
|---|---|---|---|
| `category` | 否 | `real-drama` | `real-drama` / `comic-drama` / `ai-drama` / `all` |
| `page` | 否 | `1` | 从 1 起 |
| `limit` | 否 | `40` | 上限 100 |

### 4.2 响应 `data`

```json
{
  "page": 1,
  "page_count": 2,
  "total": 6,
  "list": [
    {
      "series_id": "7597479073606028313",
      "title": "一品仵作甜妻飒爆全京城",
      "cover": "https://p3-novel.byteimg.com/novel-pic/7df4ad75...~tplv-shrink:640:0.image",
      "episode_count": 73,
      "remark": "全73集",
      "intro": "……",
      "tags": ["古装", "甜宠"],
      "year": ""
    }
  ]
}
```

| 字段 | 说明 |
|---|---|
| `series_id` | **剧集号（10~30 位数字）**。这是后续 `series` / `resolve` 的唯一入口参数 |
| `title` | 剧名 |
| `cover` | 封面。网关已按红果规则归一化（`novel-pic/xxx` → `~tplv-shrink:640:0.image`；`.heic` → `.image`） |
| `episode_count` | 总集数，0 表示未知 |
| `remark` | 如 `全73集` |
| `intro` | 简介 |
| `tags` | 标签数组，**永不为 `null`**（无标签时为空数组） |
| `year` | 恒为空字符串 —— 红果 App API 不提供年份，网关不编造 |

### 4.3 分页语义（重要）

红果上游是**游标分页**（`cell_id` + `offset` + `session_id`），网关把它包装成**页码分页**：

- 网关在服务端维护游标链，第 N 页对应上游的某个游标。
- **页码游标有 TTL（2 小时）**；客户端跨越 TTL 后请求一个旧页码，网关会返回 `40001`（游标过期）。
- 客户端应遵守：**翻页必须从第 1 页顺序进行**，不要跳页、不要缓存很久的页码再回访。
- `page_count` 是上游给的软上限，**不代表真实总页数**；**以 `list` 是否为空作为终止条件**。

---

## 5. `POST /api/v1/series` — 剧集详情 + 分集表

### 5.1 请求

```json
{ "series_id": "7701678491724418073" }
```

`series_id` 必须匹配 `^[0-9]{10,30}$`，否则 `40001`。

### 5.2 响应 `data`

```json
{
  "series_id": "7701678491724418073",
  "title": "二婚嫁对人，前夫悔破防",
  "cover": "https://p3-novel.byteimg.com/novel-pic/xxx~tplv-shrink:640:0.image",
  "intro": "……",
  "total_episodes": 80,
  "accessible_episodes": 80,
  "episodes": [
    { "no": 1, "vid": "7701680675979545625", "title": "第1集", "available": true },
    { "no": 2, "vid": "7701680675979545626", "title": "第2集", "available": true }
  ]
}
```

| 字段 | 说明 |
|---|---|
| `episodes[].vid` | **分集 id**。`resolve` 的第二入参就是它 —— 客户端拿到后应持久化，之后可跳过本接口直接 `resolve` |
| `episodes[].no` | 集序号，从 1 起 |
| `episodes[].available` | 是否可播。红果对未上线剧集会标 `disable_play` |
| `total_episodes` / `accessible_episodes` | 总集数 / 可看集数 |

### 5.3 上游取数顺序（双轨容错）

1. **主路（App API）**：`POST /novel/player/video_detail/v1/`
   body `{"series_id":"…"}`，取 `data.video_data.video_list[]` 的 `vid`。
2. **兜底（网页）**：`GET https://hongguoduanju.com/detail?series_id=…`
   解析页面里的 `_ROUTER_DATA` → `seriesDetail.vid_list[]`。

主路失败（`MISSING_EPISODE`）时网关自动走兜底；两条都失败才返回 `40401`。

---

## 6. `GET /api/v1/search` — 搜索

### 6.1 Query

| 参数 | 必填 | 默认 |
|---|---|---|
| `q` | 是 | — （关键词，空串返回空结果） |
| `page` | 否 | `1` |

### 6.2 响应 `data`

```json
{
  "query": "重生",
  "page": 1,
  "has_more": true,
  "total": 10,
  "list": [ { "series_id": "7683432711033211928", "title": "…", "…": "与 catalog 同结构" } ]
}
```

> `list` 元素结构与 `catalog` 完全一致（`SeriesSummary`），客户端可复用同一个渲染组件。

### 6.3 分页语义

搜索同样基于上游游标，**页码游标 TTL 15 分钟**，且**第 2 页依赖第 1 页建立游标**：

- 请求第 1 页 → 网关建立游标链。
- 请求第 2 页 → 沿用游标。
- **跳页请求（直接要第 3 页而没要过第 1 页）→ `40001`**，提示"搜索游标已过期，请从第一页重新搜索"。

### 6.4 搜索建议（网关暂未开放）

红果提供两个联想端点，客户端若需要可让网关补充：

| 场景 | 上游 | 参数 |
|---|---|---|
| 空关键词（热门词） | `GET /reading/bookapi/search/cue/v` | `limit=10` |
| 有关键词（补全） | `GET /reading/bookapi/search/suggest/v` | `query=…&count=10` |

共同固定参数：`bookstore_tab=16`、`tab_name=feed`、`search_source=1`、`user_is_login=0|1`。

---

## 7. `GET /api/v1/danmaku` — 弹幕

### 7.1 Query

| 参数 | 必填 | 默认 | 说明 |
|---|---|---|---|
| `danmaku_id` | 是 | — | 形如 `hongguo:{series_id}:{vid}`，取自 `resolve` 返回的 `danmaku_id` |
| `start_ms` | 否 | `0` | 本片段起点（毫秒） |
| `duration_ms` | 否 | `300000` | 片段长度（毫秒），上限 86400000 |

### 7.2 响应 `data`

```json
{
  "episode_id": "hongguo:7701678491724418073:7701680675979545625",
  "start_ms": 0,
  "next_ms": 30000,
  "items": [
    { "id": "7xxxxx", "time_ms": 1234, "text": "太好看了", "color": "#FFFFFF" }
  ]
}
```

- 每次返回**一个 30 秒片段**，`next_ms` 即下一段的 `start_ms`（客户端循环拉取即可实现滚动弹幕）。
- 单次最多 **90 条**。
- 弹幕是**按剧集（vid）**组织的，不是按剧。

---

## 8. 上游响应结构与字段映射

红果发现类接口的响应是一棵 `cell_view` 树：

```
data
└── tab_item[]                    ← 标签页
    └── cell_data[]               ← 内容单元（cell_view）
        ├── cell_id_str           ← 游标标识（分页用）
        ├── next_offset           ← 下一游标偏移
        ├── has_more              ← 是否还有更多
        ├── session_id            ← 会话标识
        ├── cell_data[]           ← 递归嵌套的子单元
        ├── cell_selector
        │   └── outer_row.items[] ← 分类选择器（selector_item_id / show_name / is_selected）
        └── video_data[]          ← ★ 剧目卡片
            ├── series_id_str / series_id   ← ★ 剧集号
            ├── title                       ← 剧名
            ├── cover                       ← 封面
            ├── episode_cnt                 ← 集数
            ├── video_desc / sub_title      ← 简介 / 副标题
            ├── tags[]                      ← 标签
            └── disable_play                ← 是否禁播
```

网关的提取规则（与参考实现 `extractVideos` 一致）：

| 规则 | 说明 |
|---|---|
| 递归遍历 | 深度遍历 `cell_data` 树，收集所有 `video_data` |
| 剧集号 | `series_id_str` 优先，空则取 `series_id`；必须匹配 `^[0-9]{10,}$` |
| 剧名 | `title` 去 HTML 标签 + trim；**为空则整条丢弃** |
| 禁播 | `disable_play == true` 则丢弃 |
| 去重 | 同一 `series_id` 只保留首次出现 |
| 集数 | `episode_cnt`；>0 时 `remark = "全N集"` |
| 标签 | `tags[]`；为空时用 `sub_title` 兜底；仍为空则给 `[]`（不返回 `null`） |

---

## 9. 缓存

| 数据 | 网关 TTL | 说明 |
|---|---|---|
| `catalog` 单页 | **5 分钟** | key = `genre:page:limit` |
| `series` 单剧 | **15 分钟** | key = `series_id` |
| `search` 会话 | **15 分钟** | 同一关键词的游标链；超时后需从第 1 页重来 |
| `catalog` 游标链 | **2 小时** | 过期后旧页码返回 `40001` |
| 上游发现 tab | 30 分钟 | 分类选择器结构很少变 |
| 榜单列表 | 15 分钟 | （网关暂未暴露） |

客户端侧建议 TTL 与网关对齐或更短（见 `02-client-api.md` §10）。

---

## 10. 错误映射

| 网关 `code` | 发现类场景下的典型触发 |
|---|---|
| `40001` | 参数格式错；**翻页游标过期 / 跳页** |
| `40101` | Token 无效 |
| `40301` | IP 不在白名单 |
| `40401` | 剧集不存在（`MISSING_EPISODE`，主路与网页兜底都失败） |
| `42901` | 限流 |
| `50201` | 上游网络错误 / 响应解析失败 |
| `50301` | 上游发现接口被拒（`DISCOVERY_REJECTED`）或全部路由失败 |
| `50401` | 超过客户端 `timeout_ms` |

上游发现类接口被拒时，上游业务码非 0 → 网关 `50301`，`message` 里带 `upstream=DISCOVERY_REJECTED`。

**排查顺序**：先看 `/api/v1/health` 的 `upstream.consecutive_failures`。若持续 ≥3 → `degraded`；≥10 且设备未就绪 → `down`。

---

## 11. 实测样本（真实抓包）

用 `node tools/golden/recorder_discovery.js` 实跑参考实现，得到的真实上游端点：

| 上游请求 | 次数 | 关键参数 |
|---|---|---|
| `POST log.snssdk.com/service/2/device_register/` | 1 | — |
| `GET reading.snssdk.com/reading/bookapi/bookmall/tab/v` | 2 | `tab_type=8&tab_index=0&offset=0`；`tab_type=16` |
| `GET reading.snssdk.com/reading/bookapi/bookmall/cell/change/v` | 3 | `cell_id=6952850996422770718&tab_type=8&selected_items=playlet&unlimited_selector_change_type=2&change_type=1&offset=0&limit=40` |
| `GET reading.snssdk.com/reading/bookapi/search/cue/v` | 1 | `limit=10&bookstore_tab=16&search_source=1` |
| `GET reading.snssdk.com/reading/bookapi/search/suggest/v` | 1 | `query=重生&count=10&bookstore_tab=16&search_source=1` |
| `GET reading.snssdk.com/reading/bookapi/search/tab/v` | 2 | `query=重生&tab_type=29&offset=0`；`offset=10` |
| `GET reading.snssdk.com/reading/bookapi/plan/v` | 2 | `scene=10&from=video_ranklist&selected_items=all`；`selected_items=ai_playlet` |

**全部带 6 个签名头，且 `x-ss-stub` 不出现**（GET 无 body）。

### 11.1 完整签名请求样例（分类列表）

```http
GET https://reading.snssdk.com/reading/bookapi/bookmall/cell/change/v?ac=wifi&channel=update_64&aid=8662&app_name=novelread&version_code=73932&version_name=7.3.9.32&device_platform=android&os=android&ssmix=a&device_type=25053RT47C&device_brand=Redmi&language=zh&os_api=36&os_version=16&manifest_version_code=73932&resolution=1280*2772&dpi=520&update_version_code=73932&host_abi=arm64-v8a&dragon_device_type=phone&pv_player=73932&compliance_status=0&need_personal_recommend=1&player_so_load=1&is_android_pad_screen=0&iid=3330900592484115&device_id=3330900592480019&cdid=956552f6-2f1a-485d-b0e3-fc529e4faaa7&cell_id=6952850996422770718&tab_type=8&selected_items=playlet&sub_selected_items=&unlimited_selector_change_type=2&client_template=2&client_req_type=2&change_type=1&offset=0&limit=40&ts=1794877906&_rticket=1794877906928

User-Agent: com.phoenix.read/71332 (Linux; U; Android 16; zh_CN; 25053RT47C; Build/BP2A.250605.031.A3; Cronet/TTNetVersion:04657795 2026-01-23 QuicVersion:c67e9834 2025-09-08)
Accept: application/json; charset=utf-8,application/x-protobuf
x-xs-from-web: 0
x-ss-req-ticket: 1794877906928
x-tt-request-tag: t=0;n=0
sdk-version: 2
passport-sdk-version: 50561
x-vc-bdturing-sdk-version: 3.7.2.cn
x-gorgon: <48 hex>
x-khronos: 1794877906
x-ladon: <base64>
x-argus: <base64>
x-helios: <base64>
x-medusa: <base64>
x-tt-dt:
lc: 101
```

> query 中固定参数（`ac`/`aid`/`app_name`/`version_*`/`device_*`/`resolution` …）由网关统一附加；
> 每次变化的是 `iid` / `device_id` / `cdid` / `ts` / `_rticket` 以及业务参数（`cell_id`/`selected_items`/`offset`/`limit`/`query`）。
> 这 5 个变化项**都参与签名**。

### 11.2 搜索联想实测返回

```json
{ "items": ["重生后，我成了顶流", "重生只剩半小时", "重生1988：重回巅峰", "……"] }
```

---

## 12. 客户端接入流程

### 12.1 找片 → 播放的完整链路

```
① GET  /api/v1/catalog?category=real-drama&page=1
        → 剧目卡片列表（SeriesSummary[]）
        → 客户端渲染分类页；点进某剧

② POST /api/v1/series  { "series_id": "7597479073606028313" }
        → 剧名/封面/简介 + 分集表 episodes[]
        → 客户端渲染选集页；缓存 episodes[].vid

③ POST /api/v1/resolve { "vid": "7701680675979545625" }
        → play_url + play_headers + decryption_key
        → 交给播放器（详见 02-client-api.md §6.4）

④ GET  /api/v1/danmaku?danmaku_id=hongguo:{series_id}:{vid}&start_ms=0
        → 弹幕片段，循环拉取（next_ms 作为下一次 start_ms）
```

### 12.2 TypeScript 示例

```ts
const GATEWAY = 'https://<your-gateway-host>';
const TOKEN = '<token>';

async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${GATEWAY}${path}`, {
    ...init,
    headers: {
      Authorization: `Bearer ${TOKEN}`,
      'Content-Type': 'application/json',
      ...(init?.headers ?? {}),
    },
  });
  const body = await res.json();
  if (body.code !== 0) throw new Error(`${body.code}: ${body.message}`);
  return body.data as T;
}

export interface SeriesSummary {
  series_id: string;
  title: string;
  cover: string;
  episode_count: number;
  remark: string;
  intro: string;
  tags: string[];
  year: string;
}

export const catalog = (category = 'real-drama', page = 1) =>
  api<{ page: number; page_count: number; total: number; list: SeriesSummary[] }>(
    `/api/v1/catalog?category=${encodeURIComponent(category)}&page=${page}`);

export const series = (seriesId: string) =>
  api<{ series_id: string; title: string; episodes: Array<{ no: number; vid: string; title: string }> }>(
    '/api/v1/series', { method: 'POST', body: JSON.stringify({ series_id: seriesId }) });

export const search = (q: string, page = 1) =>
  api<{ query: string; page: number; has_more: boolean; total: number; list: SeriesSummary[] }>(
    `/api/v1/search?q=${encodeURIComponent(q)}&page=${page}`);
```

### 12.3 翻页要点（最容易踩的坑）

```ts
// ✅ 顺序翻页，保留游标链
let page = 1;
while (true) {
  const { list, has_more } = await catalog('real-drama', page);
  render(list);
  if (!has_more || list.length === 0) break;
  page += 1;
}

// ❌ 直接跳到第 5 页 → 40001（游标链不存在）
await catalog('real-drama', 5);
```

---

## 13. 与 `resolve` 的衔接

发现类接口产出的 `series_id` / `vid` 就是 `resolve` 的输入，链路无缝：

| 阶段 | 产物 | 去向 |
|---|---|---|
| `catalog` | `series_id` | → `series` |
| `series` | `episodes[].vid` | → `resolve`（也可持久化复用） |
| `resolve` | `danmaku_id` | → `danmaku` |
| `resolve` | `play_url` + `decryption_key` | → 播放器（**不过网关**） |

**不需要客户端做中间转换**，所有 id 在同一个命名空间里。

---

## 14. 规划中的发现能力（上游已支持，网关尚未实现）

> **状态标注**：以下端点**目前网关未暴露**。此处给出完整规格，供客户端提前设计、供服务端实现时直接落地。
> 已实现端点见 §2–§7。

### 14.1 端点总览（规划）

| 规划端点 | 上游（红果） | 参考实现入口 |
|---|---|---|
| `GET /api/v1/suggestions?q=` | `search/suggest/v`（有词）/ `search/cue/v`（无词） | `spider.suggestions` |
| `GET /api/v1/rankings/boards` | `plan/v`（`from=video_ranklist`） | `spider.rankingBoards` |
| `GET /api/v1/rankings?board=&page=` | `plan/v` | `spider.rankings` |
| `GET /api/v1/recommendations?genre=&page=` | `bookmall/tab/v`（`tab_type=38`/`32`） | `spider.recommendations` |
| `GET /api/v1/feed?page=&scope=` | `bookmall/cell/change/v` | `spider.feed` |
| `GET /api/v1/series/:id/seasons` | `plan/v`（`from=single_col_same_ip_series`） | `spider.seriesSeasons` |

### 14.2 搜索联想 / 热门词

**上游**

| 场景 | 端点 | 参数 |
|---|---|---|
| 有关键词 | `GET /reading/bookapi/search/suggest/v` | `query=<kw>&count=10` |
| 空关键词 | `GET /reading/bookapi/search/cue/v` | `cue_type=0&limit=10` |

共同固定参数：`bookstore_tab=16`、`tab_name=feed`、`search_source=1`、`user_is_login=0|1`。

**参考实现的取值口径**

- 有词：读 `data.query_result`（字符串数组）；为空则退回 `data.query_result_v2[].name`。
- 无词：读 `data_v1[].text`（或 `display_text`）；为空则退回 `data.text`。
- 去重、过滤长度 >100、截断到 10 条。

**规划响应**

```json
{ "items": ["重生后，我成了顶流", "重生1988：重回巅峰"] }
```

**缓存**：15 分钟（与参考实现一致）。

### 14.3 榜单

**上游**：`GET /reading/bookapi/plan/v`

```
scene=10 & from=video_ranklist & bookstore_tab_type=26
selected_items=<group> & sub_selected_items=<board>
panel_selected_items= & background_selected_items=
```

**榜单列表**（`spider.rankingBoards`）

从 `data` 的 cell 树里提取：
- `group.selector_item_id` 只接受 `all` / `human` / `comic_series_rank` / `ai_playlet`
- `board.selector_item_id` 必须匹配 `^[a-z0-9_]{1,80}$`

**规划响应**

```json
{
  "items": [
    { "id": "hongguo-app:all:hot_sc", "name": "热播榜", "description": "…", "source": "hongguo" }
  ]
}
```

> `id` 是**不透明标识**，客户端不要解析其结构；翻页时原样回传给 `rankings` 端点。
> 榜单列表缓存 15 分钟。

**榜单内容**（`spider.rankings`）

- `board` 形如 `hongguo-app:{group}:{board}`
- 网关会校验上游返回的 `is_selected` 与请求一致，不一致 → `40001`（榜单配置已更新，客户端需刷新榜单列表）

**分页**：游标分页，TTL 2 小时（与 catalog 一致）。

### 14.4 分类型推荐

**上游**：`GET /reading/bookapi/bookmall/tab/v`

| 类别 | `tab_type` | `tab_index` |
|---|---|---|
| 真人短剧 | `38` | `1` |
| 漫剧 | `32` | `2` |
| AI 剧 | — | 走榜单 `ai_playlet_hot_sc` |

网关从 `data.tab_item[]` 里找 `tab_type` 匹配的那一项，再提取 `cell_data` 的剧目。

**分页**：游标分页，TTL 2 小时。

### 14.5 推荐流（feed）

**上游**：`GET /reading/bookapi/bookmall/cell/change/v`（带 `cell_id` + `offset` 游标）

**规划响应**

```json
{
  "page": 1,
  "has_more": true,
  "scope": "推荐流 scope 串",
  "list": [ /* SeriesSummary[] */ ]
}
```

`scope` 是客户端需要在下一次请求时回传的不透明串（内含上游游标）。客户端应把它持久化，以支持"回到应用后继续刷"。

**缓存**：2 小时（与参考实现一致）。

### 14.6 系列剧 / 番剧剧季

**上游**：`GET /reading/bookapi/plan/v`

```
scene=10 & from=single_col_same_ip_series & book_id=<series_id> & filter_ids=<series_id>
```

返回该系列下的全部剧集（含 `season_index`），最多翻 10 页。

**筛选规则（参考实现）**

| 条件 | 处理 |
|---|---|
| `series_id` 必须匹配 `^[1-9]\d{9,29}$` | 否则 `INVALID_SERIES_ID` |
| `season_index` ∈ [1, 200] | 超出则丢弃 |
| `disable_play === true` | 标记为「暂不可播放」，保留在列表 |
| `episode_cnt === 0 && subscribe_item` | 标记为「待上线」 |
| `title` 为空 | 丢弃 |

**规划响应**

```json
{
  "list": [
    { "series_id": "…", "title": "第2季", "cover": "…", "episode_count": 24,
      "remark": "24集", "season": 2, "playable": true, "status": "" }
  ]
}
```

`status` 取值：`""`（正常）/ `待上线` / `暂不可播放`。

### 14.7 账号体系（不在网关范围）

红果的账号相关接口（`/reading/user/info/v1/`、`bookshelf/video/list/v1/`、`read_progress/*`、`ugc/action/set/v1/` 等）需要**已登录用户的 Cookie**，与"网关代解析直链"是两种不同的鉴权模型。

参考实现把它们放在 `spider.accountRequest` / `spider.accountParse` 里，客户端侧对应登录页（`HongguoPhoneLoginScreen` / `HongguoQrLoginScreen`）。

**建议**：账号能力做在**客户端**（登录态由客户端持有），网关不代理账号请求。若确需服务端代持登录态，需要单独设计凭据存储方案，并明确其风险边界。

---

## 15. 扩展方式

新增发现能力时：

1. `internal/upstream`：封装上游端点（`FetchDiscoveryRaw` 已有通用实现，新增路径需加进白名单）；
2. `internal/service`：实现对应的 service，复用现有的游标分页与缓存模式；
3. `internal/api`：注册路由；
4. 文档：在本文件补一节（端点契约 + 上游参数 + 字段映射）。

白名单位置：`internal/upstream/hongguo.go` 的 `FetchDiscoveryRaw` 只接受：

```
/reading/bookapi/search/cue/v
/reading/bookapi/search/suggest/v
/reading/bookapi/plan/v
/reading/bookapi/bookmall/tab/v
/reading/bookapi/bookmall/cell/change/v
```

新增上游路径必须显式加入白名单，否则抛 `SECURITY_BLOCKED`。

---

## 15. 自检清单

- [ ] 分类页能正常翻页，没有出现 `40001` 游标过期
- [ ] 搜索从第 1 页顺序翻页
- [ ] 剧目卡片有图有标题有集数，`tags` 不是 `null`
- [ ] `series` 能拿到与总集数一致的 `episodes` 长度
- [ ] 客户端缓存了 `episodes[].vid`
- [ ] 弹幕按 `next_ms` 连续拉取，没有重叠或跳段
- [ ] 收藏/历史等本地功能按 `series_id` 存储（跨平台一致）
