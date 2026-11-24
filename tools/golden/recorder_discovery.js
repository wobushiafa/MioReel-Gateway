/**
 * 发现类接口录制器：实跑 hongguo.js 的 home / category / suggestions / search /
 * rankings / recommendations / feed / seriesSeasons，记录全部出站请求与结果摘要。
 *
 * 用法：node tools/golden/recorder_discovery.js [输出目录]
 */
const fs = require('fs');
const path = require('path');
const { loadSpider } = require('./hongguo_host.js');

const OUT = process.argv[2] || path.join(__dirname, 'out');

function brief(v, n = 220) {
  const s = typeof v === 'string' ? v : JSON.stringify(v);
  return s == null ? '' : (s.length > n ? s.slice(0, n) + '…' : s);
}

(async () => {
  fs.mkdirSync(OUT, { recursive: true });
  const sb = loadSpider();
  const captured = [];
  const realFetch = sb.fetch;

  sb.fetch = async (url, opts = {}) => {
    const u = new URL(String(url));
    const entry = {
      at: new Date().toISOString(),
      method: (opts.method || 'GET').toUpperCase(),
      host: u.host,
      path: u.pathname,
      query: Object.fromEntries(u.searchParams.entries()),
      signature_headers: Object.keys(opts.headers || {}).filter((k) =>
        /^x-(gorgon|khronos|ladon|argus|helios|medusa|ss-stub)$/i.test(k)),
      has_stub: 'x-ss-stub' in (opts.headers || {}),
      body: opts.body ? brief(opts.body, 400) : null,
    };
    try {
      const res = await realFetch(url, opts);
      entry.status = res.status;
      captured.push(entry);
      return res;
    } catch (e) {
      entry.error = String(e && (e.message || e));
      captured.push(entry);
      throw e;
    }
  };

  const spider = sb.spider;
  const report = {};

  const run = async (name, fn) => {
    const before = captured.length;
    try {
      const value = await fn();
      report[name] = { ok: true, requests: captured.slice(before), result: brief(value, 600) };
      console.log(`✔ ${name}  (${captured.length - before} 个上游请求)`);
    } catch (e) {
      report[name] = { ok: false, error: String(e && (e.message || e)), code: e && e.code, requests: captured.slice(before) };
      console.log(`✘ ${name}  ${e && (e.code || '')} ${e && (e.message || e)}`);
    }
  };

  await run('home_legacy', () => spider.home());
  await run('home_app', () => spider.home({}));
  await run('category_playlet', () => spider.category('playlet', 1));
  await run('category_motion_comic', () => spider.category('motion_comic', 1));
  await run('category_ai', () => spider.category('ai_playlet', 1));
  await run('suggestions_empty', () => spider.suggestions(''));
  await run('suggestions_keyword', () => spider.suggestions('重生'));
  await run('search_p1', () => spider.search('重生', false, 1));
  await run('search_p2', () => spider.search('重生', false, 2));
  await run('ranking_boards', () => spider.rankingBoards());
  await run('recommendations_ai', () => spider.recommendations('ai-drama', 1));
  await run('feed', () => spider.feed(1, ''));

  fs.writeFileSync(path.join(OUT, 'discovery-samples.json'), JSON.stringify(report, null, 2), 'utf8');

  console.log('\n=== 上游端点汇总 ===');
  const seen = new Map();
  for (const r of captured) {
    const key = `${r.method} ${r.host}${r.path}`;
    if (!seen.has(key)) seen.set(key, { n: 0, params: new Set(), signed: false });
    const e = seen.get(key);
    e.n++;
    e.signed = e.signed || r.signature_headers.length > 0;
    for (const k of ['tab_type', 'tab_index', 'scene', 'from', 'cell_id', 'selected_items', 'query', 'offset', 'limit', 'count', 'tab_type', 'bookstore_tab', 'search_source'])
      if (r.query[k] !== undefined) e.params.add(`${k}=${brief(r.query[k], 40)}`);
  }
  for (const [k, v] of seen) console.log(`  ${k}\n      x${v.n}  签名=${v.signed ? '是' : '否'}  参数: ${[...v.params].join(', ') || '(无)'}`);

  console.log('\n输出:', path.join(OUT, 'discovery-samples.json'));
})().catch((e) => { console.error('FAILED:', e && (e.stack || e)); process.exit(1); });
