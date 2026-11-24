/**
 * 报文录制器：在 Node 宿主沙箱中运行参考实现 hongguo.js，
 * 录制真实发出的签名请求（URL / method / headers / body），
 * 并产出可供 Go 实现比对的黄金样本。
 *
 * 用法：node tools/golden/recorder.js [输出目录]
 */
const fs = require('fs');
const path = require('path');
const { loadSpider } = require('./hongguo_host.js');

const OUT = process.argv[2] || path.join(__dirname, 'out');

function shortBody(b) {
  if (b == null) return null;
  const s = Buffer.isBuffer(b) ? b.toString('utf8') : String(b);
  return s.length > 4096 ? s.slice(0, 4096) + `...<${s.length}B>` : s;
}

(async () => {
  fs.mkdirSync(OUT, { recursive: true });
  const sb = loadSpider();
  const captured = [];

  // 包一层 fetch，把所有出站请求记录下来
  const realFetch = sb.fetch;
  sb.fetch = async (url, opts = {}) => {
    const entry = {
      at: new Date().toISOString(),
      method: (opts.method || 'GET').toUpperCase(),
      url: String(url),
      headers: Object.assign({}, opts.headers || {}),
      body: shortBody(opts.body),
    };
    try {
      const res = await realFetch(url, opts);
      entry.status = res.status;
      entry.responseContentType = res.headers.get('content-type');
      captured.push(entry);
      return res;
    } catch (e) {
      entry.error = String(e && (e.message || e));
      captured.push(entry);
      throw e;
    }
  };

  const spider = sb.spider;

  // 1) 列表
  const cat = await spider.category('real-drama', 1);
  const item = cat.list[0];

  // 2) 详情
  const det = await spider.detail(item.vod_id);
  const vod = det.list[0];
  const playerRoute = String(vod.vod_play_url).split('#')[0].split('$')[1];

  // 3) 解析直链
  const play = await spider.play(vod.vod_play_from, playerRoute, [{ seriesId: String(item.vod_id) }]);

  const direct = {
    capturedAt: new Date().toISOString(),
    series: { vod_id: item.vod_id, vod_name: item.vod_name, remarks: vod.vod_remarks },
    episode: { playerRoute, line: String(vod.vod_play_url).split('#')[0].split('$')[0] },
    resolve: {
      parse: play.parse,
      url: play.url,
      header: play.header,
      decryptionKey: play.decryptionKey || '',
      quality: play.quality,
      qualities: play.qualities,
      expiresAt: play.expiresAt,
      expiresAtISO: Number.isFinite(play.expiresAt) ? new Date(play.expiresAt).toISOString() : null,
      mediaBytes: play.mediaBytes || 0,
      episodeId: play.episodeId || '',
      error: play.error || '',
    },
    requests: captured,
  };

  fs.writeFileSync(path.join(OUT, 'wire-samples.json'), JSON.stringify(direct, null, 2), 'utf8');

  // 单独抽出一份"最小可用"清单，便于客户端冒烟测试
  const smoke = {
    series_id: item.vod_id,
    series_name: item.vod_name,
    episode_line: direct.episode.line,
    player_route: playerRoute,
    play_url: play.url,
    play_headers: play.header,
    decryption_key: play.decryptionKey || '',
    quality: play.quality,
    expires_at: play.expiresAt,
  };
  fs.writeFileSync(path.join(OUT, 'smoke.json'), JSON.stringify(smoke, null, 2), 'utf8');

  console.log('=== 录制完成 ===');
  console.log('剧集      :', item.vod_name, '(' + item.vod_id + ')');
  console.log('直链      :', play.url ? play.url.slice(0, 110) + '...' : '(无)');
  console.log('解密密钥  :', play.decryptionKey || '(无)');
  console.log('有效期至  :', direct.resolve.expiresAtISO);
  console.log('出站请求  :', captured.length, '条');
  for (const r of captured) {
    const signed = Object.keys(r.headers || {}).filter((k) => /^x-(gorgon|khronos|ladon|argus|helios|medusa|ss-stub)$/i.test(k));
    console.log(`  - ${r.method} ${r.url.slice(0, 96)}${r.url.length > 96 ? '…' : ''} [${r.status ?? r.error}]` +
      (signed.length ? `\n      签名头: ${signed.join(', ')}` : ''));
  }
  console.log('\n输出目录  :', OUT);
})().catch((e) => { console.error('FAILED:', e && (e.stack || e.message || e)); process.exit(1); });
