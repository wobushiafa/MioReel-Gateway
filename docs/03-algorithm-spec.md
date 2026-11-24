# 算法规范（Go 实现依据）

> 服务端团队实现 `signer` / `resolver` 两个包时的唯一依据。
> 每条规则都标注了参考实现（`tools/golden/reference-hongguo.js`）的**行号**，可直接对照翻译。
>
> 全文约定：`REF` = `tools/golden/reference-hongguo.js`（2915 行）。

---

## 0. 必读：三条铁律

### 铁律 1 —— 32 位无符号溢出语义

JS 里所有位运算都是 **int32 截断**，移植到 Go 必须显式用 `uint32`：

| JS | Go | 备注 |
|---|---|---|
| `x >>> 0` | `uint32(x)` | 转无符号 |
| `~x` | `^uint32(x)` | Go 的 `^` 对 uint32 才是按位取反 |
| `x << 3` | `x << 3`（uint32 自动截断） | Go 对 uint32 左移本就溢出，**天然正确** |
| `x >> 3`（算术） | `x >> 3`（uint32 是逻辑右移） | **JS 的 `>>` 是算术右移，符号位会补 1 —— 必须区分！** |

> 参考实现里 `M(r) = Number(r)>>>0` 就是显式转 uint32；`re`/`ne` 是循环右移/左移；`ft`/`st`/`sn` 是 64 位拆成 `[hi, lo]` 对。
>
> **最危险的坑**：JS `>>>` 作用于 int32（先转 int32 再逻辑右移），Go 里对 `uint32` 用 `>>` 是对的，但对 `int32`/有符号类型就错。统一约定：**所有参与位运算的量一律 `uint32`**，需要 64 位时用 `[2]uint32{hi, lo}` 对（参考实现里叫 `word64`）。

### 铁律 2 —— 摘要算法有三套，不能只实现一套

`components()`（REF L960）按 `selector` 分派：

```js
const selector = (pComponents.selector(q, bodyHex, epoch) >>> 0) & 15;   // ∈ [0,15]

if (selector % 3 === 2)      head = md5Variant(selector, q, bodyHex, epoch);
else if (selector % 3 === 0) head = shaVariant(selector, q, bodyHex, epoch);
else                         head = thirdHead(selector, q‖body‖time, epoch);
```

`selector ∈ [0,15]` 的分布：

| selector | 分支 | 占比 |
|---|---|---|
| 0, 3, 6, 9, 12, 15 | `PublicSha`（改造版 SHA-1） | 6/16 |
| 2, 5, 8, 11, 14 | `PublicMd5Zi`（改造版 MD5） | 5/16 |
| 1, 4, 7, 10, 13 | `thirdHead`（自定义 64 轮状态机） | 5/16 |

> `third` 常量表（REF L819）**恰好只含 selector 1/4/7/10/13 五条 profile**，与上表第三行严格对应。

**selector 由请求内容动态决定**，所以三套必须全部实现，缺一必错。

### 铁律 3 —— 摘要不能用标准库替代

| 算法 | 参考位置 | 能否用标准库 |
|---|---|---|
| `PublicMd5Zi` | REF L816 | ❌ **改造版 MD5**：IV 由 selector 决定（非标准 `67452301…`），尾部拼 `a0010000`，轮函数也被改写 |
| `PublicSha` | REF L817 | ❌ **改造版 SHA-1**：轮数与常量均不同 |
| `thirdHead` | REF L906 | ❌ 自定义 64 轮 / 8×64 位状态机 + 8 张 256 项查表 |
| `cipher`/`block`/`expand` | REF L942 / L932 / L928 | ❌ 自定义分组密码（SM4 族，48 字节轮密钥扩展） |
| `PublicProfileHelios` | REF L967 | ❌ 34 轮 SM4 风格扩散 + 自定义 16 字节块函数 |
| `Ti`（`x-ss-stub` = MD5(body).upper） | REF L91266 | ✅ 标准 MD5 |
| `PublicProfileGorgon` 内的 MD5(query)/MD5(body) | REF L968 | ✅ 标准 MD5 |
| `io()` 内的 SHA-512 | REF L1008 | ✅ 标准 SHA-512 |
| `createConfigurationDecoder` 的 MD5 + AES-128-CBC | REF L867 | ✅ 标准库（仅本地 APK profile 路径需要） |

**`PublicMd5Zi` / `PublicSha` / `thirdHead` / `cipher` / `PublicProfileHelios` 必须逐行对照 REF 翻译，不得凭算法名猜测实现。** 这五处是整个移植的工作量主体。

---

## 1. 术语表

| 名词 | 含义 | REF 位置 |
|---|---|---|
| **device** | 设备三元组 `iid` / `device_id` / `cdid` | L1120 |
| **epoch** | Unix 秒时间戳，参与全部签名 | — |
| **profile** | 签名配置 `{id, material}`，material 固定 32 字节 | L1000 |
| **selector** | 0..15，决定用哪套摘要 | L818 |
| **digest** | `{queryHashHex, selector, mixedDigestHex}` | L960 |
| **proto** | protobuf 风格二进制请求描述 | L994 |
| **packet** | `x-medusa` 的载荷 | L943 |
| **model** | 上游返回的 `video_model`（含 `video_list`） | L1040 |
| **variant** | 一条可用播放流（url + contentKey + 画质） | L1119 |

---

## 2. 签名材料（profile）

```js
// REF L1000
const builtInConfiguration = {
  id: "NjQ2NjM3Nzk0",                                       // base64 → "646637794"（9 字节 ASCII）
  material: "sjwqRwog0QXcP4xEOPgnLtRtbIn/3N7dUQhpjSbpSJ0="  // base64 → 32 字节
};
```

- `id` 解 base64 后为 **9 字节 ASCII** `"646637794"`（protobuf field6 使用）
- `material` 解 base64 后为 **32 字节**（protobuf field8 使用，签名密钥派生）
- `sdkVersion` 固定 `"v04.10.03-ml-android"`（REF L1001；不匹配会抛 `LOCAL_PROFILE_UNSUPPORTED`）

**材料会轮换**。服务端应支持外部下发（配置表 / KV / 环境变量），并保留内置值作为兜底。

> 另有本地 APK 提取路径：`profileConfiguration()`（REF L973）→ `createConfigurationDecoder()`（REF L867）用 **AES-128-CBC + RC4 双层** 解出 `sign_key`。仅在需要时实现。

---

## 3. Step 1 — 设备会话

```
POST https://log.snssdk.com/service/2/device_register/?aid=8662&app_name=novelread
Content-Type: application/json; charset=utf-8
User-Agent: <APP_UA>

{
  "magic_tag": "ss_app_log",
  "header": {
    "aid": 8662, "app_name": "novelread", "package": "com.phoenix.read",
    "app_version": "7.3.9.32", "version_code": 73932, "update_version_code": 73932,
    "manifest_version_code": 73932, "sdk_version": "6.18.0",
    "device_platform": "android", "os": "Android", "os_version": "…", "os_api": 36,
    "device_model": "25053RT47C", "device_brand": "Redmi", "device_manufacturer": "Xiaomi",
    "density_dpi": 520, "display_density": "mdpi", "resolution": "1280x2772",
    "language": "zh", "timezone": 8, "access": "wifi", "channel": "update_64",
    "cdid": "<uuid4>", "openudid": "<16 hex>"
  },
  "_gen_time": <ms>
}
```

响应取：
- `install_id_str` → **`iid`**
- `device_id_str` → **`device_id`**
- `cdid` → 客户端自造 UUID（REF L1120 的 `ao()`，16 字节随机 → UUID v4 格式）

**校验**：`iid` 与 `device_id` 必须匹配 `^\d{10,24}$`，否则抛 `GUEST_REGISTRATION_FAILED`。

> 参考实现把 device 缓存在 `globalThis.__hongguoCachedDevice`（进程内），并通过宿主接口 `__native_save_device(json)` 落盘。Go 侧对应：内存缓存 + 持久化（文件/DB）。

---

## 4. Step 2 — 查询串组装

```
query = ts=<unix秒> & _rticket=<unix毫秒> & iid=… & device_id=… & cdid=… & aid=8662 & …
```

- `ts` / `_rticket` **同时**进 query，并且**都参与签名**（`x-gorgon` 覆盖整个 query 串，`x-medusa` 覆盖 query 的 MD5）
- `Ni(params)`（REF L91266）的序列化规则：
  ```js
  encodeURIComponent(v).replace(/[!'()]/g, c => '%' + c.charCodeAt(0).toString(16).toUpperCase())
  ```
  → Go 用 `url.QueryEscape` 后**再修正** `!` `'` `(` `)` 四个字符为大写 %XX（`url.QueryEscape` 不转义这四个，正好相反）
- 参数顺序 = map 迭代顺序。**签名绑定的是最终字符串本身**，只要最终串一致，顺序不必与参考实现完全相同（但保持一致更安全）

**自校验循环**（REF `Ht`）：

```js
for (let x = 0; x < 32; x++) {
  s = c + x;                       // _rticket 递增
  f = `${e}?${u}`;                  // 完整 URL
  if (to(f, a, o) !== 1) break;     // to() 是 url/body/时间戳 的一致性校验
}
```

→ Go 侧同样要有一个"URL 与签名输入必须一致"的守卫函数。

---

## 5. Step 3 — 摘要核心 `components()`

REF **L960**。输入：`queryHex`（query 串的 UTF-8 字节 hex）、`bodyHex`（body 的 MD5 hex；无 body 时为 16 个 0）、`epoch`。

### 5.1 queryHashHex

```js
pComponents.hash = r => ht(S.from(r, 'hex')).toString('hex')
```

`ht` = 标准 SHA-256（CryptoJS，REF L814）→ **`queryHashHex` = SHA256(query 原始字节)**。

### 5.2 selector

```js
// REF L818
selector: (q, b, t) => Ce(Ce(Ce(539167016, S.from(q,'hex')), S.from(b,'hex')), Z(t)) >>> 0
```

- `Ce(acc, bytes)`（REF L91266）是一个字节吸收的哈希：
  ```js
  function Ce(r, e) {
    let t = M(r);
    for (let n = 0; n < e.length; n++)
      (n & 1) === 0 ? t = M(t >>> 4 ^ t ^ t << 6 ^ e[n])
                    : t = M(~(t >>> 7 ^ t ^ (e[n] | t << 12)));
    return t;
  }
  ```
  → Go：`func ce(acc uint32, b []byte) uint32`，注意 `t >>> 4 ^ t ^ t << 6 ^ e[n]` 的**优先级**（`^` 左结合）和 `~(...)` 的按位取反。
- `Z(t)` = 4 字节**小端** epoch（REF L91266）
- 最终 `& 15`

### 5.3 三条摘要分支

**分支 A — `selector % 3 === 2` → `PublicMd5Zi`（REF L816）**

签名：`PublicMd5Zi(r, e, t, n, i)`，其中 `r`=queryHex 字节、`e`=bodyHashHex 字节、`t`=4 字节小端 epoch、`n`=selector、`i`=epoch。

> **本函数必须逐行对照 REF L816 翻译。** 其 IV 由 `a=(r&13)*86` 索引到常量数组 `[2560851803, 3874480130, 3239760911, 3714875250, 2728712785]`，尾部追加 `S.from("a0010000","hex")` —— **与标准 MD5 不同**。

**分支 B — `selector % 3 === 0` → `PublicSha`（REF L817）**

签名：`PublicSha(r, e, t, n, i, selector)`。

> **同样必须逐行对照 REF L817 翻译。** 其 `u` 常量数组按 `selector/3` 选取，与标准 SHA-1 不同。

**分支 C — `selector % 3 === 1` → `thirdHead`（REF L906）**

```js
function thirdHead(selector, data, epoch) {
  const profile = third.find(r => r.selector === selector);
  const C = profile.publicScalar;
  const T = tables.get(selector);                     // 8 张 256 项表，由 tableData 展开
  let h = Array(8).fill(ror(word64(epoch), (epoch + C - 1) & 63));
  const rc = literalRows.map(v => ror(v, (epoch + C) & 63));
  const padded = S.concat([data, S.from([128]), S.alloc(43), S.alloc(32)]);
  padded[126] = 1; padded[127] = 160;
  const transform = v => Array.from({length:8}, (_, i) =>
    T.reduce((out, t, j) => wordXor(out, t[wordByte(v[(i-j)&7], 56-8*j)]), [0,0]));
  for (let index = 0; index < 2; index++) {
    const block = Array.from({length:8}, (_, i) => bigRead(padded, index*64 + i*8));
    let key = index ? profile.paddedBlockKeyHashPermutation.map(i => h[i]) : h.slice();
    let state = index ? key.slice()
                      : key.map((v, i) => wordXor(v, block[profile.firstBlockInputPermutation[i]]));
    if (index) state[profile.paddedBlockLengthXorCanonicalIndex] =
                  wordXor(state[...], [0, 416]);
    for (let i = 1; i <= 10; i++) {
      key = transform(key);
      key[0] = wordXor(key[0], rc[i]);
      state = transform(state).map((v, j) => wordXor(v, key[j]));
    }
    h = h.map((v, i) => wordXor(wordXor(v, state[i]), block[(i+1)&7]));
  }
  const packed = S.from(h.slice().reverse().flatMap(v => [8,24,40,48].map(s => wordByte(v, s))));
  return S.from(Array.from({length:16}, (_, i) => packed[i] ^ packed[i+16]));
}
```

> `third`（5 条 profile）、`tableData`（8 张表）、`literalRows`、`algorithm` 全部在 **REF L819 这一行（172 KB）**，Go 侧应把它们**原样移植成常量**（可用 `go:embed` 内嵌一个 JSON/二进制常量文件）。
>
> `word64` / `ror` / `wordXor` / `wordByte` / `bigRead` 见 REF L887–L899。

### 5.4 校验和 `rawChecksum`

```js
function rawChecksum(data, seed = 0, count = data.length) {
  let e = seed >>> 0;
  for (let i = 0; i < count; i++) {
    const n = (i & 1) ? e >>> 5 ^ e : e >>> 3 ^ e;
    const v = (i & 1) ? ~(data[i] | e << 11) : data[i] ^ e << 7;
    e = (v ^ n) >>> 0;
  }
  return e;
}
```

### 5.5 组装 digest

```js
let sum = rawChecksum(head, 539100192, normalJni ? 12 : 8);
sum = normalJni ? ((sum | 4 | 32) ^ 16777216) >>> 0 : ((sum & ~4 & ~32) ^ 16777216) >>> 0;
const check = S.alloc(4); check.writeUInt32LE(sum);
const digest = S.concat([head, check]);        // head 16B + check 4B = 20B
```

**`normalJni` 双路径**：先按 `normalJni=false` 算，若与 proto 里的 field13 不符，则改用 `normalJni=true` 重算（REF L947-948）。

> 注意 `~4` / `~32` 在 Go 中是 `^uint32(4)` / `^uint32(32)`。

### 5.6 派生流密钥（仅 `packet` 需要）

```js
if (materialHex !== undefined) {
  const material = S.from(materialHex, 'hex');
  const seed = pComponents.zi(materialHex, nonce);
  result = { ...result,
    prefixFlag: rawChecksum(material) & 255,
    streamSeedHex: seed[0],
    streamKeyHex:  seed[1] };
  if (protoHex !== undefined) result.xiHex = pComponents.xi(protoHex, seed[0]);
}
```

---

## 6. Step 4 — `packet()`：生成 `x-medusa`

REF **L943**。输入：`protoHex`、`materialHex`、`randoms[3]`、`queryHex`、`bodyHashHex`、`epoch`。

### 6.1 protobuf 风格编解码

- `decodeFields`（REF L920）：读 tag（varint），`field = tag>>>3`，`wire = tag&7`；wire 0 = varint，wire 2 = 长度前缀字节串
- `readWordVarint`（REF L899）：**64 位 varint**，拆成 `[hi, lo]` 对
- 写入侧 `profileInteger(field, value)`（REF L971）/ `profileBytes(field, value)`（REF L972）：
  - `profileInteger` 用 **zigzag** 编码（`Li`：负数 `-n*2-1`，非负 `n*2`），tag = `field*8`
  - `profileBytes` tag = `field*8+2`，后跟长度 varint + 原始字节

### 6.2 proto 的字段布局

（REF L994，由 `produce()` 组装）

| field | wire | 值 |
|---|---|---|
| 1 | bytes | `2d4b4fca49750d433fb5ae2c226dcc56`（16 字节 magic） |
| 2 | int | `5` |
| 3 | int | `randoms[0]` |
| 4 | bytes | `"8662"` |
| 5 | bytes | `device_id` |
| 6 | bytes | `profile.id` |
| 8 | bytes | `sdkVersion` = `"v04.10.03-ml-android"` |
| 9 | int | `67764992` |
| 10 | bytes | `S.alloc(8)`（8 字节零，作为 prefix） |
| 12 | int | `epoch` |
| 13 | bytes | `digest.mixedDigestHex`（20 字节） |
| 14 | bytes | `queryHashHex` 前 6 字节 |
| 15 | bytes | `state`（见下） |
| 17 | int | `epoch` |

`state`（REF L993）= `profileInteger(1, counter) ‖ profileInteger(2, 694367) ‖ profileInteger(3, 694367) ‖ profileInteger(5, 694367)`

`counter` 是**进程内自增**（`++sequence`，REF L991），范围 `[1, 2147483647]`。

### 6.3 绑定校验（防篡改，必须实现）

```js
if (![32,64].includes(material.length) || randoms.length !== 3 || …) throw Error('V5_INPUT_SHAPE');
if (!wordEquals(fields.get(2),  word64(10)) ||
    !wordEquals(fields.get(3),  word64(randoms[0]*2)) ||
    !wordEquals(fields.get(12), word64(epoch*2)))   throw Error('V5_INPUT_BINDING');

let digest = components({queryHex, bodyHashHex, epoch});
let normalJni = false;
if (digest.mixedDigestHex !== fields.get(13).toString('hex')) {
  normalJni = true;
  digest = components({queryHex, bodyHashHex, epoch, normalJni});
}
if (digest.mixedDigestHex !== fields.get(13).toString('hex') ||
    digest.queryHashHex.slice(0,12) !== fields.get(14).toString('hex'))
  throw Error('V5_DIGEST_BINDING');
```

> 这是**自校验**：proto 里存的摘要必须能被重算命中。Go 侧同样要保留 —— 它是发现移植错误的最快信号。
> 注意 field3 的绑定是 `randoms[0]*2`（zigzag 编码的 `randoms[0]`）。

### 6.4 载荷组装

```js
const nonce = randoms[1];
const stream = components({queryHex, bodyHashHex, epoch, normalJni, protoHex, materialHex, nonce});
const prefix = fields.get(10);                       // 8 字节零
const seed   = S.from(stream.streamKeyHex, 'hex');
const body   = S.concat([prefix, S.from(stream.xiHex,'hex')]).reverse();
for (let i = 0; i < body.length; i++) body[i] ^= seed[~i & 3];   // 反向 4 字节循环异或

const q = S.from(digest.queryHashHex,'hex'), d = S.from(digest.mixedDigestHex,'hex');
const flags = (q[0] & 63) << 14 | 402653185 | (d[0] & 63) << 8;
const head = S.alloc(9), tail = S.alloc(4);
head[0] = stream.prefixFlag;
head.writeUInt32LE(randoms[2] >>> 0, 1);
head.writeUInt32LE(flags >>> 0, 5);
tail.writeUInt32LE(nonce >>> 0);

const data = S.concat([head, body, tail.subarray(2)]);
const extracted = extract(data);                    // REF L926，位平面提取 → 31 字节
if (extracted.length !== 31) throw Error('V5_EXTRACTION_SIZE');

const md5 = b => K.createHash('md5').update(b).digest();
const coded = cipher(extracted, md5(material.subarray(0,16)), md5(material.subarray(16,32)),
                     algorithm.cipherProfiles[epoch & 3]);

const header = S.concat([S.from([5,0,0,0]), fields.get(1)]);   // field1 = 16 字节 magic
for (let i = 0; i < 20; i += 4) header.writeUInt32LE((header.readUInt32LE(i) ^ epoch) >>> 0, i);

const meta = S.alloc(4);
meta.writeUInt16LE(nonce & 65535);
meta[3] = coded.length - 31;

return S.concat([header, meta, coded.subarray(31), embed(data, coded)]).toString('base64');
```

**`cipher` 密钥**：`key = MD5(material[0:16])`、`iv = MD5(material[16:32])`。
**`cipher` 算法**：`algorithm.cipherProfiles[epoch & 3]`（4 个 profile 轮换，常量在 REF L819）。

`extract` / `embed`（REF L926-927）是**位平面**操作：`algorithm.planes` 描述若干平面，每个平面给出 `outputBitToSourceBit` 映射，把 8 字节字里的比特按映射重排。

---

## 7. Step 5 — 六个签名头

REF **L997**（`sign()`）：

```js
sign(input) {
  const randoms = Array.from({length:3}, () => K.randomBytes(4).readUInt32LE(0) & 2147483647);
  const p = produce({...input, randoms});
  return {
    'x-gorgon'  : PublicProfileGorgon(p.query, p.body, input.epoch, Math.floor(Math.random()*65536)),
    'x-khronos' : String(input.epoch),
    'x-ladon'   : gn(input.epoch).toString('base64'),
    'x-argus'   : Z(input.epoch).toString('base64'),
    'x-helios'  : PublicProfileHelios(input.epoch, configuration.id),
    'x-medusa'  : p.signature,
  };
}
```

| 头 | 实现 | 要点 |
|---|---|---|
| `x-gorgon` | `PublicProfileGorgon`（REF L968） | `MD5(query)[0:4] ‖ MD5(body)[0:4] ‖ 4B常量(67764992) ‖ BE时间戳` → `Wi` 风格的 RC4 置换（key=`[74,64,22,n>>8&255,71,108,1,n&255]`）+ 字节代换，前缀 `"8404"` → hex |
| `x-khronos` | — | epoch 秒字符串 |
| `x-ladon` | `gn`（REF L91266） | 4 字节**大端** epoch → base64 |
| `x-argus` | `Z`（REF L91266） | 4 字节**小端** epoch → base64 |
| `x-helios` | `PublicProfileHelios`（REF L967） | `MD5(Z(rand)‖"8662")` 派生 IV → 34 轮 SM4 风格扩散 → AES 分组 → base64 |
| `x-medusa` | `packet()` | 见 §6 |

**随机数消费顺序（移植时必须一致，否则同输入不同输出）**

1. `produce()` 内：`K.randomBytes(4)` × **3** → `randoms[0..2]`（顺序消耗）
2. 之后 `PublicProfileGorgon` 内：`Math.floor(Math.random()*65536)` × **1**
3. 再之后 `PublicProfileHelios` 内：`Math.floor(Math.random()*4294967296)` × **1**

---

## 8. Step 6 — 组装与发送请求

REF `Ht`（L1008）+ `Pe`（L1008）。

### 8.1 请求头全集

```
User-Agent: <APP_UA>            // com.phoenix.read/71332 (Linux; U; Android 16; zh_CN; 25053RT47C; …)
Accept: application/json; charset=utf-8,application/x-protobuf
Content-Type: application/json; charset=UTF-8
x-xs-from-web: 0
x-ss-req-ticket: <ms>
x-tt-request-tag: t=0;n=0
sdk-version: 2
passport-sdk-version: 50561
x-vc-bdturing-sdk-version: 3.7.2.cn
x-ss-stub: <MD5(body).toUpperCase()>     // 仅当 body 非空
x-gorgon / x-khronos / x-ladon / x-argus / x-helios / x-medusa: <签名>
```

> `x-ss-stub` 在 body 为空时**必须删除**（REF L1008：`a || delete d["x-ss-stub"]`）。

### 8.2 传输层约束（`Pe`）

- `redirect: "manual"` —— **3xx 必须原样交回**，不能自动跟随
- 域名白名单：`fqnovel.com` / `snssdk.com`（含子域），否则 `SECURITY_BLOCKED`
- 最多 4 次重定向；POST 遇 3xx 直接 `SECURITY_BLOCKED`
- 响应体大小上限：普通接口 2 MB；`search/tab`、`bookmall/tab|cell/change`、`video_detail` 为 12 MB
- 非 200 → `HTTP_<code>`；空体 → `EMPTY_RESPONSE`；解析失败 → `INVALID_RESPONSE`

---

## 9. Step 7 — `video_model` 请求与路由降级

REF **L1011–L1055**。

```js
const appModelRoutes = [
  { url: 'https://api5-normal-sinfonlineb.fqnovel.com/novel/player/multi_video_model/v1/', single: false },
  { url: 'https://reading.snssdk.com/novel/player/multi_video_model/v1/',                      single: false },
  { url: 'https://reading.snssdk.com/novel/player/video_model/v1/',                          single: true  },
  { url: 'https://api5-normal.fqnovel.com/novel/player/multi_video_model/v1/',               single: false },
];
```

- 首选路由会**粘滞 5 分钟**（`preferredAppModelUntil = now + 5min`）
- body：

```js
const biz = {
  detail_page_version: 0, device_level: 3, disable_digg_stat: false,
  need_all_video_definition: true, need_mp4_align: false,
  use_os_player: false, use_server_dns: false, video_platform: 1024
};
// single 路由
{ video_id: videoId, content_type: 1, biz_param: biz }
// multi 路由
{ biz_param: biz, mixed_video_id_map: { 1004: [videoId] } }
```

- 解析：`route.single ? response.data : response.data[videoId]`，再取 `.video_model`（**可能是字符串**，需再 `JSON.parse`；`Lt` 会给 16 位以上裸数字 id 补引号，REF L91266）
- 过期时间：`appModelExpiry`（REF L1017）取 `expire_time` / `url_expire` / `__hongguoExpiresAt` 的最小值

**可恢复错误**（触发换路由）：`MISSING_MODEL`、`EMPTY_RESPONSE`、`RESPONSE_NOT_UTF8`、`INVALID_RESPONSE`、`HTTP_0/404/405/408/500/502/503/504`、`ETIMEDOUT`、`DNS`、`TIMEOUT`、`CONNECTION`
**不可恢复**（直接失败）：`BUSINESS_REJECTED`、`SECURITY_BLOCKED`、`CANCELLED`

---

## 10. Step 8 — 提流 `ln()`

REF **L1107–L1119**。输入 `model`、`videoId`、目标清晰度。

### 10.1 筛选与排序

```js
const entries = Object.values(model?.video_list || {})
  .filter(item => item && (item.main_url || item.backup_url || Array.isArray(item.backup_url_list)))
  .map(item => {
    const meta = item.video_meta || {};
    const codec = String(item.codec_type || meta.codec_type || '').toLowerCase();
    const codecRank = ['h264','avc','avc1'].includes(codec) ? 0
      : ['hevc','h265','hvc1','hev1','bytevc1'].includes(codec) ? 1
      : (!codec && ['mp4','mpeg'].includes(String(item.vtype).toLowerCase())) ? 2 : 3;
    // 分辨率四个来源，按序取第一个全为正的
    const [width, height] = [[item.vwidth,item.vheight],[item.width,item.height],
                             [meta.vwidth,meta.vheight],[meta.width,meta.height]]
      .map(p => p.map(Number))
      .find(p => p.every(v => Number.isFinite(v) && v > 0)) || [0,0];
    const label = String(item.quality_desc || item.definition || meta.definition || '');
    const pixels = (width > 0 && height > 0) ? Math.min(width, height) : Number(label.match(/\d+/)?.[0] || 0);
    return { item, codec, codecRank, pixels, quality: pixels > 0 ? pixels + 'p' : '', width, height };
  })
  .filter(v => v.codecRank < 3)                    // rank 3 直接丢弃
  .sort(downloadQuality === 'auto'
      ? (l, r) => r.pixels - l.pixels || l.codecRank - r.codecRank
      : (l, r) => Math.min(l.width,l.height) - Math.min(r.width,r.height) || l.codecRank - r.codecRank);
```

> 分辨率来源优先级：`vwidth/vheight` → `width/height` → `video_meta.vwidth/vheight` → `video_meta.width/height` → 从 `quality_desc`/`definition` 里抠数字。

### 10.2 地址三态解析（核心）

```js
for (const c of entries) {
  const spadeA = c.item.spade_a || c.item.encrypt_info?.spade_a;
  const contentKey = spadeA ? no(spadeA) : null;                 // ← 内容密钥
  for (const f of [c.item.main_url, c.item.backup_url, c.item.backup_url_1,
                   c.item.backup_url_2, c.item.backup_url_3,
                   ...(Array.isArray(c.item.backup_url_list) ? c.item.backup_url_list : [])]) {
    if (typeof f !== 'string' || !f) continue;
    const h = model.key_seed
      ? io(f, Buffer.from(model.key_seed, 'base64'))   // ① spade 密文
      : (/^https:\/\//i.test(cn(f)) ? cn(f)             // ② 明文 URL
                                          : Buffer.from(f,'base64').toString('utf8'));  // ③ base64
    const d = lt(h, ['qznovelvod.com','fqnovelvod.com','douyinvod.com']);
    out.push({ url: d, contentKey: contentKey?.toString('hex') ?? null,
               codec: c.codec === 'bytevc1' ? 'hevc' : (c.codec || 'auto'),
               source: 'app', vid: videoId, expiresAt: appModelExpiry(model),
               userAgent: APP_UA, referer: 'https://novelquickapp.com/',
               mediaBytes: Math.max(0, Number(c.item.size || c.item.video_meta?.size || 0)),
               quality: c.quality, width: c.width, height: c.height });
  }
}
```

**去重**：按 `url + ':' + contentKey` 去重。

### 10.3 `cn()` —— 转义还原（REF L91266）

```js
cn = s => String(s ?? '').trim()
          .replace(/\\\//g, '/')
          .replace(/\\u0026/g, '&')
          .replace(/&amp;/g, '&');
```

---

## 11. Step 9 — 两个解码器

### 11.1 `no(spade_a)` → 内容密钥（AES-128 key）

REF **L1008**。

```js
function no(r) {
  let e = Buffer.from(String(r).trim().replace(/-/g,'+').replace(/_/g,'/'), 'base64');
  if (e.length < 3) throw new Error('spade_a too short');
  let t = e.length - (e[0] ^ e[1] ^ e[2]) + 47;
  t = Math.min(t, e.length - 1);
  if (t < 33) throw new Error('spade_a key length invalid');
  let n = e.subarray(1, 1 + t);
  let i = 85, o = 246;
  for (let c = 0; c < t; c++) {
    const s = n[c];
    const u = (c & 1) ? i : o;
    if (c & 1) i = s; else o = s;
    const f = c.toString(2).split('1').length - 1;     // popcount(c)
    n[c] = (-21 - f + (u ^ s)) & 255;
  }
  const a = n.subarray(1, 33).toString('ascii');
  if (!/^[0-9a-f]{32}$/i.test(a)) throw new Error('App 视频密钥格式无效');
  return Buffer.from(a, 'hex');                          // ← 16 字节 AES-128 key
}
```

> Go 注意：`c.toString(2).split('1').length - 1` = **c 的二进制中 1 的个数**（popcount），用 `bits.OnesCount32(uint32(c))`。

### 11.2 `io(cipherText, keySeed)` → 明文 URL

REF **L1008**。

```js
function io(r, e) {                        // r = 密文(base64), e = key_seed 字节
  const n = Buffer.from(String(r).trim(), 'base64');
  if (n.length < 5) throw new Error('spade URL ciphertext too short');
  if (n[0] !== 168 || n[2] !== 1 || n[3] !== 0) throw new Error('spade URL header format error');
  const i = Math.floor((n.length - 4) / 16) * 16;
  if (!i) throw new Error('spade URL ciphertext has no complete block');
  const SALT = Buffer.from('4dd4c2e6b83162090e52b3c7a6733ba41cb2462b829ab58a196b39db57177524f' +
                           '49baf7f08e8d68d26a72e37c1a95a2f1f05a51892aef2949732b62a38aadd58','hex');
  const a = sha512(e);
  const c = sha512(Buffer.concat([a, SALT]));
  const d = createDecipheriv('aes-128-cbc', c.subarray(0,16), c.subarray(16,32));
  d.setAutoPadding(false);
  let u = Buffer.concat([d.update(n.subarray(4, 4 + i)), d.final()]);
  // 去 PKCS#7 padding
  const pad = u[u.length - 1];
  if (pad >= 1 && pad <= 16 && pad <= u.length) u = u.subarray(0, u.length - pad);
  while (u.length && u[u.length - 1] === 0) u = u.subarray(0, u.length - 1);
  return u.toString('utf8');
}
```

**派生链**：`key = SHA512(key_seed)`；`iv||key = SHA512(key_seed ‖ SALT)`，前 16 字节作 AES key、后 16 字节作 IV。

---

## 12. Step 10 — 域名收敛 `lt()`

REF **L1008**（另 REF L814 有一处同名，按行号取 L1008 这份）。

```js
function lt(url, allow) {
  const t = new URL(url);
  if (t.protocol !== 'https:' || t.username || t.password ||
      (t.port && t.port !== '443') ||
      !allow.some(n => t.hostname === n || t.hostname.endsWith('.' + n)))
    throw Object.assign(new Error('App 片源返回了不受支持的服务地址'), { code: 'SECURITY_BLOCKED' });
  return t.href;
}
```

允许域名：
- 上游 API：`fqnovel.com`、`snssdk.com`
- 媒体 CDN：`qznovelvod.com`、`fqnovelvod.com`、`douyinvod.com`

> **`lt` 返回的是 `t.href`（URL 规范化后的形式）**，不是原串 —— 路径中的转义可能被重新编码。Go 侧用 `net/url` 的 `String()` 近似即可。

---

## 13. Step 11 — 选流与直链产出

REF **L2057–L2186**（`spider.play` 覆写）。

### 13.1 入参解析

```js
const target = String(id || '').replace(/^hongguo:/, '');
const match = /\/player\/([^/?#]+)\/(\d+)(?:[/?#]|$)/.exec(target)
          || /^(\d+)_(\d{10,})$/.exec(target)
          || (publicMatch ? ['', seriesId, publicMatch[1]] : null);
```

`flags` 数组里的对象字段：`seriesId`、`quality`（目标短边像素）、`avoidUrl`（上次失败的 URL，本次避开）。

### 13.2 探活

```js
async function readable(v) {
  const res = await fetch(v.url, { method: 'HEAD', headers: mediaHeaders(v), timeoutMs: 5000 });
  if (![200,206].includes(res.status)) { mediaFailure = 'MEDIA_HTTP_' + res.status; return false; }
  const ct = res.headers.get('content-type');
  if (!/^video\//i.test(ct || '')) { mediaFailure = 'MEDIA_TYPE_INVALID'; return false; }
  return true;
}
```

`mediaHeaders`：**删除 `Referer`**，只保留 `User-Agent`。

### 13.3 排序与产出

按 `qualityRank`（贴近目标清晰度，同清晰度时 `avoidUrl` 排后）排序，取第一个通过 `readable` 的：

```js
return {
  parse: 0,
  url: candidate.url,
  header: mediaHeaders(candidate),
  decryptionKey: candidate.contentKey || '',
  mediaBytes: candidate.mediaBytes || 0,
  quality, qualities, expiresAt, episodeId: vid, danmakuId
};
```

> `expiresAt` = `min(上游 expire, now + 300000)`，即**最多 5 分钟**。

---

## 14. Go 落地建议

### 14.1 包划分

```
internal/signer/     // 签名引擎（纯函数 + 随机源注入）
    digest.go        // components / selector / rawChecksum / 三条摘要分支
    packet.go        // protobuf varint / extract / embed / cipher
    profile.go       // 配置（id/material/sdkVersion）+ 常量表
    sign.go          // 6 个头的产出
internal/resolver/   // 提流
    model.go         // video_model 解析
    extract.go       // ln()：筛选 / 排序 / 三态地址 / 去重
    decode.go        // no() / io() / cn() / lt()
internal/upstream/   // HTTP 客户端（超时 / 重定向手动 / 域名白名单）
internal/device/     // 设备注册与持久化
internal/api/        // 对外 HTTP 契约（见 docs/02）
```

### 14.2 随机源必须可注入

```go
type Rand interface {
    Uint32() uint32   // 供 randoms[0..2] 使用
    Uint16() uint16   // 供 x-gorgon 使用
    Uint32Big() uint32 // 供 x-helios 使用
}
```

**理由**：`x-gorgon` / `x-helios` / `packet` 都依赖随机数。生产用 `crypto/rand`，测试用**固定序列**（如 `math/rand.New(rand.NewSource(1))`）才能产出可比对的金样本。

> 注意随机消费**顺序**必须与参考实现一致（§7 末尾）。

### 14.3 32 位类型约定

```go
func u32(x uint64) uint32 { return uint32(x) }
func rotl32(x uint32, n uint) uint32 { return x<<n | x>>(32-n) }
func rotr32(x uint32, n uint) uint32 { return x>>n | x<<(32-n) }
```

所有 `^` 取反写 `^uint32(x)`（不是 `^x`）。

### 14.4 常量表外置

`third`（5 条 profile）、`tableData`（8 张 256 项表）、`literalRows`、`algorithm`（位平面 + 4 个 cipher profile）在参考实现里挤在 **REF L819 一行（172 KB）**。建议导出为独立文件用 `go:embed` 引入：

```go
//go:embed tables.json
var tablesJSON []byte
```

### 14.5 自校验是第一道防线

移植过程中，以下任一抛出即说明有字节序/移位错误：

- `V5_INPUT_SHAPE`
- `V5_INPUT_BINDING`
- `V5_DIGEST_BINDING`
- `V5_EXTRACTION_SIZE`
- `V5_PROTO_VARINT` / `V5_PROTO_TRUNCATED` / `V5_PROTO_LENGTH` / `V5_PROTO_WIRE`

---

## 15. 验证方法（必做）

### 15.1 三层比对

| 层 | 比对对象 | 判据 |
|---|---|---|
| ① 摘要层 | `components()` 的 `queryHashHex` / `selector` / `mixedDigestHex` | 逐字节相同（**完全确定性**，best first move） |
| ② 签名层 | 6 个头 + `proto` hex + `packet` base64 | 固定随机源后逐字节相同 |
| ③ 端到端 | `resolve` 返回的 `play_url` 结构 / `decryption_key` 长度 / `variants` 数量 | 一致 |

**① 是关键**：它只依赖 `(queryHex, bodyHex, epoch)`，完全确定，且覆盖了 selector 计算 + 三条摘要分支 + 校验和 —— 也就是最容易出错的部分。

### 15.2 参考实现的运行方式

`tools/golden/` 里已经有一个可运行的 Node 宿主沙箱：

```bash
node tools/golden/recorder.js tools/golden/out
```

它会跑完 `catalog → detail → resolve` 全流程并落盘：

- `out/wire-samples.json` —— 全部出站请求（URL / method / **完整签名头** / body）+ 解析结果
- `out/smoke.json` —— 最小可用清单（直链、UA、密钥、有效期）

Go 实现完成后，把同样的 `(query, body, epoch)` 喂进自己的 `components()`，与 `wire-samples.json` 里的签名头**逐字符比对**。

### 15.3 上游冒烟测试

```bash
node tools/golden/recorder.js tools/golden/out
# 用产出的直链 + 密钥验证解密播放
ffmpeg -hide_banner -loglevel error \
       -decryption_key <decryption_key> -user_agent "<UA>" \
       -i "<play_url>" -frames:v 3 -f null -
# 期望：exit 0 且无任何错误输出
```

> 对照组：去掉 `-decryption_key` 会得到 `Invalid NAL unit size` / `Error splitting the input into NAL units` —— 这是"密钥没生效"的典型症状。

---

## 16. 未在本文档展开的部分

以下功能在参考实现中存在，但不在网关首版范围内：

| 功能 | REF 位置 |
|---|---|
| 搜索 / 弹幕 / 排行榜 / 推荐 / feed | L2189+ |
| 账号体系（`profile` / `favorites` / `history` / 追剧进度 / 点赞关注） | L1121+ |
| 手机登录 / 二维码登录 / 验证码 | Dart 侧 `core/account/hongguo_*.dart` |
| 私有源 / 源开关 | Dart 侧 `views/sources/source_gate_dialog.dart` |
| 本地 APK 提取签名材料 | REF L973-984 |

需要时按同样的方式补充接口，并在 `docs/02-client-api.md` 增加对应端点。
