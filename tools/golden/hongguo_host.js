// MioReel 宿主环境模拟器：在 Node 中复现 Dart 侧注入的 JS 运行时接口，
// 以便独立复现 assets/sources/hongguo.js 的取流链路并拿到播放直链。
const fs = require('fs');
const vm = require('vm');
const nodeCrypto = require('crypto');

const SRC = './tools/golden/reference-hongguo.js';

function makeHostFetch(log) {
  return async function fetch(url, opts = {}) {
    const timeoutMs = Number(opts.timeoutMs) > 0 ? Number(opts.timeoutMs) : 20000;
    const ctrl = new AbortController();
    const timer = setTimeout(() => ctrl.abort(), timeoutMs);
    const init = { method: opts.method || 'GET', headers: opts.headers || {}, redirect: opts.redirect || 'follow' };
    if (opts.body != null && opts.body !== '' && init.method !== 'GET' && init.method !== 'HEAD') {
      init.body = typeof opts.body === 'string' ? opts.body : Buffer.from(opts.body);
    }
    try {
      const res = await globalThis.fetch(url, { ...init, signal: ctrl.signal });
      const buf = Buffer.from(await res.arrayBuffer());
      const text = () => Promise.resolve(buf.toString('utf8'));
      return {
        ok: res.status >= 200 && res.status < 300,
        status: res.status,
        statusText: res.statusText,
        url: res.url,
        headers: {
          get: (n) => res.headers.get(String(n).toLowerCase()),
          has: (n) => res.headers.has(String(n).toLowerCase()),
          keys: () => [...res.headers.keys()],
          entries: () => [...res.headers.entries()],
        },
        text,
        json: () => Promise.resolve(JSON.parse(buf.toString('utf8'))),
        blob: () => Promise.resolve(buf),
      };
    } finally {
      clearTimeout(timer);
    }
  };
}

function buildSandbox() {
  const savedDevices = [];
  const sandbox = {
    console,
    Buffer,
    URL,
    URLSearchParams,
    TextEncoder,
    TextDecoder,
    atob: (s) => Buffer.from(s, 'base64').toString('binary'),
    btoa: (s) => Buffer.from(s, 'binary').toString('base64'),
    setTimeout,
    clearTimeout,
    setInterval,
    clearInterval,
    queueMicrotask,
    performance,
    fetch: makeHostFetch(),
    require,
    crypto: {
      getRandomValues(a) { nodeCrypto.randomFillSync(a); return a; },
      base64(s) { return new Uint8Array(Buffer.from(String(s), 'base64')); },
      utf8(s) { return new Uint8Array(Buffer.from(String(s), 'utf8')); },
      md5(bytes) { return new Uint8Array(nodeCrypto.createHash('md5').update(Buffer.from(bytes)).digest()); },
      decryptBlock(block, key) {
        const d = nodeCrypto.createDecipheriv('aes-128-ecb', Buffer.from(key), null);
        d.setAutoPadding(false);
        return new Uint8Array(Buffer.concat([d.update(Buffer.from(block)), d.final()]));
      },
    },
  };
  sandbox.globalThis = sandbox;
  sandbox.global = sandbox;
  sandbox.self = sandbox;
  sandbox.__native_save_device = (json) => { savedDevices.push(json); };
  sandbox.__savedDevices = savedDevices;
  return sandbox;
}

function loadSpider() {
  const sandbox = buildSandbox();
  const code = fs.readFileSync(SRC, 'utf8');
  vm.createContext(sandbox);
  vm.runInContext(code, sandbox, { filename: 'hongguo.js', timeout: 60000 });
  return sandbox;
}

module.exports = { loadSpider };

if (require.main === module) {
  (async () => {
    const sb = loadSpider();
    const spider = sb.spider || sb.window?.spider;
    console.log('== spider loaded ==', typeof spider, Object.keys(spider || {}).join(','));

    const home = await spider.home();
    console.log('home categories:', JSON.stringify(home.class));

    const cat = await spider.category('real-drama', 1);
    console.log('category page:', cat.page, 'count:', cat.list?.length);
    for (const it of (cat.list || []).slice(0, 3)) console.log('  -', it.vod_id, it.vod_name);

    const item = (cat.list || [])[0];
    if (!item) { console.log('no items'); return; }

    const det = await spider.detail(item.vod_id);
    const vod = det.list?.[0];
    console.log('\n== detail ==');
    console.log('title:', vod?.vod_name, '| remarks:', vod?.vod_remarks);
    console.log('vod_play_url:', vod?.vod_play_url);

    const entries = String(vod?.vod_play_url || '').split('#');
    const first = entries[0]?.split('$')[1];
    console.log('\nplayer route:', first);

    const play = await spider.play(vod?.vod_play_from || '官方直链', first, [{ seriesId: String(item.vod_id) }]);
    console.log('\n== play result ==');
    console.log(JSON.stringify({ parse: play.parse, url: play.url, header: play.header, decryptionKey: play.decryptionKey, quality: play.quality, qualities: play.qualities, expiresAt: play.expiresAt, error: play.error }, null, 2));

    if (play.url) {
      const res = await sb.fetch(play.url, { method: 'HEAD', headers: play.header || {} });
      console.log('\nHEAD direct link ->', res.status, res.headers.get('content-type'), 'len=', res.headers.get('content-length'));
    }
  })().catch((e) => { console.error('FAILED:', e && (e.stack || e.message || e)); process.exit(1); });
}
