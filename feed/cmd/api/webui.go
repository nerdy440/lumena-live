package main

// lumenaWebUI is the complete Lumena Live web application, embedded in the binary.
// No build step required — the Go server serves it directly.
// It connects to /api/v1/* endpoints on the same origin.
func init() {
	lumenaWebUI = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Lumena Live</title>
<script src="https://cdnjs.cloudflare.com/ajax/libs/react/18.2.0/umd/react.development.js"></script>
<script src="https://cdnjs.cloudflare.com/ajax/libs/react-dom/18.2.0/umd/react-dom.development.js"></script>
<script src="https://cdnjs.cloudflare.com/ajax/libs/babel-standalone/7.23.5/babel.min.js"></script>
<style>
  *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }
  :root {
    --c-bg:       #0a0a0f;
    --c-surface:  #13131a;
    --c-surface2: #1c1c28;
    --c-border:   #2a2a3a;
    --c-primary:  #6c47ff;
    --c-primary2: #9b72ff;
    --c-accent:   #ff5c7a;
    --c-success:  #12b886;
    --c-text:     #f0f0f8;
    --c-text2:    #9898b8;
    --c-text3:    #5a5a7a;
    --c-live:     #ff3b5c;
    --r-sm: 8px; --r-md: 12px; --r-lg: 16px; --r-xl: 20px;
    --font: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
  }
  body { background: var(--c-bg); color: var(--c-text); font-family: var(--font); min-height: 100vh; }
  button { cursor: pointer; border: none; background: none; font-family: inherit; }
  input, textarea { font-family: inherit; }
  a { color: inherit; text-decoration: none; }
  ::-webkit-scrollbar { width: 6px; } ::-webkit-scrollbar-track { background: transparent; }
  ::-webkit-scrollbar-thumb { background: var(--c-border); border-radius: 3px; }
</style>
</head>
<body>
<div id="root"></div>
<script type="text/babel">
const { useState, useEffect, useCallback, useRef } = React;

// ── API client ──────────────────────────────────────────────────────────────
const BASE = '/api/v1';
let _token = localStorage.getItem('lumena_token') || '';

async function api(method, path, body, auth = true, extraHeaders) {
  const headers = { 'Content-Type': 'application/json', ...(extraHeaders || {}) };
  if (auth && _token) headers['Authorization'] = 'Bearer ' + _token;
  const res = await fetch(BASE + path, {
    method, headers,
    body: body ? JSON.stringify(body) : undefined,
  });
  const data = await res.json();
  if (!res.ok) throw data.error || { code: 'UNKNOWN', message: 'Request failed' };
  return data;
}

function setToken(t) {
  _token = t;
  if (t) localStorage.setItem('lumena_token', t);
  else localStorage.removeItem('lumena_token');
}

// uploadAttachment drives the two-step doc 07 §8 flow (create a slot, then
// PUT raw bytes to it) and returns the final scanned status directly from
// the upload response — no polling needed, the dev scanner is synchronous.
// Throws with a message on failure, same contract as api().
async function uploadAttachment(file) {
  const slot = await api('POST', '/attachments', { content_type: file.type || 'application/octet-stream', filename: file.name });
  // slot.upload_url is already a full "/api/v1/..." path from the server.
  const res = await fetch(slot.upload_url, {
    method: 'PUT',
    headers: { 'Content-Type': file.type || 'application/octet-stream', 'Authorization': 'Bearer ' + _token },
    body: file,
  });
  const data = await res.json();
  if (!res.ok) throw data.error || { code: 'UNKNOWN', message: 'Upload failed' };
  return { attachment_id: data.attachment_id, status: data.status, block_reason: data.block_reason, filename: file.name, content_type: file.type };
}

// attachmentObjectURL fetches an attachment's bytes with the caller's own
// auth (an <img>/<a> tag can't carry an Authorization header) and returns a
// local blob: URL for display — revoke it with URL.revokeObjectURL when done.
async function attachmentObjectURL(attachmentId) {
  const res = await fetch(BASE + '/attachments/' + attachmentId + '/download', {
    headers: { 'Authorization': 'Bearer ' + _token },
  });
  if (!res.ok) throw new Error('Could not load attachment');
  const blob = await res.blob();
  return URL.createObjectURL(blob);
}

// ── Design tokens ───────────────────────────────────────────────────────────
const css = {
  card: { background: 'var(--c-surface)', borderRadius: 'var(--r-lg)', overflow: 'hidden', cursor: 'pointer', transition: 'transform 0.15s, box-shadow 0.15s' },
  pill: (color='var(--c-primary)') => ({ background: color + '22', color, border: '1px solid ' + color + '44', borderRadius: '99px', padding: '2px 10px', fontSize: 11, fontWeight: 600 }),
  btn: (variant='primary') => ({
    padding: '10px 22px', borderRadius: 'var(--r-md)', fontWeight: 700, fontSize: 14,
    background: variant === 'primary' ? 'linear-gradient(135deg, var(--c-primary), var(--c-primary2))' : 'var(--c-surface2)',
    color: 'var(--c-text)', border: '1px solid ' + (variant === 'primary' ? 'transparent' : 'var(--c-border)'),
    transition: 'opacity 0.15s',
  }),
  input: { width: '100%', background: 'var(--c-surface2)', border: '1px solid var(--c-border)', borderRadius: 'var(--r-md)', padding: '11px 14px', color: 'var(--c-text)', fontSize: 14, outline: 'none' },
};

// ── Live badge ──────────────────────────────────────────────────────────────
function LiveBadge() {
  return (
    <span style={{ display:'inline-flex', alignItems:'center', gap:5, ...css.pill('var(--c-live)'), fontSize:10 }}>
      <span style={{ width:6, height:6, borderRadius:'50%', background:'var(--c-live)', animation:'pulse 1.5s infinite' }}/>
      LIVE
    </span>
  );
}

// ── Room card ───────────────────────────────────────────────────────────────
function RoomCard({ room, onClick }) {
  const [hovered, setHovered] = useState(false);
  const flagMap = { US:'🇺🇸', PK:'🇵🇰', IN:'🇮🇳', BR:'🇧🇷', NG:'🇳🇬', PH:'🇵🇭', ID:'🇮🇩', MX:'🇲🇽', EG:'🇪🇬', TR:'🇹🇷', XX:'🌍' };
  const flag = flagMap[room.region_code] || '🌍';

  // Generate a gradient background from host name (deterministic)
  const hue = (room.host_id?.charCodeAt(5) || 0) * 37 % 360;
  const gradients = [
    'linear-gradient(135deg, #667eea, #764ba2)',
    'linear-gradient(135deg, #f093fb, #f5576c)',
    'linear-gradient(135deg, #4facfe, #00f2fe)',
    'linear-gradient(135deg, #43e97b, #38f9d7)',
    'linear-gradient(135deg, #fa709a, #fee140)',
    'linear-gradient(135deg, #a18cd1, #fbc2eb)',
    'linear-gradient(135deg, #fda085, #f6d365)',
    'linear-gradient(135deg, #89f7fe, #66a6ff)',
  ];
  const bg = gradients[hue % gradients.length];

  return (
    <div
      style={{ ...css.card, transform: hovered ? 'scale(1.02)' : 'scale(1)', boxShadow: hovered ? '0 8px 32px rgba(108,71,255,0.25)' : 'none' }}
      onClick={() => onClick(room)}
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
    >
      {/* Thumbnail */}
      <div style={{ position:'relative', paddingTop:'56.25%', background: bg }}>
        {/* Host initial as avatar placeholder */}
        <div style={{ position:'absolute', inset:0, display:'flex', alignItems:'center', justifyContent:'center' }}>
          <div style={{ width:64, height:64, borderRadius:'50%', background:'rgba(255,255,255,0.15)', border:'2px solid rgba(255,255,255,0.3)', display:'flex', alignItems:'center', justifyContent:'center', fontSize:28, fontWeight:700 }}>
            {room.host_name?.[0] || '?'}
          </div>
        </div>
        {/* Live badge top-left */}
        <div style={{ position:'absolute', top:8, left:8 }}><LiveBadge /></div>
        {/* Viewer count top-right */}
        <div style={{ position:'absolute', top:8, right:8, background:'rgba(0,0,0,0.6)', borderRadius:'99px', padding:'3px 10px', fontSize:11, fontWeight:600, display:'flex', alignItems:'center', gap:4 }}>
          👁 {room.viewer_count?.toLocaleString()}
        </div>
        {/* Region flag bottom-right */}
        <div style={{ position:'absolute', bottom:8, right:8, fontSize:18 }}>{flag}</div>
      </div>
      {/* Info */}
      <div style={{ padding:'12px 14px' }}>
        <p style={{ fontWeight:700, fontSize:13, marginBottom:4, overflow:'hidden', textOverflow:'ellipsis', whiteSpace:'nowrap' }}>{room.title}</p>
        <p style={{ color:'var(--c-text2)', fontSize:12 }}>{room.host_name}</p>
        {room.tags?.length > 0 && (
          <div style={{ display:'flex', gap:5, marginTop:8, flexWrap:'wrap' }}>
            {room.tags.slice(0,2).map(t => <span key={t} style={css.pill()}>{t}</span>)}
          </div>
        )}
      </div>
    </div>
  );
}

// ── Room modal (viewer placeholder) ─────────────────────────────────────────
// ── Realtime gateway client (Phase 6, doc 08) ──────────────────────────────
// Implements: ws-token exchange, SUBSCRIBE/BACKFILL, reconnect with
// exponential backoff + jitter (BT-04), and msg_id dedup on a 500-entry
// ring buffer (doc 08 §13) so backfill overlap never double-renders events.
function useRealtimeRoom(roomId) {
  const [connected, setConnected] = useState(false);
  const [comments, setComments] = useState([]);
  const [viewerEvents, setViewerEvents] = useState([]);
  const [viewerCount, setViewerCount] = useState(null); // null until the first VIEWER_JOINED/LEFT — server-authoritative, never incremented locally
  const [likeCount, setLikeCount] = useState(0);
  const [notices, setNotices] = useState([]);
  // Wallet state renders ONLY from server-pushed BALANCE_CHANGED /
  // DIAMONDS_CHANGED events (plus an initial REST fetch for the starting
  // value) — never from an optimistic local update (doc 11 §4: "the client
  // never optimistically updates the balance").
  const [wallet, setWallet] = useState({ coins: null, diamonds: null });
  const [giftError, setGiftError] = useState(null);
  const [lastSignal, setLastSignal] = useState(null); // most recent BROADCAST_SIGNAL (WebRTC relay)
  const [translations, setTranslations] = useState({}); // msg_id -> {body, target_lang, confidence}

  const wsRef = useRef(null);
  const lastSeqRef = useRef(0);
  // viewerCount is a live gauge, not an append-only log — unlike comments,
  // reapplying an older event must never regress a newer value. Backfill
  // replays history in seq order, but BACKFILL_RESULT arrives over its
  // own round trip and can land after a live event with a higher seq
  // already updated the count, so "most recently applied" isn't safe to
  // trust; only "highest seq ever applied" is.
  const viewerCountSeqRef = useRef(0);
  const attemptRef = useRef(0);
  const seenRef = useRef([]); // ring buffer of msg_id, cap 500
  const stoppedRef = useRef(false);
  const timerRef = useRef(null);

  const dedup = useCallback((msgId) => {
    if (!msgId) return false; // control messages (SUBSCRIBED etc.) have no msg_id — never dedup those away
    if (seenRef.current.includes(msgId)) return true;
    seenRef.current.push(msgId);
    if (seenRef.current.length > 500) seenRef.current.shift();
    return false;
  }, []);

  const applyEvent = useCallback((ev) => {
    if (dedup(ev.msg_id)) return;
    if (ev.seq) lastSeqRef.current = Math.max(lastSeqRef.current, ev.seq);
    switch (ev.type) {
      case 'COMMENT':
        // msg_id lives on the envelope, not the payload — attach it so the
        // UI can key a REQUEST_TRANSLATION's ref_msg_id back to this comment.
        setComments(c => [...c.slice(-199), { ...ev.payload, _msgId: ev.msg_id }]);
        break;
      case 'COMMENT_TRANSLATED':
        // A separate, later event — the original comment above is never
        // replaced, this only ever adds a patch keyed by ref_msg_id (doc 08 §5).
        setTranslations(t => ({ ...t, [ev.payload.ref_msg_id]: ev.payload }));
        break;
      case 'VIEWER_JOINED':
        setViewerEvents(v => [...v.slice(-49), { kind: 'joined', ...ev.payload }]);
        if (typeof ev.payload.viewer_count === 'number' && ev.seq >= viewerCountSeqRef.current) {
          viewerCountSeqRef.current = ev.seq;
          setViewerCount(ev.payload.viewer_count);
        }
        break;
      case 'VIEWER_LEFT':
        setViewerEvents(v => [...v.slice(-49), { kind: 'left', ...ev.payload }]);
        if (typeof ev.payload.viewer_count === 'number' && ev.seq >= viewerCountSeqRef.current) {
          viewerCountSeqRef.current = ev.seq;
          setViewerCount(ev.payload.viewer_count);
        }
        break;
      case 'LIKE':
        setLikeCount(n => n + 1);
        break;
      case 'GIFT_SENT':
        setComments(c => [...c.slice(-199), { system: true, body: '🎁 ' + ev.payload.sender_id + ' sent ' + (ev.payload.quantity || 1) + 'x ' + (ev.payload.gift_name || ev.payload.gift_id) }]);
        break;
      case 'BALANCE_CHANGED':
        setWallet(w => ({ ...w, coins: ev.payload.new_balance }));
        break;
      case 'DIAMONDS_CHANGED':
        setWallet(w => ({ ...w, diamonds: ev.payload.new_diamonds }));
        break;
      case 'BROADCAST_SIGNAL':
        setLastSignal({ ...ev.payload, _t: Date.now() + Math.random() });
        break;
      case 'STREAM_ENDED':
        setNotices(n => [...n, { type: 'stream_ended', message: 'This stream has ended.' }]);
        break;
      case 'USER_MUTED':
      case 'USER_KICKED':
      case 'ROOM_MODERATION_NOTICE':
        setNotices(n => [...n, { type: ev.type, message: ev.payload.message || ev.type, ruleId: ev.payload.rule_id }]);
        break;
      case 'ACCOUNT_STATUS':
        setNotices(n => [...n, { type: 'account_status', message: 'Your account status: ' + ev.payload.status, ruleId: ev.payload.rule_id }]);
        break;
      case 'ERROR':
        if (ev.payload && ev.payload.code === 'INSUFFICIENT_BALANCE') {
          setGiftError({ shortfall: ev.payload.shortfall });
        }
        break;
      default:
        break;
    }
  }, [dedup]);

  // Initial wallet snapshot — WS only pushes deltas from here on.
  useEffect(() => {
    if (!_token) return;
    api('GET', '/wallet/balance').then(d => setWallet({ coins: d.coins, diamonds: d.diamonds })).catch(() => {});
  }, []);

  const backfillIfNeeded = useCallback((ws, topic, ack) => {
    // Any gap between what we've seen (lastSeqRef, 0 on a brand-new
    // subscribe) and the topic's current seq means there's history to
    // replay — including history from BEFORE this client ever connected
    // (e.g. a host opening their own room after viewers already joined).
    // A previous version treated "server ahead of us" as proof this was a
    // first-time subscribe with nothing to catch up on, which is wrong
    // whenever anyone else was active on the topic first — it silently
    // dropped that backlog (a host would never learn about a viewer who
    // joined before the host opened the room, for example).
    if (ack.payload && ack.payload.last_seq > lastSeqRef.current) {
      ws.send(JSON.stringify({ type: 'BACKFILL', topic, payload: { from_seq: lastSeqRef.current } }));
    }
  }, []);

  const connect = useCallback(async () => {
    if (stoppedRef.current || !_token) return;
    try {
      const tokData = await api('GET', '/auth/ws-token?device=web', null, true);
      const proto = location.protocol === 'https:' ? 'wss' : 'ws';
      const ws = new WebSocket(proto + '://' + location.host + '/ws?token=' + tokData.ws_token);
      wsRef.current = ws;
      const topic = 'room:' + roomId;

      ws.onopen = () => {
        attemptRef.current = 0;
        setConnected(true);
        ws.send(JSON.stringify({ type: 'SUBSCRIBE', topic }));
      };
      ws.onmessage = (msg) => {
        const ev = JSON.parse(msg.data);
        if (ev.type === 'SUBSCRIBED') {
          backfillIfNeeded(ws, topic, ev);
          return;
        }
        if (ev.type === 'BACKFILL_RESULT') {
          (ev.payload?.events || []).forEach(applyEvent);
          return;
        }
        if (ev.type === 'CATCHUP_REQUIRED') {
          // Gap too large to replay (doc 08 §11.3) — reset local state from a fresh subscribe.
          lastSeqRef.current = 0;
          setComments([]); setViewerEvents([]);
          return;
        }
        applyEvent(ev);
      };
      ws.onclose = () => {
        setConnected(false);
        if (stoppedRef.current) return;
        scheduleReconnect();
      };
      ws.onerror = () => ws.close();
    } catch (e) {
      scheduleReconnect();
    }
  }, [roomId, applyEvent, backfillIfNeeded]);

  const scheduleReconnect = useCallback(() => {
    attemptRef.current += 1;
    // doc 08 §11: min(60s, 2^n * 0.5s) ± jitter (jitter = half the base delay).
    const base = Math.min(60000, Math.pow(2, attemptRef.current) * 500);
    const jitter = (Math.random() - 0.5) * base;
    const delay = Math.max(250, base + jitter);
    timerRef.current = setTimeout(connect, delay);
  }, [connect]);

  const send = useCallback((type, payload) => {
    if (wsRef.current && wsRef.current.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify({ type, topic: 'room:' + roomId, payload: payload || {} }));
    }
  }, [roomId]);

  useEffect(() => {
    stoppedRef.current = false;
    connect();
    return () => {
      stoppedRef.current = true;
      clearTimeout(timerRef.current);
      if (wsRef.current) wsRef.current.close();
    };
  }, [roomId]); // eslint-disable-line react-hooks/exhaustive-deps

  const requestTranslation = useCallback((comment, targetLang) => {
    if (!comment._msgId || translations[comment._msgId]) return; // already have it, or nothing to key it to
    send('REQUEST_TRANSLATION', {
      ref_msg_id: comment._msgId, body: comment.body,
      source_lang: comment.lang || '', target_lang: targetLang,
    });
  }, [send, translations]);

  return {
    connected, comments, viewerEvents, viewerCount, likeCount, notices, send, wallet, giftError, lastSignal,
    translations, requestTranslation,
    clearGiftError: () => setGiftError(null),
    // Not an optimistic update: the caller passes the actual response from a
    // REST call it just made (e.g. dev-topup), which is as authoritative as
    // the initial balance fetch above — WS only pushes deltas from here on,
    // so a REST-driven balance change needs this to reach the display.
    setWalletFromServer: (w) => setWallet(w),
  };
}

// ── Chat overlay visibility toggle (BT-01) ─────────────────────────────────
// The reference product's chat overlay had no way to hide it, obscuring the
// stream itself. This persists per-browser so the preference survives room
// exit/return, matching the roadmap's exit-gate for BT-01.
function useChatVisible() {
  const [visible, setVisible] = useState(() => localStorage.getItem('lumena_chat_visible') !== '0');
  const toggle = useCallback(() => {
    setVisible(v => {
      const next = !v;
      localStorage.setItem('lumena_chat_visible', next ? '1' : '0');
      return next;
    });
  }, []);
  return [visible, toggle];
}

// ── Translation toggle + target language (Phase 13, doc 08 §5) ─────────────
// Per-user preference, persisted across room exit/return (roadmap exit
// gate: "Translation toggle state persists").
function useTranslatePref() {
  const [enabled, setEnabled] = useState(() => localStorage.getItem('lumena_translate_on') === '1');
  const [targetLang, setTargetLang] = useState(() => localStorage.getItem('lumena_translate_lang') || 'es');
  const toggle = useCallback(() => {
    setEnabled(v => {
      const next = !v;
      localStorage.setItem('lumena_translate_on', next ? '1' : '0');
      return next;
    });
  }, []);
  const changeLang = useCallback((lang) => {
    setTargetLang(lang);
    localStorage.setItem('lumena_translate_lang', lang);
  }, []);
  return { enabled, targetLang, toggle, changeLang };
}

// ── Gift drawer (Phase 8, doc 11 §4 / roadmap Phase 8) ─────────────────────
// Balance shown here comes only from rt.wallet (server-pushed BALANCE_CHANGED),
// never from a local guess. Each send gets a fresh idempotency key generated
// client-side (roadmap: "Gift send requires an Idempotency-Key header" — the
// WS path threads the same key through payload.idempotency_key instead).
function GiftDrawer({ rt, recipientID, onClose }) {
  const [catalogue, setCatalogue] = useState([]);
  const [sendingID, setSendingID] = useState(null);
  const [topupBusy, setTopupBusy] = useState(false);
  const [showBuyCoins, setShowBuyCoins] = useState(false);

  useEffect(() => {
    api('GET', '/gifts/catalogue', null, false).then(d => setCatalogue(d.items || [])).catch(() => {});
  }, []);

  useEffect(() => {
    if (sendingID && (rt.giftError || rt.wallet.coins !== null)) {
      setSendingID(null);
    }
  }, [rt.wallet.coins, rt.giftError]); // eslint-disable-line react-hooks/exhaustive-deps

  function sendGift(gift) {
    setSendingID(gift.id);
    rt.clearGiftError();
    const idempotencyKey = 'web-' + Date.now() + '-' + Math.random().toString(36).slice(2);
    rt.send('GIFT_SENT', { recipient_id: recipientID, gift_id: gift.id, quantity: 1, idempotency_key: idempotencyKey });
    setTimeout(() => setSendingID(id => (id === gift.id ? null : id)), 4000); // fallback if no event arrives
  }

  async function devTopUp() {
    setTopupBusy(true);
    try {
      const d = await api('POST', '/wallet/dev-topup', { coins: (rt.giftError?.shortfall || 500) + 100 });
      rt.setWalletFromServer({ coins: d.coins, diamonds: d.diamonds });
      rt.clearGiftError();
    } catch (e) {
      alert(e.message || 'Top-up failed.');
    } finally {
      setTopupBusy(false);
    }
  }

  return (
    <div style={{ position:'absolute', inset:0, background:'rgba(0,0,0,0.6)', display:'flex', alignItems:'flex-end', zIndex:10 }} onClick={onClose}>
      <div style={{ background:'var(--c-surface)', width:'100%', borderRadius:'var(--r-xl) var(--r-xl) 0 0', padding:16 }} onClick={e => e.stopPropagation()}>
        <div style={{ display:'flex', justifyContent:'space-between', alignItems:'center', marginBottom:12 }}>
          <strong>🎁 Send a gift</strong>
          <span style={{ fontSize:12, color:'var(--c-text2)' }}>Balance: {rt.wallet.coins === null ? '…' : rt.wallet.coins.toLocaleString()} coins</span>
        </div>

        {rt.giftError && (
          <div style={{ background:'var(--c-accent)22', border:'1px solid var(--c-accent)44', borderRadius:'var(--r-md)', padding:12, marginBottom:12, fontSize:12 }}>
            <p style={{ color:'var(--c-accent)', fontWeight:700, marginBottom:6 }}>Not enough coins — need {rt.giftError.shortfall} more.</p>
            <div style={{ display:'flex', gap:8 }}>
              <button onClick={() => setShowBuyCoins(true)} style={{ ...css.btn('primary'), padding:'6px 12px', fontSize:12 }}>+ Buy Coins</button>
              <button onClick={devTopUp} disabled={topupBusy} style={{ ...css.btn('secondary'), padding:'6px 12px', fontSize:12 }}>
                {topupBusy ? 'Adding…' : 'Dev top-up'}
              </button>
            </div>
          </div>
        )}

        <button onClick={() => setShowBuyCoins(true)} style={{ ...css.btn('secondary'), width:'100%', marginBottom:10, fontSize:12 }}>+ Buy Coins</button>

        {showBuyCoins && (
          <BuyCoinsModal onClose={() => setShowBuyCoins(false)} onPurchased={() => {
            api('GET', '/wallet/balance').then(d => rt.setWalletFromServer({ coins: d.coins, diamonds: d.diamonds })).catch(() => {});
            rt.clearGiftError();
          }} />
        )}

        <div style={{ display:'grid', gridTemplateColumns:'repeat(auto-fill, minmax(100px, 1fr))', gap:10 }}>
          {catalogue.map(g => (
            <button key={g.id} disabled={sendingID === g.id} onClick={() => sendGift(g)}
              style={{ background:'var(--c-surface2)', borderRadius:'var(--r-md)', padding:'12px 8px', display:'flex', flexDirection:'column', alignItems:'center', gap:4, opacity: sendingID === g.id ? 0.5 : 1 }}>
              <span style={{ fontSize:24 }}>🎁</span>
              <span style={{ fontSize:12, fontWeight:600 }}>{g.name}</span>
              <span style={{ fontSize:11, color:'var(--c-text2)' }}>{sendingID === g.id ? 'Sending…' : g.coins + ' coins'}</span>
            </button>
          ))}
        </div>
      </div>
    </div>
  );
}

// ── Real browser-to-browser WebRTC video (host camera -> each viewer) ──────
// Not the doc 09 broadcast pipeline (ingest/transcode/CDN, 1:many at scale)
// — there's no media server here. This is a direct mesh: the host's browser
// opens one RTCPeerConnection per viewer and sends its camera track over
// it, signaled through the gateway's BROADCAST_SIGNAL relay (point-to-point
// via each account's private topic). Fine for a handful of viewers in a
// dev/test room; would not scale to a real audience.
const ICE_SERVERS = [{ urls: 'stun:stun.l.google.com:19302' }];

function makeSyntheticStream(label) {
  // Fallback when getUserMedia fails (no camera, permission denied, or a
  // sandboxed browser with no device) — proves the WebRTC transport is
  // real even without physical hardware.
  const canvas = document.createElement('canvas');
  canvas.width = 320; canvas.height = 240;
  const ctx = canvas.getContext('2d');
  let hue = Math.random() * 360;
  const timer = setInterval(() => {
    hue = (hue + 2) % 360;
    ctx.fillStyle = 'hsl(' + hue + ', 65%, 28%)';
    ctx.fillRect(0, 0, 320, 240);
    ctx.fillStyle = 'rgba(255,255,255,0.9)';
    ctx.font = 'bold 16px sans-serif';
    ctx.fillText('🔴 ' + label, 16, 100);
    ctx.font = '12px sans-serif';
    ctx.fillText('no camera — synthetic feed', 16, 122);
    ctx.fillText(new Date().toLocaleTimeString(), 16, 144);
  }, 100);
  const stream = canvas.captureStream(30);
  stream.addEventListener('inactive', () => clearInterval(timer));
  stream._syntheticTimer = timer; // stopped explicitly in stopCamera
  return stream;
}

function useBroadcast(rt, accountId, isHost) {
  const [localStream, setLocalStream] = useState(null);
  const [remoteStream, setRemoteStream] = useState(null);
  const [broadcasting, setBroadcasting] = useState(false);
  const [cameraError, setCameraError] = useState('');
  const [connectingToHost, setConnectingToHost] = useState(false); // viewer: got an offer, negotiating
  const pcsRef = useRef({});          // host: viewerId -> pc. viewer: hostId -> pc.
  const localStreamRef = useRef(null);
  const knownViewersRef = useRef(new Set());

  const iceServersRef = useRef(ICE_SERVERS);

  function newPeerConnection() {
    return new RTCPeerConnection({ iceServers: iceServersRef.current });
  }

  async function createOfferFor(viewerId) {
    if (pcsRef.current[viewerId] || !localStreamRef.current) return;
    console.log('[webrtc] host: creating offer for', viewerId);
    const pc = newPeerConnection();
    pcsRef.current[viewerId] = pc;
    localStreamRef.current.getTracks().forEach(t => pc.addTrack(t, localStreamRef.current));
    pc.onicecandidate = (e) => {
      if (e.candidate) rt.send('BROADCAST_SIGNAL', { target_account_id: viewerId, kind: 'ice', candidate: e.candidate.toJSON() });
    };
    pc.oniceconnectionstatechange = () => console.log('[webrtc] host->' + viewerId + ' ICE state:', pc.iceConnectionState);
    pc.onconnectionstatechange = () => console.log('[webrtc] host->' + viewerId + ' connection state:', pc.connectionState);
    try {
      const offer = await pc.createOffer();
      await pc.setLocalDescription(offer);
      rt.send('BROADCAST_SIGNAL', { target_account_id: viewerId, kind: 'offer', sdp: offer.sdp });
    } catch (e) {
      console.error('[webrtc] host: failed to create/send offer for', viewerId, e);
      delete pcsRef.current[viewerId];
    }
  }

  async function startCamera() {
    let stream;
    try {
      stream = await navigator.mediaDevices.getUserMedia({ video: true, audio: true });
      setCameraError('');
    } catch (e) {
      console.warn('[webrtc] getUserMedia failed, falling back to synthetic feed:', e);
      setCameraError('No camera available (' + (e.message || e.name) + ') — using a synthetic feed instead.');
      stream = makeSyntheticStream(accountId);
    }
    localStreamRef.current = stream;
    setLocalStream(stream);
    setBroadcasting(true);
  }

  function stopCamera() {
    if (localStreamRef.current) {
      if (localStreamRef.current._syntheticTimer) clearInterval(localStreamRef.current._syntheticTimer);
      localStreamRef.current.getTracks().forEach(t => t.stop());
    }
    Object.values(pcsRef.current).forEach(pc => pc.close());
    pcsRef.current = {};
    localStreamRef.current = null;
    setLocalStream(null);
    setBroadcasting(false);
  }

  // Track the room's current viewer roster from join/left events. Processes
  // every new event since the last render (not just the latest one) so a
  // burst of joins/leaves can never skip anyone — the original version only
  // inspected events[events.length-1], which silently dropped anyone who
  // wasn't the single most recent event by the time this effect re-ran.
  const processedCountRef = useRef(0);
  useEffect(() => {
    if (!isHost) return;
    const events = rt.viewerEvents;
    for (let i = processedCountRef.current; i < events.length; i++) {
      const ev = events[i];
      if (!ev.user_id || ev.user_id === accountId) continue;
      if (ev.kind === 'joined') {
        knownViewersRef.current.add(ev.user_id);
        if (broadcasting) createOfferFor(ev.user_id);
      } else if (ev.kind === 'left') {
        knownViewersRef.current.delete(ev.user_id);
        const pc = pcsRef.current[ev.user_id];
        if (pc) { pc.close(); delete pcsRef.current[ev.user_id]; }
      }
    }
    processedCountRef.current = events.length;
  }, [rt.viewerEvents, isHost, broadcasting]); // eslint-disable-line react-hooks/exhaustive-deps

  // When the camera turns on, offer to every viewer already known — covers
  // viewers who joined before Start Camera was clicked (the join-tracking
  // effect above only offers to viewers joining AFTER broadcasting is true).
  useEffect(() => {
    if (!isHost || !broadcasting) return;
    console.log('[webrtc] host: broadcasting started, offering to', knownViewersRef.current.size, 'known viewer(s)');
    knownViewersRef.current.forEach(viewerId => createOfferFor(viewerId));
  }, [broadcasting, isHost]); // eslint-disable-line react-hooks/exhaustive-deps

  // Handle incoming signaling (offers/answers/ICE) for both roles.
  useEffect(() => {
    const sig = rt.lastSignal;
    if (!sig) return;
    const { sender_id, kind, sdp, candidate } = sig;

    console.log('[webrtc] received signal', kind, 'from', sender_id, isHost ? '(as host)' : '(as viewer)');
    (async () => {
      if (isHost) {
        const pc = pcsRef.current[sender_id];
        if (!pc) { console.warn('[webrtc] host: no pc for', sender_id, '— was an offer ever sent?'); return; }
        if (kind === 'answer') {
          try { await pc.setRemoteDescription({ type: 'answer', sdp }); } catch (e) { console.error('[webrtc] host: setRemoteDescription(answer) failed', e); }
        } else if (kind === 'ice' && candidate) {
          try { await pc.addIceCandidate(candidate); } catch (e) { console.error('[webrtc] host: addIceCandidate failed', e); }
        }
      } else {
        if (kind === 'offer') {
          let pc = pcsRef.current[sender_id];
          if (!pc) {
            pc = newPeerConnection();
            pcsRef.current[sender_id] = pc;
            pc.ontrack = (e) => { console.log('[webrtc] viewer: received remote track from', sender_id); setRemoteStream(e.streams[0]); };
            pc.onicecandidate = (e) => {
              if (e.candidate) rt.send('BROADCAST_SIGNAL', { target_account_id: sender_id, kind: 'ice', candidate: e.candidate.toJSON() });
            };
            pc.oniceconnectionstatechange = () => console.log('[webrtc] viewer<-' + sender_id + ' ICE state:', pc.iceConnectionState);
            pc.onconnectionstatechange = () => console.log('[webrtc] viewer<-' + sender_id + ' connection state:', pc.connectionState);
          }
          try {
            setConnectingToHost(true);
            await pc.setRemoteDescription({ type: 'offer', sdp });
            const answer = await pc.createAnswer();
            await pc.setLocalDescription(answer);
            rt.send('BROADCAST_SIGNAL', { target_account_id: sender_id, kind: 'answer', sdp: answer.sdp });
          } catch (e) { console.error('[webrtc] viewer: failed to answer offer from', sender_id, e); setConnectingToHost(false); }
        } else if (kind === 'ice' && candidate) {
          const pc = pcsRef.current[sender_id];
          if (pc) { try { await pc.addIceCandidate(candidate); } catch (e) { console.error('[webrtc] viewer: addIceCandidate failed', e); } }
        }
      }
    })();
  }, [rt.lastSignal]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => () => stopCamera(), []); // eslint-disable-line react-hooks/exhaustive-deps

  return { localStream, remoteStream, broadcasting, cameraError, connectingToHost, startCamera, stopCamera };
}

function VideoView({ stream, muted, style }) {
  const videoRef = useRef(null);
  useEffect(() => {
    if (videoRef.current) videoRef.current.srcObject = stream || null;
  }, [stream]);
  return <video ref={videoRef} autoPlay playsInline muted={!!muted} style={style} />;
}

function RoomModal({ room, account, onClose }) {
  const flagMap = { US:'🇺🇸', PK:'🇵🇰', IN:'🇮🇳', BR:'🇧🇷', NG:'🇳🇬', PH:'🇵🇭', ID:'🇮🇩', MX:'🇲🇽', EG:'🇪🇬', TR:'🇹🇷', XX:'🌍' };
  const hue = (room.host_id?.charCodeAt(5) || 0) * 37 % 360;
  const gradients = ['linear-gradient(135deg,#667eea,#764ba2)','linear-gradient(135deg,#f093fb,#f5576c)','linear-gradient(135deg,#4facfe,#00f2fe)','linear-gradient(135deg,#43e97b,#38f9d7)','linear-gradient(135deg,#fa709a,#fee140)','linear-gradient(135deg,#a18cd1,#fbc2eb)','linear-gradient(135deg,#fda085,#f6d365)','linear-gradient(135deg,#89f7fe,#66a6ff)'];
  const bg = gradients[hue % gradients.length];
  const rt = useRealtimeRoom(room.room_id);
  const [chatVisible, toggleChat] = useChatVisible();
  const translatePref = useTranslatePref();
  const [draft, setDraft] = useState('');
  const [showGiftDrawer, setShowGiftDrawer] = useState(false);
  const [showReport, setShowReport] = useState(false);
  const [following, setFollowing] = useState(null); // null until the relationship loads
  const [followBusy, setFollowBusy] = useState(false);
  const [viewedProfileID, setViewedProfileID] = useState(null);
  const isHost = account && account.id === room.host_id;
  const bcast = useBroadcast(rt, account?.id, isHost);

  useEffect(() => {
    if (!account || isHost) return;
    api('GET', '/users/' + room.host_id + '/relationship')
      .then(d => setFollowing(!!d.relationship?.following))
      .catch(() => {});
  }, [account, isHost, room.host_id]);

  async function toggleFollow() {
    if (!account || followBusy) return;
    setFollowBusy(true);
    try {
      const d = following
        ? await api('DELETE', '/follows/' + room.host_id)
        : await api('POST', '/follows', { followee_id: room.host_id });
      setFollowing(!!d.relationship?.following);
    } catch (e) {
      alert(e.message || 'Could not update follow status.');
    } finally {
      setFollowBusy(false);
    }
  }

  // Auto-request a translation for each new comment while the toggle is on
  // — the request is async and per-message; the original already rendered
  // the instant it arrived (doc 08 §5).
  useEffect(() => {
    if (!translatePref.enabled || rt.comments.length === 0) return;
    const last = rt.comments[rt.comments.length - 1];
    if (last.system || !last._msgId) return;
    rt.requestTranslation(last, translatePref.targetLang);
  }, [rt.comments, translatePref.enabled, translatePref.targetLang]); // eslint-disable-line react-hooks/exhaustive-deps

  function sendComment(e) {
    e.preventDefault();
    if (!draft.trim()) return;
    rt.send('COMMENT', { body: draft.trim(), lang: 'en' });
    setDraft('');
  }

  return (
    <div style={{ position:'fixed', inset:0, background:'rgba(0,0,0,0.85)', zIndex:1000, display:'flex', alignItems:'center', justifyContent:'center', padding:20 }} onClick={onClose}>
      <div style={{ background:'var(--c-surface)', borderRadius:'var(--r-xl)', overflow:'hidden', width:'100%', maxWidth:520, maxHeight:'90vh', overflowY:'auto', position:'relative' }} onClick={e => e.stopPropagation()}>
        {showGiftDrawer && <GiftDrawer rt={rt} recipientID={room.host_id} onClose={() => setShowGiftDrawer(false)} />}
        {/* Stream area */}
        <div style={{ background: (isHost ? bcast.broadcasting : bcast.remoteStream) ? '#000' : bg, position:'relative', minHeight:260, display:'flex', flexDirection:'column', alignItems:'center', justifyContent:'center', gap:12, padding: (isHost ? bcast.broadcasting : bcast.remoteStream) ? 0 : 40 }}>
          <div style={{ position:'absolute', top:12, right:12, zIndex:2, display:'flex', alignItems:'center', gap:6, fontSize:11, background:'rgba(0,0,0,0.4)', borderRadius:'99px', padding:'4px 10px' }}>
            <span style={{ width:7, height:7, borderRadius:'50%', background: rt.connected ? 'var(--c-success)' : 'var(--c-accent)' }}/>
            {rt.connected ? 'Live updates connected' : 'Reconnecting…'}
          </div>

          {isHost && bcast.broadcasting && (
            <VideoView stream={bcast.localStream} muted style={{ width:'100%', height:260, objectFit:'cover' }} />
          )}
          {!isHost && bcast.remoteStream && (
            <VideoView stream={bcast.remoteStream} style={{ width:'100%', height:260, objectFit:'cover' }} />
          )}

          {!(isHost ? bcast.broadcasting : bcast.remoteStream) && (
            <>
              <div style={{ width:80, height:80, borderRadius:'50%', background:'rgba(255,255,255,0.2)', display:'flex', alignItems:'center', justifyContent:'center', fontSize:36, fontWeight:700, border:'3px solid rgba(255,255,255,0.4)' }}>
                {room.host_name?.[0]}
              </div>
              <LiveBadge />
              <p style={{ fontWeight:800, fontSize:18 }}>{room.host_name}</p>
              <p style={{ opacity:0.8, fontSize:14, textAlign:'center' }}>{room.title}</p>
            </>
          )}

          {isHost && !bcast.broadcasting && (
            <button style={{ ...css.btn('primary'), padding:'10px 20px' }} onClick={bcast.startCamera}>📷 Start Camera</button>
          )}
          {isHost && bcast.broadcasting && (
            <button style={{ position:'absolute', bottom:12, right:12, zIndex:2, ...css.btn('secondary'), padding:'6px 14px', fontSize:12 }} onClick={bcast.stopCamera}>Stop Camera</button>
          )}
          {!isHost && !bcast.remoteStream && (
            <p style={{ fontSize:12, opacity:0.7, textAlign:'center', maxWidth:280 }}>
              {bcast.connectingToHost
                ? 'Connecting to host\'s stream… (check browser console for [webrtc] logs if this hangs)'
                : 'Waiting for the host to start their camera… (real WebRTC — no media server, direct browser-to-browser)'}
            </p>
          )}
          {bcast.cameraError && (
            <p style={{ fontSize:11, color:'#ffd479', textAlign:'center', maxWidth:280 }}>{bcast.cameraError}</p>
          )}
        </div>
        {/* Info */}
        <div style={{ padding:20 }}>
          <div style={{ display:'flex', justifyContent:'space-between', alignItems:'center', marginBottom:16 }}>
            <div>
              <button style={{ fontWeight:700 }} onClick={() => setViewedProfileID(room.host_id)}>{room.host_name}</button>
              <p style={{ color:'var(--c-text2)', fontSize:13 }}>{(rt.viewerCount ?? room.viewer_count ?? 0).toLocaleString()} viewers · {flagMap[room.region_code] || '🌍'} {room.region_code}</p>
            </div>
            <div style={{ display:'flex', gap:8 }}>
              {account && !isHost && (
                <button
                  style={following ? css.btn('secondary') : css.btn('primary')}
                  disabled={followBusy || following === null}
                  onClick={toggleFollow}
                >
                  {following ? 'Following' : 'Follow'}
                </button>
              )}
              <button style={{ ...css.btn('primary'), padding:'10px 16px' }} onClick={() => rt.send('LIKE')}>❤️ {rt.likeCount}</button>
              {account && account.id !== room.host_id && (
                <button style={{ ...css.btn('primary'), padding:'10px 16px' }} onClick={() => setShowGiftDrawer(true)}>🎁 Gift</button>
              )}
              {account && account.id !== room.host_id && (
                <button style={{ ...css.btn('secondary'), padding:'10px 12px', fontSize:16 }} title="Report" onClick={() => setShowReport(true)}>🚩</button>
              )}
            </div>
          </div>

          {showReport && (
            <ReportModal subjectType="user" subjectId={room.host_id} onClose={() => setShowReport(false)} />
          )}

          {viewedProfileID && (
            <ProfileModal accountID={viewedProfileID} viewerAccount={account} onClose={() => setViewedProfileID(null)} onOpenProfile={setViewedProfileID} />
          )}

          {notices(rt.notices)}

          {/* Chat overlay visibility toggle — BT-01 */}
          <div style={{ display:'flex', justifyContent:'space-between', alignItems:'center', marginBottom:8, gap:8, flexWrap:'wrap' }}>
            <span style={{ fontSize:12, color:'var(--c-text2)' }}>💬 Live chat</span>
            <div style={{ display:'flex', gap:6, alignItems:'center' }}>
              <button onClick={translatePref.toggle} style={{ fontSize:11, color: translatePref.enabled ? 'var(--c-primary2)' : 'var(--c-text2)', padding:'4px 10px', borderRadius:'99px', border:'1px solid ' + (translatePref.enabled ? 'var(--c-primary)' : 'var(--c-border)') }}>
                🌐 {translatePref.enabled ? 'Translating' : 'Translate'}
              </button>
              {translatePref.enabled && (
                <select value={translatePref.targetLang} onChange={e => translatePref.changeLang(e.target.value)} style={{ fontSize:11, background:'var(--c-surface2)', color:'var(--c-text)', border:'1px solid var(--c-border)', borderRadius:'99px', padding:'4px 8px' }}>
                  <option value="es">Spanish</option>
                  <option value="ar">Arabic</option>
                  <option value="hi">Hindi</option>
                  <option value="fr">French</option>
                  <option value="ja">Japanese</option>
                </select>
              )}
              <button onClick={toggleChat} style={{ fontSize:11, color:'var(--c-text2)', padding:'4px 10px', borderRadius:'99px', border:'1px solid var(--c-border)' }}>
                {chatVisible ? '🙈 Hide chat' : '👁 Show chat'}
              </button>
            </div>
          </div>

          {chatVisible && (
            <div style={{ background:'var(--c-surface2)', borderRadius:'var(--r-md)', padding:14, marginBottom:12, height:160, overflowY:'auto', display:'flex', flexDirection:'column', gap:6, fontSize:13 }}>
              {rt.comments.length === 0 && rt.viewerEvents.length === 0 && (
                <span style={{ color:'var(--c-text3)', margin:'auto' }}>No activity yet — say hello!</span>
              )}
              {rt.viewerEvents.slice(-5).map((v, i) => (
                <div key={'v'+i} style={{ color:'var(--c-text3)', fontSize:11 }}>{v.user_id} {v.kind === 'joined' ? 'joined' : 'left'}</div>
              ))}
              {rt.comments.map((c, i) => {
                const t = c._msgId ? rt.translations[c._msgId] : null;
                return c.system
                  ? <div key={i} style={{ color:'var(--c-primary2)', fontWeight:600 }}>{c.body}</div>
                  : (
                    <div key={i}>
                      <strong style={{ color:'var(--c-primary2)' }}>{c.sender_id}: </strong>{c.body}
                      {c.moderation === 'flagged' && <span style={{ color:'var(--c-accent)', fontSize:10 }}> (flagged)</span>}
                      {t && (
                        <div style={{ color:'var(--c-text2)', fontSize:11, fontStyle:'italic', marginTop:2 }}>
                          🌐 {t.translated_body} <span style={{ opacity:0.6 }}>({t.target_lang})</span>
                        </div>
                      )}
                    </div>
                  );
              })}
            </div>
          )}

          <form onSubmit={sendComment} style={{ display:'flex', gap:8 }}>
            <input style={{ ...css.input, flex:1 }} placeholder="Send a message…" value={draft} onChange={e => setDraft(e.target.value)} />
            <button type="submit" style={{ ...css.btn('primary'), padding:'10px 16px' }}>Send</button>
          </form>
        </div>
        <div style={{ padding:'0 20px 20px', display:'flex', justifyContent:'flex-end' }}>
          <button style={css.btn('secondary')} onClick={onClose}>← Back to feed</button>
        </div>
      </div>
    </div>
  );
}

// notices renders moderation / stream-end banners above the chat.
function notices(list) {
  if (!list || list.length === 0) return null;
  return (
    <div style={{ marginBottom:12, display:'flex', flexDirection:'column', gap:6 }}>
      {list.map((n, i) => (
        <div key={i} style={{ background:'var(--c-accent)22', border:'1px solid var(--c-accent)44', color:'var(--c-accent)', borderRadius:'var(--r-md)', padding:'8px 12px', fontSize:12 }}>
          {n.message}{n.ruleId && <span> — rule {n.ruleId}</span>}
        </div>
      ))}
    </div>
  );
}

// ── Private chat realtime client (Phase 7, doc 08 §9) ──────────────────────
// Same ws-token exchange, reconnect-with-backoff, and msg_id-dedup pattern
// as useRealtimeRoom, but for the conv:{id} topic: MESSAGE, TYPING, READ.
function useRealtimeConversation(conversationId) {
  const [connected, setConnected] = useState(false);
  const [liveMessages, setLiveMessages] = useState([]);
  const [typingUser, setTypingUser] = useState(null);
  const [readUpTo, setReadUpTo] = useState(0);

  const wsRef = useRef(null);
  const lastSeqRef = useRef(0);
  const attemptRef = useRef(0);
  const seenRef = useRef([]);
  const stoppedRef = useRef(false);
  const timerRef = useRef(null);
  const typingTimerRef = useRef(null);

  const dedup = useCallback((msgId) => {
    if (!msgId) return false;
    if (seenRef.current.includes(msgId)) return true;
    seenRef.current.push(msgId);
    if (seenRef.current.length > 500) seenRef.current.shift();
    return false;
  }, []);

  const applyEvent = useCallback((ev) => {
    if (dedup(ev.msg_id)) return;
    if (ev.seq) lastSeqRef.current = Math.max(lastSeqRef.current, ev.seq);
    if (ev.type === 'MESSAGE') {
      setLiveMessages(m => [...m.slice(-199), ev.payload]);
    } else if (ev.type === 'TYPING') {
      setTypingUser(ev.payload.user_id);
      clearTimeout(typingTimerRef.current);
      typingTimerRef.current = setTimeout(() => setTypingUser(null), 3000);
    } else if (ev.type === 'READ') {
      setReadUpTo(ev.payload.up_to_seq || 0);
    }
  }, [dedup]);

  const connect = useCallback(async () => {
    if (stoppedRef.current || !_token || !conversationId) return;
    try {
      const tokData = await api('GET', '/auth/ws-token?device=web', null, true);
      const proto = location.protocol === 'https:' ? 'wss' : 'ws';
      const ws = new WebSocket(proto + '://' + location.host + '/ws?token=' + tokData.ws_token);
      wsRef.current = ws;
      const topic = 'conv:' + conversationId;

      ws.onopen = () => {
        attemptRef.current = 0;
        setConnected(true);
        ws.send(JSON.stringify({ type: 'SUBSCRIBE', topic }));
      };
      ws.onmessage = (msg) => {
        const ev = JSON.parse(msg.data);
        if (ev.type === 'SUBSCRIBED' || ev.type === 'BACKFILL_RESULT' || ev.type === 'CATCHUP_REQUIRED') return;
        if (ev.type === 'ERROR') return;
        applyEvent(ev);
      };
      ws.onclose = () => {
        setConnected(false);
        if (!stoppedRef.current) scheduleReconnect();
      };
      ws.onerror = () => ws.close();
    } catch (e) {
      scheduleReconnect();
    }
  }, [conversationId, applyEvent]);

  const scheduleReconnect = useCallback(() => {
    attemptRef.current += 1;
    const base = Math.min(60000, Math.pow(2, attemptRef.current) * 500);
    const jitter = (Math.random() - 0.5) * base;
    timerRef.current = setTimeout(connect, Math.max(250, base + jitter));
  }, [connect]);

  const send = useCallback((type, payload) => {
    if (wsRef.current && wsRef.current.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify({ type, topic: 'conv:' + conversationId, payload: payload || {} }));
    }
  }, [conversationId]);

  useEffect(() => {
    if (!conversationId) return;
    stoppedRef.current = false;
    setLiveMessages([]);
    connect();
    return () => {
      stoppedRef.current = true;
      clearTimeout(timerRef.current);
      clearTimeout(typingTimerRef.current);
      if (wsRef.current) wsRef.current.close();
    };
  }, [conversationId]); // eslint-disable-line react-hooks/exhaustive-deps

  return { connected, liveMessages, typingUser, readUpTo, send };
}

// AttachmentBubble renders one message's attachment — an inline image when
// the content type is image/*, otherwise a plain download link. Fetches
// its own metadata and bytes (via attachmentObjectURL, since a plain <img>
// src can't carry an Authorization header) rather than the parent needing
// to know anything about attachment shape.
function AttachmentBubble({ attachmentId }) {
  const [meta, setMeta] = useState(null);
  const [url, setUrl] = useState(null);
  const [err, setErr] = useState('');

  useEffect(() => {
    let cancelled = false;
    let objectUrl = null;
    api('GET', '/attachments/' + attachmentId).then(async m => {
      if (cancelled) return;
      setMeta(m);
      try {
        objectUrl = await attachmentObjectURL(attachmentId);
        if (!cancelled) setUrl(objectUrl);
      } catch (e) { if (!cancelled) setErr('Could not load attachment'); }
    }).catch(() => { if (!cancelled) setErr('Attachment unavailable'); });
    return () => { cancelled = true; if (objectUrl) URL.revokeObjectURL(objectUrl); };
  }, [attachmentId]);

  if (err) return <p style={{ fontSize:11, color:'var(--c-text3)', fontStyle:'italic' }}>{err}</p>;
  if (!meta || !url) return <p style={{ fontSize:11, color:'var(--c-text3)' }}>Loading attachment…</p>;

  if (meta.content_type && meta.content_type.startsWith('image/')) {
    return <img src={url} alt={meta.filename} style={{ maxWidth:200, maxHeight:200, borderRadius:'var(--r-md)', display:'block' }} />;
  }
  return (
    <a href={url} download={meta.filename} style={{ fontSize:12, color:'var(--c-primary2)', textDecoration:'underline' }}>
      📎 {meta.filename || 'Download attachment'}
    </a>
  );
}

function ConversationThread({ conversation, account, onBack }) {
  const otherID = conversation.participant_a === account.id ? conversation.participant_b : conversation.participant_a;
  const [history, setHistory] = useState([]);
  const [draft, setDraft] = useState('');
  const [pendingAttachment, setPendingAttachment] = useState(null);
  const [attachBusy, setAttachBusy] = useState(false);
  const [attachErr, setAttachErr] = useState('');
  const fileInputRef = useRef(null);
  const rt = useRealtimeConversation(conversation.id);
  const typingTimerRef = useRef(null);

  async function handleFileSelect(e) {
    const file = e.target.files && e.target.files[0];
    e.target.value = ''; // allow re-selecting the same file later
    if (!file) return;
    setAttachErr('');
    setAttachBusy(true);
    try {
      const result = await uploadAttachment(file);
      if (result.status === 'blocked') {
        setAttachErr(result.block_reason || 'This file was blocked.');
      } else {
        setPendingAttachment(result);
      }
    } catch (e2) { setAttachErr(e2.message || 'Upload failed.'); }
    finally { setAttachBusy(false); }
  }

  useEffect(() => {
    api('GET', '/conversations/' + conversation.id + '/messages').then(d => setHistory(d.items || [])).catch(() => {});
  }, [conversation.id]);

  // Live WS messages already use the same snake_case field names as REST
  // history (sender_id, body) — only the id key differs (msg_id on the
  // envelope vs id on the REST Message), so that's the only remap needed.
  const allMessages = [...history, ...rt.liveMessages.map(m => ({ ...m, id: m.msg_id }))];
  const seen = new Set();
  const merged = allMessages.filter(m => {
    if (seen.has(m.id)) return false;
    seen.add(m.id);
    return true;
  });

  function sendMessage(e) {
    e.preventDefault();
    if (!draft.trim() && !pendingAttachment) return;
    rt.send('MESSAGE', {
      body: draft.trim(), client_msg_id: 'web-' + Date.now() + '-' + Math.random().toString(36).slice(2),
      attachment_id: pendingAttachment ? pendingAttachment.attachment_id : undefined,
    });
    setDraft('');
    setPendingAttachment(null);
    setAttachErr('');
  }

  function handleTyping(e) {
    setDraft(e.target.value);
    rt.send('TYPING');
  }

  return (
    <div style={{ display:'flex', flexDirection:'column', height:400 }}>
      <div style={{ display:'flex', alignItems:'center', gap:8, marginBottom:10 }}>
        <button onClick={onBack} style={{ color:'var(--c-text2)', fontSize:13 }}>← Back</button>
        <strong>{otherID}</strong>
        <span style={{ marginLeft:'auto', fontSize:11, color: rt.connected ? 'var(--c-success)' : 'var(--c-text3)' }}>
          {rt.connected ? '● connected' : '○ reconnecting…'}
        </span>
      </div>
      <div style={{ flex:1, overflowY:'auto', background:'var(--c-surface2)', borderRadius:'var(--r-md)', padding:12, display:'flex', flexDirection:'column', gap:6 }}>
        {merged.length === 0 && <span style={{ color:'var(--c-text3)', margin:'auto', fontSize:13 }}>No messages yet.</span>}
        {merged.map((m, i) => (
          <div key={m.id || i} style={{ alignSelf: m.sender_id === account.id ? 'flex-end' : 'flex-start', maxWidth:'75%', display:'flex', flexDirection:'column', gap:4 }}>
            {m.attachment_id && <AttachmentBubble attachmentId={m.attachment_id} />}
            {m.body && (
              <div style={{ background: m.sender_id === account.id ? 'var(--c-primary)' : 'var(--c-surface)', borderRadius:'var(--r-md)', padding:'6px 10px', fontSize:13 }}>
                {m.body}
              </div>
            )}
          </div>
        ))}
        {rt.typingUser && rt.typingUser !== account.id && (
          <span style={{ fontSize:11, color:'var(--c-text3)', fontStyle:'italic' }}>{rt.typingUser} is typing…</span>
        )}
      </div>
      {attachErr && <p style={{ fontSize:11, color:'var(--c-live)', marginTop:6 }}>{attachErr}</p>}
      {pendingAttachment && (
        <div style={{ display:'flex', alignItems:'center', gap:8, marginTop:6, fontSize:12, color:'var(--c-text2)' }}>
          <span>📎 {pendingAttachment.filename}</span>
          <button type="button" onClick={() => setPendingAttachment(null)} style={{ color:'var(--c-live)' }}>✕</button>
        </div>
      )}
      <form onSubmit={sendMessage} style={{ display:'flex', gap:8, marginTop:10 }}>
        <input ref={fileInputRef} type="file" style={{ display:'none' }} onChange={handleFileSelect} />
        <button type="button" onClick={() => fileInputRef.current && fileInputRef.current.click()} disabled={attachBusy || !!pendingAttachment}
          style={{ ...css.btn('secondary'), padding:'10px 12px' }} title="Attach a file">
          {attachBusy ? '…' : '📎'}
        </button>
        <input style={{ ...css.input, flex:1 }} placeholder="Message…" value={draft} onChange={handleTyping} />
        <button type="submit" style={{ ...css.btn('primary'), padding:'10px 16px' }}>Send</button>
      </form>
    </div>
  );
}

function MessagesModal({ account, onClose }) {
  const [folder, setFolder] = useState('inbox');
  const [conversations, setConversations] = useState([]);
  const [selected, setSelected] = useState(null);
  const [newTo, setNewTo] = useState('');

  const load = useCallback(() => {
    api('GET', '/conversations?folder=' + folder).then(d => setConversations(d.items || [])).catch(() => {});
  }, [folder]);

  useEffect(() => { load(); }, [load]);

  async function startNew(e) {
    e.preventDefault();
    if (!newTo.trim()) return;
    try {
      const d = await api('POST', '/conversations', { other_id: newTo.trim() });
      setNewTo('');
      load();
      setSelected(d.conversation);
    } catch (err) {
      alert(err.message || 'Could not start conversation.');
    }
  }

  async function accept(id) {
    await api('POST', '/conversations/' + id + '/accept', {});
    load();
  }
  async function decline(id) {
    await api('POST', '/conversations/' + id + '/decline', {});
    load();
  }

  return (
    <div style={{ position:'fixed', inset:0, background:'rgba(0,0,0,0.85)', zIndex:1000, display:'flex', alignItems:'center', justifyContent:'center', padding:20 }} onClick={onClose}>
      <div style={{ background:'var(--c-surface)', borderRadius:'var(--r-xl)', width:'100%', maxWidth:480, padding:20 }} onClick={e => e.stopPropagation()}>
        <div style={{ display:'flex', justifyContent:'space-between', alignItems:'center', marginBottom:16 }}>
          <h2 style={{ fontWeight:800 }}>✉️ Messages</h2>
          <button onClick={onClose} style={{ color:'var(--c-text2)' }}>✕</button>
        </div>

        {selected ? (
          <ConversationThread conversation={selected} account={account} onBack={() => { setSelected(null); load(); }} />
        ) : (
          <>
            <div style={{ display:'flex', gap:4, marginBottom:12 }}>
              <button onClick={() => setFolder('inbox')} style={{ padding:'6px 14px', borderRadius:'99px', fontSize:12, fontWeight: folder==='inbox'?700:500, background: folder==='inbox'?'var(--c-primary)':'var(--c-surface2)' }}>Inbox</button>
              <button onClick={() => setFolder('requests')} style={{ padding:'6px 14px', borderRadius:'99px', fontSize:12, fontWeight: folder==='requests'?700:500, background: folder==='requests'?'var(--c-primary)':'var(--c-surface2)' }}>Requests</button>
            </div>

            <form onSubmit={startNew} style={{ display:'flex', gap:8, marginBottom:14 }}>
              <input style={{ ...css.input, flex:1 }} placeholder="Start a chat — enter an account id (e.g. acc-0002)" value={newTo} onChange={e => setNewTo(e.target.value)} />
              <button type="submit" style={{ ...css.btn('primary'), padding:'10px 14px' }}>Start</button>
            </form>

            <div style={{ display:'flex', flexDirection:'column', gap:4, maxHeight:320, overflowY:'auto' }}>
              {conversations.length === 0 && <p style={{ color:'var(--c-text3)', fontSize:13, textAlign:'center', padding:20 }}>Nothing here yet.</p>}
              {conversations.map(c => {
                const other = c.conversation.participant_a === account.id ? c.conversation.participant_b : c.conversation.participant_a;
                return (
                  <div key={c.conversation.id} style={{ display:'flex', alignItems:'center', gap:10, padding:10, borderRadius:'var(--r-md)', background:'var(--c-surface2)' }}>
                    <div style={{ width:32, height:32, borderRadius:'50%', background:'var(--c-primary)', display:'flex', alignItems:'center', justifyContent:'center', fontSize:13, fontWeight:700, cursor:'pointer' }} onClick={() => setSelected(c.conversation)}>
                      {other[0]}
                    </div>
                    <div style={{ flex:1, cursor:'pointer' }} onClick={() => setSelected(c.conversation)}>
                      <p style={{ fontWeight:600, fontSize:13 }}>{other}</p>
                      <p style={{ color:'var(--c-text3)', fontSize:11 }}>{c.last_message ? c.last_message.body : 'No messages yet'}</p>
                    </div>
                    {c.unread_count > 0 && <span style={{ ...css.pill('var(--c-accent)'), fontSize:10 }}>{c.unread_count}</span>}
                    {folder === 'requests' && (
                      <div style={{ display:'flex', gap:4 }}>
                        <button onClick={() => accept(c.conversation.id)} style={{ fontSize:11, padding:'4px 8px', borderRadius:'99px', background:'var(--c-success)22', color:'var(--c-success)' }}>Accept</button>
                        <button onClick={() => decline(c.conversation.id)} style={{ fontSize:11, padding:'4px 8px', borderRadius:'99px', background:'var(--c-accent)22', color:'var(--c-accent)' }}>Decline</button>
                      </div>
                    )}
                  </div>
                );
              })}
            </div>
          </>
        )}
      </div>
    </div>
  );
}

// ── Tab bar ─────────────────────────────────────────────────────────────────
function TabBar({ active, onChange }) {
  const tabs = [
    { id:'for_you', label:'For You' },
    { id:'hot', label:'🔥 Hot' },
    { id:'explore', label:'Explore' },
    { id:'following', label:'Following' },
  ];
  return (
    <div style={{ display:'flex', gap:4, borderBottom:'1px solid var(--c-border)', paddingBottom:0, marginBottom:20, overflowX:'auto' }}>
      {tabs.map(t => (
        <button key={t.id} onClick={() => onChange(t.id)} style={{ padding:'10px 18px', fontWeight: active===t.id ? 700 : 500, color: active===t.id ? 'var(--c-primary2)' : 'var(--c-text2)', borderBottom: active===t.id ? '2px solid var(--c-primary)' : '2px solid transparent', background:'none', fontSize:14, whiteSpace:'nowrap', transition:'color 0.15s' }}>
          {t.label}
        </button>
      ))}
    </div>
  );
}

// ── Auth modal ───────────────────────────────────────────────────────────────
function AuthModal({ onSuccess, onClose }) {
  const [mode, setMode] = useState('login'); // login | register
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);

  async function submit(e) {
    e.preventDefault();
    setLoading(true); setError('');
    try {
      const path = mode === 'register' ? '/auth/email/register' : '/auth/email/login';
      const data = await api('POST', path, { email, password, device_id: 'web-' + Date.now() }, false);
      setToken(data.access_token);
      onSuccess(data.account);
    } catch(err) {
      setError(err.message || 'Authentication failed.');
    } finally {
      setLoading(false);
    }
  }

  return (
    <div style={{ position:'fixed', inset:0, background:'rgba(0,0,0,0.85)', zIndex:2000, display:'flex', alignItems:'center', justifyContent:'center', padding:20 }} onClick={onClose}>
      <div style={{ background:'var(--c-surface)', borderRadius:'var(--r-xl)', padding:32, width:'100%', maxWidth:400 }} onClick={e => e.stopPropagation()}>
        {/* Logo */}
        <div style={{ textAlign:'center', marginBottom:24 }}>
          <div style={{ fontSize:32, marginBottom:8 }}>⚡</div>
          <h1 style={{ fontSize:24, fontWeight:800, background:'linear-gradient(135deg,var(--c-primary),var(--c-accent))', WebkitBackgroundClip:'text', WebkitTextFillColor:'transparent' }}>Lumena Live</h1>
          <p style={{ color:'var(--c-text2)', fontSize:13, marginTop:4 }}>Watch, connect, and go live</p>
        </div>
        {/* Tab */}
        <div style={{ display:'flex', gap:4, marginBottom:20, background:'var(--c-surface2)', borderRadius:'var(--r-md)', padding:4 }}>
          {['login','register'].map(m => (
            <button key={m} onClick={() => setMode(m)} style={{ flex:1, padding:'8px', borderRadius:'var(--r-sm)', background: mode===m ? 'var(--c-primary)' : 'transparent', color:'var(--c-text)', fontWeight: mode===m ? 700 : 500, fontSize:14, transition:'background 0.15s' }}>
              {m === 'login' ? 'Sign In' : 'Register'}
            </button>
          ))}
        </div>
        <form onSubmit={submit} style={{ display:'flex', flexDirection:'column', gap:12 }}>
          <input style={css.input} type="email" placeholder="Email address" value={email} onChange={e => setEmail(e.target.value)} required />
          <input style={css.input} type="password" placeholder="Password (8+ chars)" value={password} onChange={e => setPassword(e.target.value)} required minLength={8} />
          {error && <div style={{ background:'#ff5c7a22', border:'1px solid #ff5c7a44', borderRadius:'var(--r-sm)', padding:'10px 14px', color:'var(--c-accent)', fontSize:13 }}>{error}</div>}
          <button type="submit" style={{ ...css.btn(), opacity: loading ? 0.7 : 1 }} disabled={loading}>
            {loading ? '…' : mode === 'login' ? 'Sign In' : 'Create Account'}
          </button>
        </form>
        <p style={{ textAlign:'center', color:'var(--c-text3)', fontSize:12, marginTop:16 }}>
          {mode === 'login' ? "Don't have an account? " : "Already have an account? "}
          <button style={{ color:'var(--c-primary2)', fontWeight:600, fontSize:12 }} onClick={() => setMode(mode === 'login' ? 'register' : 'login')}>
            {mode === 'login' ? 'Register' : 'Sign In'}
          </button>
        </p>
      </div>
    </div>
  );
}

// ── Search bar ───────────────────────────────────────────────────────────────
function SearchBar({ onResults }) {
  const [q, setQ] = useState('');
  const [loading, setLoading] = useState(false);
  const timerRef = useRef(null);

  function handleChange(e) {
    const val = e.target.value;
    setQ(val);
    clearTimeout(timerRef.current);
    if (val.length < 2) { onResults(null); return; }
    timerRef.current = setTimeout(async () => {
      setLoading(true);
      try {
        const data = await api('GET', '/search?q=' + encodeURIComponent(val), null, false);
        onResults(data.items || []);
      } catch { onResults([]); }
      finally { setLoading(false); }
    }, 350);
  }

  return (
    <div style={{ position:'relative' }}>
      <input style={{ ...css.input, paddingLeft:38 }} placeholder="Search streams, creators…" value={q} onChange={handleChange} />
      <span style={{ position:'absolute', left:12, top:'50%', transform:'translateY(-50%)', color:'var(--c-text3)', pointerEvents:'none' }}>
        {loading ? '⟳' : '🔍'}
      </span>
      {q && <button style={{ position:'absolute', right:12, top:'50%', transform:'translateY(-50%)', color:'var(--c-text3)', fontSize:16 }} onClick={() => { setQ(''); onResults(null); }}>×</button>}
    </div>
  );
}

// ── Main App ─────────────────────────────────────────────────────────────────
// ── Creator dashboard modal (Phase 14) ───────────────────────────────────────
function CreatorDashboardModal({ onClose }) {
  const [tab, setTab] = useState('earnings');
  const [kyc, setKyc] = useState(null);
  const [earnings, setEarnings] = useState(null);
  const [analytics, setAnalytics] = useState(null);
  const [payouts, setPayouts] = useState([]);
  const [legalName, setLegalName] = useState('');
  const [country, setCountry] = useState('US');
  const [payoutAmount, setPayoutAmount] = useState('');
  const [profile, setProfile] = useState(null);
  const [payoutCountries, setPayoutCountries] = useState([]);
  const [profileMethod, setProfileMethod] = useState('bank_transfer');
  const [profileCountry, setProfileCountry] = useState('PK');
  const [profilePayeeName, setProfilePayeeName] = useState('');
  const [profileDestination, setProfileDestination] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');

  const load = useCallback(() => {
    api('GET', '/me/creator/kyc').then(setKyc).catch(() => {});
    api('GET', '/me/creator/earnings').then(setEarnings).catch(() => {});
    api('GET', '/me/creator/analytics').then(setAnalytics).catch(() => {});
    api('GET', '/me/creator/payouts').then(d => setPayouts(d.items || [])).catch(() => {});
    api('GET', '/me/creator/payout-profile').then(setProfile).catch(() => {});
    api('GET', '/payout-countries').then(d => setPayoutCountries(d.items || [])).catch(() => {});
  }, []);

  useEffect(() => { load(); }, [load]);

  async function submitKyc(e) {
    e.preventDefault();
    if (!legalName.trim()) return;
    setBusy(true); setErr('');
    try {
      await api('POST', '/me/creator/kyc', { legal_name: legalName.trim(), country });
      load();
    } catch (e2) { setErr(e2.message || 'Could not submit KYC.'); }
    finally { setBusy(false); }
  }

  async function devApprove() {
    setBusy(true); setErr('');
    try {
      await api('POST', '/me/creator/kyc/dev-approve', {});
      load();
    } catch (e2) { setErr(e2.message || 'Could not approve KYC.'); }
    finally { setBusy(false); }
  }

  async function requestPayout(e) {
    e.preventDefault();
    const amount = parseInt(payoutAmount, 10);
    if (!amount || amount <= 0) return;
    setBusy(true); setErr('');
    try {
      await api('POST', '/me/creator/payouts', { amount_diamonds: amount }, true, {
        'Idempotency-Key': 'payout-' + Date.now() + '-' + Math.random().toString(36).slice(2),
      });
      setPayoutAmount('');
      load();
    } catch (e2) { setErr(e2.message || 'Could not request payout.'); }
    finally { setBusy(false); }
  }

  async function saveProfile(e) {
    e.preventDefault();
    if (!profilePayeeName.trim() || !profileDestination.trim() || !profileCountry.trim()) return;
    setBusy(true); setErr('');
    try {
      await api('PUT', '/me/creator/payout-profile', {
        method: profileMethod, country: profileCountry.trim(),
        payee_name: profilePayeeName.trim(), destination: profileDestination.trim(),
      });
      setProfilePayeeName(''); setProfileDestination('');
      load();
    } catch (e2) { setErr(e2.message || 'Could not save payout profile.'); }
    finally { setBusy(false); }
  }

  const statusColor = { none:'var(--c-text3)', pending:'var(--c-accent)', verified:'var(--c-success)', rejected:'var(--c-live)' };
  const payoutColor = {
    requested:'var(--c-text3)', approved:'var(--c-accent)', processing:'var(--c-accent)',
    paid:'var(--c-success)', completed:'var(--c-success)', rejected:'var(--c-live)', failed:'var(--c-live)',
  };
  const hasProfile = !!(profile && profile.method);
  const selectedCountry = payoutCountries.find(c => c.country_code === profileCountry);
  const methodLabels = { bank_transfer: 'Bank transfer', mobile_wallet: 'Mobile wallet (JazzCash/Easypaisa)', paypal: 'PayPal' };
  const availableMethods = selectedCountry ? selectedCountry.available_methods : [];

  return (
    <div style={{ position:'fixed', inset:0, background:'rgba(0,0,0,0.85)', zIndex:1000, display:'flex', alignItems:'center', justifyContent:'center', padding:20 }} onClick={onClose}>
      <div style={{ background:'var(--c-surface)', borderRadius:'var(--r-xl)', width:'100%', maxWidth:560, maxHeight:'85vh', overflowY:'auto', padding:20 }} onClick={e => e.stopPropagation()}>
        <div style={{ display:'flex', justifyContent:'space-between', alignItems:'center', marginBottom:16 }}>
          <h2 style={{ fontWeight:800 }}>💰 Creator Dashboard</h2>
          <button onClick={onClose} style={{ color:'var(--c-text2)' }}>✕</button>
        </div>

        <div style={{ display:'flex', gap:4, marginBottom:16 }}>
          {['earnings','analytics','payout','kyc'].map(t => (
            <button key={t} onClick={() => setTab(t)} style={{ padding:'6px 14px', borderRadius:'99px', fontSize:12, fontWeight: tab===t?700:500, background: tab===t?'var(--c-primary)':'var(--c-surface2)', textTransform:'capitalize' }}>{t}</button>
          ))}
        </div>

        {err && <p style={{ color:'var(--c-accent)', fontSize:12, marginBottom:12 }}>{err}</p>}

        {tab === 'earnings' && earnings && (
          <div style={{ display:'flex', flexDirection:'column', gap:12 }}>
            <div style={{ display:'grid', gridTemplateColumns:'1fr 1fr', gap:10 }}>
              <StatBox label="Total earned" value={earnings.total_earned_diamonds + ' 💎'} />
              <StatBox label="Current balance" value={earnings.current_balance_diamonds + ' 💎'} />
              <StatBox label="Available (unlocked)" value={earnings.available_diamonds + ' 💎'} highlight />
              <StatBox label="Locked (< 14d hold)" value={earnings.locked_diamonds + ' 💎'} />
            </div>
            <p style={{ fontWeight:700, fontSize:13, marginTop:8 }}>Gift breakdown</p>
            {(earnings.gift_breakdown || []).length === 0
              ? <p style={{ color:'var(--c-text3)', fontSize:12 }}>No gifts received yet.</p>
              : earnings.gift_breakdown.map(g => (
                <div key={g.gift_id} style={{ display:'flex', justifyContent:'space-between', fontSize:13, padding:'6px 0', borderBottom:'1px solid var(--c-border)' }}>
                  <span>{g.gift_id} × {g.count}</span>
                  <span>{g.total_diamonds} 💎</span>
                </div>
              ))
            }
          </div>
        )}

        {tab === 'analytics' && analytics && (
          <div style={{ display:'flex', flexDirection:'column', gap:12 }}>
            <div style={{ display:'grid', gridTemplateColumns:'1fr 1fr', gap:10 }}>
              <StatBox label="Total streams" value={analytics.streams.total_streams} />
              <StatBox label="Avg peak viewers" value={analytics.streams.avg_peak_viewers.toFixed(1)} />
              <StatBox label="Total followers" value={analytics.followers.total_followers} />
              <StatBox label="New (7d)" value={'+' + analytics.followers.new_last_7_days} />
            </div>
          </div>
        )}

        {tab === 'kyc' && kyc && (
          <div style={{ display:'flex', flexDirection:'column', gap:12 }}>
            <p style={{ fontSize:13 }}>Status: <span style={{ fontWeight:700, color: statusColor[kyc.status] || 'var(--c-text)', textTransform:'capitalize' }}>{kyc.status}</span></p>
            {kyc.status !== 'verified' && (
              <form onSubmit={submitKyc} style={{ display:'flex', flexDirection:'column', gap:10 }}>
                <input style={css.input} placeholder="Legal name" value={legalName} onChange={e => setLegalName(e.target.value)} />
                <input style={css.input} placeholder="Country (e.g. US)" value={country} onChange={e => setCountry(e.target.value)} />
                <button style={css.btn()} disabled={busy || !legalName.trim()}>Submit KYC</button>
              </form>
            )}
            {kyc.status === 'pending' && (
              <div style={{ background:'var(--c-surface2)', borderRadius:'var(--r-md)', padding:12 }}>
                <p style={{ fontSize:12, color:'var(--c-text2)', marginBottom:8 }}>No real identity/sanctions-screening vendor is wired into this dev build (doc 02 ID-09). Use this dev-only button to simulate approval.</p>
                <button style={css.btn('secondary')} onClick={devApprove} disabled={busy}>🛠 Dev: Approve KYC</button>
              </div>
            )}
          </div>
        )}

        {tab === 'payout' && (
          <div style={{ display:'flex', flexDirection:'column', gap:16 }}>
            {earnings && (
              <p style={{ fontSize:12, color:'var(--c-text2)' }}>
                Available to withdraw: <strong>{earnings.available_diamonds} 💎</strong> (minimum {10000} 💎, 14-day hold on new earnings, KYC required).
              </p>
            )}

            <div style={{ background:'var(--c-surface2)', borderRadius:'var(--r-md)', padding:12 }}>
              <p style={{ fontWeight:700, fontSize:13, marginBottom:8 }}>Payout profile</p>
              {hasProfile ? (
                <p style={{ fontSize:12, color:'var(--c-text2)' }}>
                  {profile.method.replace('_',' ')} · {profile.country} · {profile.payee_name} · ****{profile.destination.slice(-4)}
                </p>
              ) : (
                <p style={{ fontSize:12, color:'var(--c-accent)', marginBottom:8 }}>Set where withdrawals should be sent before requesting one — an admin sends payouts manually to this destination.</p>
              )}
              <form onSubmit={saveProfile} style={{ display:'flex', flexDirection:'column', gap:8, marginTop:8 }}>
                <select style={css.input} value={profileCountry} onChange={e => { setProfileCountry(e.target.value); const c = payoutCountries.find(c => c.country_code === e.target.value); if (c && c.available_methods && c.available_methods.length) setProfileMethod(c.available_methods[0]); }}>
                  {payoutCountries.filter(c => c.enabled).map(c => (
                    <option key={c.country_code} value={c.country_code}>{c.country_code} ({c.currency})</option>
                  ))}
                </select>
                <select style={css.input} value={profileMethod} onChange={e => setProfileMethod(e.target.value)}>
                  {availableMethods.map(m => (
                    <option key={m} value={m}>{methodLabels[m] || m}</option>
                  ))}
                </select>
                <input style={css.input} placeholder="Payee name" value={profilePayeeName} onChange={e => setProfilePayeeName(e.target.value)} />
                <input style={css.input} placeholder={profileMethod === 'paypal' ? 'PayPal email' : profileMethod === 'mobile_wallet' ? 'Mobile wallet number' : 'Bank account number / IBAN'} value={profileDestination} onChange={e => setProfileDestination(e.target.value)} />
                <button style={css.btn('secondary')} disabled={busy || !profilePayeeName.trim() || !profileDestination.trim() || !availableMethods.length}>{hasProfile ? 'Update profile' : 'Save profile'}</button>
              </form>
            </div>

            <form onSubmit={requestPayout} style={{ display:'flex', gap:8 }}>
              <input style={css.input} type="number" min="1" placeholder="Amount (diamonds)" value={payoutAmount} onChange={e => setPayoutAmount(e.target.value)} disabled={!hasProfile} />
              <button style={css.btn()} disabled={busy || !payoutAmount || !hasProfile}>Request</button>
            </form>
            <div>
              <p style={{ fontWeight:700, fontSize:13, marginBottom:8 }}>Payout history</p>
              {payouts.length === 0
                ? <p style={{ color:'var(--c-text3)', fontSize:12 }}>No payouts requested yet.</p>
                : payouts.map(p => (
                  <div key={p.id} style={{ display:'flex', flexDirection:'column', gap:2, padding:'6px 0', borderBottom:'1px solid var(--c-border)' }}>
                    <div style={{ display:'flex', justifyContent:'space-between', fontSize:13 }}>
                      <span>{p.amount_diamonds} 💎</span>
                      <span style={{ color: payoutColor[p.status] || 'var(--c-text)', fontWeight:600, textTransform:'capitalize' }}>{p.status}</span>
                    </div>
                    {p.payout_reference && <span style={{ fontSize:11, color:'var(--c-text3)' }}>ref: {p.payout_reference}</span>}
                    {p.failure_reason && <span style={{ fontSize:11, color:'var(--c-live)' }}>{p.failure_reason}</span>}
                  </div>
                ))
              }
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

function StatBox({ label, value, highlight }) {
  return (
    <div style={{ background:'var(--c-surface2)', borderRadius:'var(--r-md)', padding:'10px 14px', border: highlight ? '1px solid var(--c-success)44' : 'none' }}>
      <p style={{ fontSize:11, color:'var(--c-text3)', marginBottom:4 }}>{label}</p>
      <p style={{ fontSize:18, fontWeight:800, color: highlight ? 'var(--c-success)' : 'var(--c-text)' }}>{value}</p>
    </div>
  );
}

// ── Report modal (MODR_001, Phase 15) ───────────────────────────────────────
function ReportModal({ subjectType, subjectId, onClose }) {
  const [reason, setReason] = useState('harassment');
  const [detail, setDetail] = useState('');
  const [caseId, setCaseId] = useState('');
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);

  async function submit(e) {
    e.preventDefault();
    setBusy(true); setErr('');
    try {
      const d = await api('POST', '/reports', { subject_type: subjectType, subject_id: subjectId, reason_code: reason, detail });
      setCaseId(d.case_id);
    } catch (e2) { setErr(e2.message || 'Could not submit report.'); }
    finally { setBusy(false); }
  }

  return (
    <div style={{ position:'fixed', inset:0, background:'rgba(0,0,0,0.9)', zIndex:2000, display:'flex', alignItems:'center', justifyContent:'center', padding:20 }} onClick={onClose}>
      <div style={{ background:'var(--c-surface)', borderRadius:'var(--r-xl)', padding:24, width:'100%', maxWidth:380 }} onClick={e => e.stopPropagation()}>
        <div style={{ display:'flex', justifyContent:'space-between', alignItems:'center', marginBottom:12 }}>
          <h2 style={{ fontWeight:800 }}>🚩 Report</h2>
          <button onClick={onClose} style={{ color:'var(--c-text2)' }}>✕</button>
        </div>
        {caseId ? (
          <div>
            <p style={{ fontSize:13, color:'var(--c-success)', fontWeight:600, marginBottom:8 }}>Report submitted.</p>
            <p style={{ fontSize:12, color:'var(--c-text2)' }}>Case ID: <strong>{caseId}</strong></p>
            <button style={{ ...css.btn(), marginTop:16, width:'100%' }} onClick={onClose}>Done</button>
          </div>
        ) : (
          <form onSubmit={submit} style={{ display:'flex', flexDirection:'column', gap:10 }}>
            <select style={css.input} value={reason} onChange={e => setReason(e.target.value)}>
              <option value="harassment">Harassment or bullying</option>
              <option value="sexual_content">Sexual content</option>
              <option value="spam">Spam</option>
              <option value="grooming_pattern">Grooming / unsafe behavior toward a minor</option>
              <option value="other">Other</option>
            </select>
            <textarea style={{ ...css.input, minHeight:70, resize:'vertical' }} placeholder="Additional detail (optional)" value={detail} onChange={e => setDetail(e.target.value)} />
            {err && <p style={{ color:'var(--c-accent)', fontSize:12 }}>{err}</p>}
            <button style={css.btn()} disabled={busy}>Submit report</button>
          </form>
        )}
      </div>
    </div>
  );
}

// ── Moderation console (MODR_002/003 + moderator queue, Phase 15) ──────────
function ModerationConsoleModal({ onClose }) {
  const [tab, setTab] = useState('notices');
  const [isMod, setIsMod] = useState(false);
  const [enforcements, setEnforcements] = useState([]);
  const [queue, setQueue] = useState([]);
  const [riskQueue, setRiskQueue] = useState([]);
  const [rules, setRules] = useState([]);
  const [appealText, setAppealText] = useState({});
  const [err, setErr] = useState('');

  const load = useCallback(() => {
    api('GET', '/me/enforcements').then(d => setEnforcements(d.items || [])).catch(() => {});
    api('GET', '/moderation/queue').then(d => { setQueue(d.items || []); setIsMod(true); }).catch(() => setIsMod(false));
    api('GET', '/moderation/risk-queue').then(d => setRiskQueue(d.items || [])).catch(() => {});
  }, []);

  useEffect(() => { load(); }, [load]);

  useEffect(() => {
    // The public rule catalogue (GET /policies/rules) had a complete
    // backend and no page anywhere in the app that showed it to users.
    if (tab === 'rules' && rules.length === 0) {
      api('GET', '/policies/rules', null, false).then(d => setRules(d.items || [])).catch(() => {});
    }
  }, [tab]); // eslint-disable-line react-hooks/exhaustive-deps

  async function becomeModerator() {
    try {
      await api('POST', '/moderation/dev-grant', {});
      load();
    } catch (e) { setErr(e.message || 'Could not grant moderator access.'); }
  }

  async function appeal(enforcementId) {
    try {
      await api('POST', '/me/enforcements/' + enforcementId + '/appeal', { statement: appealText[enforcementId] || '' });
      load();
    } catch (e) { setErr(e.message || 'Could not file appeal.'); }
  }

  async function triage(reportId, state) {
    try {
      await api('POST', '/moderation/reports/' + reportId + '/triage', { state });
      load();
    } catch (e) { setErr(e.message || 'Could not triage report.'); }
  }

  return (
    <div style={{ position:'fixed', inset:0, background:'rgba(0,0,0,0.85)', zIndex:1000, display:'flex', alignItems:'center', justifyContent:'center', padding:20 }} onClick={onClose}>
      <div style={{ background:'var(--c-surface)', borderRadius:'var(--r-xl)', width:'100%', maxWidth:560, maxHeight:'85vh', overflowY:'auto', padding:20 }} onClick={e => e.stopPropagation()}>
        <div style={{ display:'flex', justifyContent:'space-between', alignItems:'center', marginBottom:16 }}>
          <h2 style={{ fontWeight:800 }}>🛡 Trust &amp; Safety</h2>
          <button onClick={onClose} style={{ color:'var(--c-text2)' }}>✕</button>
        </div>

        <div style={{ display:'flex', gap:4, marginBottom:16, flexWrap:'wrap' }}>
          <button onClick={() => setTab('notices')} style={{ padding:'6px 14px', borderRadius:'99px', fontSize:12, fontWeight: tab==='notices'?700:500, background: tab==='notices'?'var(--c-primary)':'var(--c-surface2)' }}>My Notices</button>
          {isMod && <button onClick={() => setTab('queue')} style={{ padding:'6px 14px', borderRadius:'99px', fontSize:12, fontWeight: tab==='queue'?700:500, background: tab==='queue'?'var(--c-primary)':'var(--c-surface2)' }}>Report Queue</button>}
          {isMod && <button onClick={() => setTab('risk')} style={{ padding:'6px 14px', borderRadius:'99px', fontSize:12, fontWeight: tab==='risk'?700:500, background: tab==='risk'?'var(--c-primary)':'var(--c-surface2)' }}>Risk Queue</button>}
          <button onClick={() => setTab('rules')} style={{ padding:'6px 14px', borderRadius:'99px', fontSize:12, fontWeight: tab==='rules'?700:500, background: tab==='rules'?'var(--c-primary)':'var(--c-surface2)' }}>Community Guidelines</button>
        </div>

        {err && <p style={{ color:'var(--c-accent)', fontSize:12, marginBottom:12 }}>{err}</p>}

        {!isMod && (
          <div style={{ background:'var(--c-surface2)', borderRadius:'var(--r-md)', padding:12, marginBottom:16 }}>
            <p style={{ fontSize:12, color:'var(--c-text2)', marginBottom:8 }}>There is no RBAC/admin system yet (roadmap Phase 16). Dev-only button to try the moderator console.</p>
            <button style={css.btn('secondary')} onClick={becomeModerator}>🛠 Dev: Become a moderator</button>
          </div>
        )}

        {tab === 'notices' && (
          <div style={{ display:'flex', flexDirection:'column', gap:12 }}>
            {enforcements.length === 0
              ? <p style={{ color:'var(--c-text3)', fontSize:12 }}>No enforcement notices — you're in good standing.</p>
              : enforcements.map(e => (
                <div key={e.id} style={{ background:'var(--c-surface2)', borderRadius:'var(--r-md)', padding:12 }}>
                  <p style={{ fontWeight:700, fontSize:13 }}>{e.rule_id} — {e.action}</p>
                  <p style={{ fontSize:11, color:'var(--c-text3)', marginTop:2 }}>Case {e.case_id} · decided by {e.decided_by}{e.duration_hours ? ' · ' + e.duration_hours + 'h' : ''}</p>
                  {e.appealable ? (
                    <div style={{ display:'flex', gap:6, marginTop:8 }}>
                      <input style={{ ...css.input, fontSize:12, padding:'6px 10px' }} placeholder="Appeal statement…" value={appealText[e.id] || ''} onChange={ev => setAppealText(t => ({ ...t, [e.id]: ev.target.value }))} />
                      <button style={{ ...css.btn('secondary'), fontSize:12, padding:'6px 12px' }} onClick={() => appeal(e.id)}>Appeal</button>
                    </div>
                  ) : (
                    <p style={{ fontSize:11, color:'var(--c-text3)', marginTop:6 }}>Not appealable.</p>
                  )}
                </div>
              ))
            }
          </div>
        )}

        {tab === 'queue' && isMod && (
          <div style={{ display:'flex', flexDirection:'column', gap:10 }}>
            {queue.length === 0
              ? <p style={{ color:'var(--c-text3)', fontSize:12 }}>Queue is empty.</p>
              : queue.map(r => (
                <div key={r.id} style={{ background:'var(--c-surface2)', borderRadius:'var(--r-md)', padding:12 }}>
                  <p style={{ fontSize:13 }}><strong>{r.reason_code}</strong> — {r.subject_type} {r.subject_id}</p>
                  <p style={{ fontSize:11, color:'var(--c-text3)', margin:'4px 0' }}>{r.detail || 'No detail provided.'} · Case {r.case_id} · {r.state}</p>
                  {r.state === 'open' && (
                    <div style={{ display:'flex', gap:6, marginTop:6 }}>
                      <button style={{ ...css.btn('secondary'), fontSize:11, padding:'4px 10px' }} onClick={() => triage(r.id, 'triaged')}>Triage</button>
                      <button style={{ ...css.btn('secondary'), fontSize:11, padding:'4px 10px' }} onClick={() => triage(r.id, 'dismissed')}>Dismiss</button>
                    </div>
                  )}
                </div>
              ))
            }
          </div>
        )}

        {tab === 'risk' && isMod && (
          <div style={{ display:'flex', flexDirection:'column', gap:10 }}>
            <p style={{ fontSize:11, color:'var(--c-text3)' }}>Signals only — never an automated ban (doc 10 §2d).</p>
            {riskQueue.length === 0
              ? <p style={{ color:'var(--c-text3)', fontSize:12 }}>No flagged accounts.</p>
              : riskQueue.map(p => (
                <div key={p.account_id} style={{ background:'var(--c-surface2)', borderRadius:'var(--r-md)', padding:12 }}>
                  <p style={{ fontSize:13 }}><strong>{p.account_id}</strong> — score {p.risk_score} ({p.review_status})</p>
                  <p style={{ fontSize:11, color:'var(--c-text3)', marginTop:4 }}>{(p.risk_reasons || []).join(', ')}</p>
                </div>
              ))
            }
          </div>
        )}

        {tab === 'rules' && (
          <div style={{ display:'flex', flexDirection:'column', gap:10 }}>
            {rules.length === 0
              ? <p style={{ color:'var(--c-text3)', fontSize:12 }}>Loading…</p>
              : rules.slice().sort((a, b) => b.severity - a.severity).map(r => (
                <div key={r.id} style={{ background:'var(--c-surface2)', borderRadius:'var(--r-md)', padding:12 }}>
                  <p style={{ fontWeight:700, fontSize:13 }}>{r.title} <span style={{ fontWeight:400, color:'var(--c-text3)', fontSize:11 }}>({r.id})</span></p>
                  <p style={{ fontSize:12, color:'var(--c-text2)', marginTop:4 }}>{r.description}</p>
                  {r.public_url && <a href={r.public_url} target="_blank" rel="noreferrer" style={{ fontSize:11, color:'var(--c-primary2)', marginTop:4, display:'inline-block' }}>Learn more →</a>}
                </div>
              ))
            }
          </div>
        )}
      </div>
    </div>
  );
}

// ── Support tickets modal (SUPP_001-003, BT-08, Phase 16) ───────────────────
function SupportModal({ onClose }) {
  const [tickets, setTickets] = useState([]);
  const [selected, setSelected] = useState(null);
  const [messages, setMessages] = useState([]);
  const [subject, setSubject] = useState('');
  const [category, setCategory] = useState('billing');
  const [body, setBody] = useState('');
  const [reply, setReply] = useState('');
  const [err, setErr] = useState('');

  const load = useCallback(() => {
    api('GET', '/support/tickets').then(d => setTickets(d.items || [])).catch(() => {});
  }, []);
  useEffect(() => { load(); }, [load]);

  async function openTicket(t) {
    setSelected(t);
    try {
      const d = await api('GET', '/support/tickets/' + t.id);
      setMessages(d.messages || []);
    } catch (e) { setErr(e.message || 'Could not load ticket.'); }
  }

  async function create(e) {
    e.preventDefault();
    if (!subject.trim() || !body.trim()) return;
    try {
      await api('POST', '/support/tickets', { subject, category, body });
      setSubject(''); setBody('');
      load();
    } catch (e2) { setErr(e2.message || 'Could not create ticket.'); }
  }

  async function sendReply(e) {
    e.preventDefault();
    if (!reply.trim()) return;
    try {
      await api('POST', '/support/tickets/' + selected.id + '/messages', { body: reply });
      setReply('');
      openTicket(selected);
      load();
    } catch (e2) { setErr(e2.message || 'Could not send reply.'); }
  }

  const statusColor = { open:'var(--c-accent)', pending:'var(--c-primary2)', resolved:'var(--c-success)' };

  return (
    <div style={{ position:'fixed', inset:0, background:'rgba(0,0,0,0.85)', zIndex:1000, display:'flex', alignItems:'center', justifyContent:'center', padding:20 }} onClick={onClose}>
      <div style={{ background:'var(--c-surface)', borderRadius:'var(--r-xl)', width:'100%', maxWidth:480, maxHeight:'85vh', overflowY:'auto', padding:20 }} onClick={e => e.stopPropagation()}>
        <div style={{ display:'flex', justifyContent:'space-between', alignItems:'center', marginBottom:16 }}>
          <h2 style={{ fontWeight:800 }}>🎫 Support</h2>
          <button onClick={() => selected ? setSelected(null) : onClose()} style={{ color:'var(--c-text2)' }}>{selected ? '← Back' : '✕'}</button>
        </div>

        {err && <p style={{ color:'var(--c-accent)', fontSize:12, marginBottom:12 }}>{err}</p>}

        {!selected ? (
          <div style={{ display:'flex', flexDirection:'column', gap:16 }}>
            <form onSubmit={create} style={{ display:'flex', flexDirection:'column', gap:8, background:'var(--c-surface2)', borderRadius:'var(--r-md)', padding:12 }}>
              <p style={{ fontSize:12, fontWeight:700 }}>New ticket</p>
              <input style={css.input} placeholder="Subject" value={subject} onChange={e => setSubject(e.target.value)} />
              <select style={css.input} value={category} onChange={e => setCategory(e.target.value)}>
                <option value="billing">Missing coins / billing</option>
                <option value="account">Account issue</option>
                <option value="bug">Bug report</option>
                <option value="other">Other</option>
              </select>
              <textarea style={{ ...css.input, minHeight:60, resize:'vertical' }} placeholder="Describe the issue…" value={body} onChange={e => setBody(e.target.value)} />
              <button style={css.btn()}>Submit ticket</button>
            </form>
            <div>
              <p style={{ fontSize:12, fontWeight:700, marginBottom:8 }}>My tickets</p>
              {tickets.length === 0
                ? <p style={{ color:'var(--c-text3)', fontSize:12 }}>No tickets yet.</p>
                : tickets.map(t => (
                  <div key={t.id} style={{ display:'flex', justifyContent:'space-between', alignItems:'center', padding:'8px 0', borderBottom:'1px solid var(--c-border)', cursor:'pointer' }} onClick={() => openTicket(t)}>
                    <span style={{ fontSize:13 }}>{t.subject}</span>
                    <span style={{ fontSize:11, color: statusColor[t.status], fontWeight:600, textTransform:'capitalize' }}>{t.status}</span>
                  </div>
                ))
              }
            </div>
          </div>
        ) : (
          <div style={{ display:'flex', flexDirection:'column', gap:12 }}>
            <p style={{ fontWeight:700 }}>{selected.subject}</p>
            <div style={{ display:'flex', flexDirection:'column', gap:8, maxHeight:260, overflowY:'auto' }}>
              {messages.map(m => (
                <div key={m.id} style={{ alignSelf: m.is_admin ? 'flex-start' : 'flex-end', background: m.is_admin ? 'var(--c-surface2)' : 'var(--c-primary)33', borderRadius:'var(--r-md)', padding:'8px 12px', maxWidth:'80%' }}>
                  <p style={{ fontSize:11, color:'var(--c-text3)', marginBottom:2 }}>{m.is_admin ? 'Support' : 'You'}</p>
                  <p style={{ fontSize:13 }}>{m.body}</p>
                </div>
              ))}
            </div>
            {selected.status !== 'resolved' && (
              <form onSubmit={sendReply} style={{ display:'flex', gap:8 }}>
                <input style={css.input} placeholder="Reply…" value={reply} onChange={e => setReply(e.target.value)} />
                <button style={css.btn()}>Send</button>
              </form>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

// ── Admin console modal (RBAC-gated, Phase 16) ──────────────────────────────
function AdminConsoleModal({ onClose }) {
  const [role, setRole] = useState(null);
  const [tab, setTab] = useState('accounts');
  const [lookupId, setLookupId] = useState('');
  const [account, setAccount2] = useState(null);
  const [anomalies, setAnomalies] = useState([]);
  const [fraud, setFraud] = useState([]);
  const [tickets, setTickets] = useState([]);
  const [health, setHealth] = useState(null);
  const [auditLog, setAuditLog] = useState([]);
  const [dauMau, setDauMau] = useState(null);
  const [funnel, setFunnel] = useState([]);
  const [economy, setEconomy] = useState(null);
  const [retention, setRetention] = useState([]);
  const [payouts, setPayouts] = useState([]);
  const [payoutStatusFilter, setPayoutStatusFilter] = useState('');
  const [payoutRefDraft, setPayoutRefDraft] = useState({});
  const [lookupTxId, setLookupTxId] = useState('');
  const [txView, setTxView] = useState(null);
  const [refundReason, setRefundReason] = useState('');
  const [refundResult, setRefundResult] = useState(null);
  const [err, setErr] = useState('');

  const load = useCallback(() => {
    api('GET', '/admin/me/role').then(d => setRole(d.role)).catch(() => {});
  }, []);
  useEffect(() => { load(); }, [load]);

  const loadPayouts = useCallback(() => {
    api('GET', '/admin/payouts' + (payoutStatusFilter ? '?status=' + payoutStatusFilter : '')).then(d => setPayouts(d.items || [])).catch(e => setErr(e.message));
  }, [payoutStatusFilter]);

  useEffect(() => {
    if (!role) return;
    if (tab === 'economy') api('GET', '/admin/economy/anomalies').then(d => setAnomalies(d.items || [])).catch(e => setErr(e.message));
    if (tab === 'fraud') api('GET', '/admin/fraud/queue').then(d => setFraud(d.items || [])).catch(e => setErr(e.message));
    if (tab === 'support') api('GET', '/admin/support/queue').then(d => setTickets(d.items || [])).catch(e => setErr(e.message));
    if (tab === 'health') api('GET', '/admin/health').then(setHealth).catch(e => setErr(e.message));
    if (tab === 'audit') api('GET', '/admin/audit-log').then(d => setAuditLog(d.items || [])).catch(e => setErr(e.message));
    if (tab === 'withdrawals') loadPayouts();
    if (tab === 'analytics') {
      api('GET', '/analytics/dau-mau').then(setDauMau).catch(e => setErr(e.message));
      api('GET', '/analytics/funnel').then(d => setFunnel(d.items || [])).catch(e => setErr(e.message));
      api('GET', '/analytics/economy?range=30').then(setEconomy).catch(e => setErr(e.message));
      api('GET', '/analytics/retention').then(d => setRetention(d.items || [])).catch(e => setErr(e.message));
    }
  }, [tab, role, loadPayouts]);

  async function payoutAction(id, action, body) {
    try {
      await api('POST', '/admin/payouts/' + id + '/' + action, body || {});
      loadPayouts();
    } catch (e) { setErr(e.message || 'Could not update payout.'); }
  }

  async function lookupTransaction(e) {
    e.preventDefault();
    if (!lookupTxId.trim()) return;
    setRefundResult(null);
    try {
      const tv = await api('GET', '/admin/transactions/' + lookupTxId.trim());
      setTxView(tv);
      setErr('');
    } catch (e2) { setErr(e2.message || 'Transaction not found.'); setTxView(null); }
  }

  async function reverseTransaction() {
    if (!txView || !refundReason.trim()) return;
    try {
      const tv = await api('POST', '/admin/transactions/' + txView.id + '/reverse', { reason: refundReason.trim() }, true, {
        'Idempotency-Key': 'refund-' + txView.id + '-' + Date.now(),
      });
      setRefundResult(tv);
      setRefundReason('');
      setErr('');
    } catch (e2) { setErr(e2.message || 'Could not reverse transaction.'); }
  }

  async function grant(r) {
    try {
      await api('POST', '/admin/dev-grant', { role: r });
      load();
    } catch (e) { setErr(e.message || 'Could not grant role.'); }
  }

  async function lookupAccount(e) {
    e.preventDefault();
    if (!lookupId.trim()) return;
    try {
      const a = await api('GET', '/admin/accounts/' + lookupId.trim());
      setAccount2(a);
      setErr('');
    } catch (e2) { setErr(e2.message || 'Account not found.'); setAccount2(null); }
  }

  async function restrict() {
    try {
      await api('POST', '/admin/accounts/' + account.account_id + '/restrict', { reason: 'admin console action' });
      lookupAccount({ preventDefault(){} });
    } catch (e) { setErr(e.message || 'Could not restrict.'); }
  }
  async function unsuspend() {
    try {
      await api('POST', '/admin/accounts/' + account.account_id + '/unsuspend', {});
      lookupAccount({ preventDefault(){} });
    } catch (e) { setErr(e.message || 'Could not unsuspend.'); }
  }
  async function resolveTicket(id) {
    try {
      await api('POST', '/admin/support/tickets/' + id + '/resolve', {});
      api('GET', '/admin/support/queue').then(d => setTickets(d.items || []));
    } catch (e) { setErr(e.message || 'Could not resolve.'); }
  }

  const tabs = [
    { id:'accounts', label:'Accounts', perm:'accounts.manage' },
    { id:'economy', label:'Economy', perm:'economy.view' },
    { id:'fraud', label:'Fraud', perm:'fraud.review' },
    { id:'support', label:'Support', perm:'support.manage' },
    { id:'health', label:'Health', perm:'health.view' },
    { id:'audit', label:'Audit Log', perm:'audit.view' },
    { id:'analytics', label:'Analytics', perm:'analytics.view' },
    { id:'withdrawals', label:'Withdrawals', perm:'payout.manage' },
    { id:'refunds', label:'Refunds', perm:'refund.manage' },
  ];

  return (
    <div style={{ position:'fixed', inset:0, background:'rgba(0,0,0,0.85)', zIndex:1000, display:'flex', alignItems:'center', justifyContent:'center', padding:20 }} onClick={onClose}>
      <div style={{ background:'var(--c-surface)', borderRadius:'var(--r-xl)', width:'100%', maxWidth:600, maxHeight:'85vh', overflowY:'auto', padding:20 }} onClick={e => e.stopPropagation()}>
        <div style={{ display:'flex', justifyContent:'space-between', alignItems:'center', marginBottom:16 }}>
          <h2 style={{ fontWeight:800 }}>⚙️ Admin Console</h2>
          <button onClick={onClose} style={{ color:'var(--c-text2)' }}>✕</button>
        </div>

        {!role ? (
          <div style={{ background:'var(--c-surface2)', borderRadius:'var(--r-md)', padding:12 }}>
            <p style={{ fontSize:12, color:'var(--c-text2)', marginBottom:8 }}>There is no staff IdP/SSO yet — dev-only role grant for trying the RBAC-gated console. Each role only unlocks its own tabs.</p>
            <div style={{ display:'flex', gap:6, flexWrap:'wrap' }}>
              {['support','finance','trust_safety','superadmin'].map(r => (
                <button key={r} style={css.btn('secondary')} onClick={() => grant(r)}>🛠 {r}</button>
              ))}
            </div>
          </div>
        ) : (
          <>
            <p style={{ fontSize:11, color:'var(--c-text3)', marginBottom:12 }}>Signed in as role: <strong style={{ color:'var(--c-primary2)' }}>{role}</strong></p>
            <div style={{ display:'flex', gap:4, marginBottom:16, flexWrap:'wrap' }}>
              {tabs.map(t => (
                <button key={t.id} onClick={() => setTab(t.id)} style={{ padding:'6px 14px', borderRadius:'99px', fontSize:12, fontWeight: tab===t.id?700:500, background: tab===t.id?'var(--c-primary)':'var(--c-surface2)' }}>{t.label}</button>
              ))}
            </div>

            {err && <p style={{ color:'var(--c-accent)', fontSize:12, marginBottom:12 }}>{err}</p>}

            {tab === 'accounts' && (
              <div style={{ display:'flex', flexDirection:'column', gap:12 }}>
                <form onSubmit={lookupAccount} style={{ display:'flex', gap:8 }}>
                  <input style={css.input} placeholder="Account ID (e.g. acc-0002)" value={lookupId} onChange={e => setLookupId(e.target.value)} />
                  <button style={css.btn('secondary')}>Look up</button>
                </form>
                {account && (
                  <div style={{ background:'var(--c-surface2)', borderRadius:'var(--r-md)', padding:12 }}>
                    <p style={{ fontSize:13 }}>{account.account_id} — <strong>{account.status}</strong></p>
                    <p style={{ fontSize:11, color:'var(--c-text3)', marginTop:2 }}>age: {account.age_status} · created {new Date(account.created_at).toLocaleDateString()}</p>
                    <div style={{ display:'flex', gap:8, marginTop:8 }}>
                      <button style={css.btn('secondary')} onClick={restrict}>Restrict</button>
                      <button style={css.btn('secondary')} onClick={unsuspend}>Unsuspend</button>
                    </div>
                  </div>
                )}
              </div>
            )}

            {tab === 'economy' && (
              <div style={{ display:'flex', flexDirection:'column', gap:10 }}>
                <p style={{ fontSize:11, color:'var(--c-text3)' }}>Read-only — gift-loop / circular-flow detection (AF-03). Admin never edits ledger entries directly.</p>
                {anomalies.length === 0
                  ? <p style={{ color:'var(--c-text3)', fontSize:12 }}>No anomalies detected.</p>
                  : anomalies.map((a, i) => (
                    <div key={i} style={{ background:'var(--c-surface2)', borderRadius:'var(--r-md)', padding:12 }}>
                      <p style={{ fontSize:13, fontWeight:700 }}>{a.type}</p>
                      <p style={{ fontSize:12, color:'var(--c-text2)', marginTop:4 }}>{a.detail}</p>
                    </div>
                  ))
                }
              </div>
            )}

            {tab === 'fraud' && (
              <div style={{ display:'flex', flexDirection:'column', gap:10 }}>
                {fraud.length === 0
                  ? <p style={{ color:'var(--c-text3)', fontSize:12 }}>No flagged accounts.</p>
                  : fraud.map(f => (
                    <div key={f.account_id} style={{ background:'var(--c-surface2)', borderRadius:'var(--r-md)', padding:12 }}>
                      <p style={{ fontSize:13 }}><strong>{f.account_id}</strong> — score {f.risk_score} ({f.review_status})</p>
                      <p style={{ fontSize:11, color:'var(--c-text3)', marginTop:4 }}>{(f.risk_reasons || []).join(', ')}</p>
                    </div>
                  ))
                }
              </div>
            )}

            {tab === 'support' && (
              <div style={{ display:'flex', flexDirection:'column', gap:10 }}>
                {tickets.length === 0
                  ? <p style={{ color:'var(--c-text3)', fontSize:12 }}>Queue is empty.</p>
                  : tickets.map(t => (
                    <div key={t.id} style={{ background:'var(--c-surface2)', borderRadius:'var(--r-md)', padding:12 }}>
                      <p style={{ fontSize:13 }}><strong>{t.subject}</strong> — {t.status}</p>
                      <p style={{ fontSize:11, color:'var(--c-text3)', margin:'4px 0' }}>{t.account_id} · SLA {new Date(t.sla_deadline).toLocaleString()}</p>
                      {t.status !== 'resolved' && <button style={{ ...css.btn('secondary'), fontSize:11, padding:'4px 10px' }} onClick={() => resolveTicket(t.id)}>Resolve</button>}
                    </div>
                  ))
                }
              </div>
            )}

            {tab === 'health' && health && (
              <div style={{ display:'grid', gridTemplateColumns:'1fr 1fr', gap:10 }}>
                <StatBox label="Active streams" value={health.active_streams} />
                <StatBox label="Open reports" value={health.open_reports} />
                <StatBox label="Flagged (risk)" value={health.flagged_risk_accounts} />
                <StatBox label="Pending payouts" value={health.pending_payouts} />
                <StatBox label="Open tickets" value={health.open_tickets} />
                <StatBox label="Ledger sum (should be 0)" value={Object.values(health.ledger_integrity || {}).reduce((a,b)=>a+b,0)} />
              </div>
            )}

            {tab === 'audit' && (
              <div style={{ display:'flex', flexDirection:'column', gap:8 }}>
                {auditLog.length === 0
                  ? <p style={{ color:'var(--c-text3)', fontSize:12 }}>No audit entries yet.</p>
                  : auditLog.map(a => (
                    <div key={a.id} style={{ fontSize:12, padding:'6px 0', borderBottom:'1px solid var(--c-border)' }}>
                      <strong>{a.actor_id}</strong> ({a.actor_role}) {a.action} → {a.target_type}:{a.target_id}
                      <span style={{ color:'var(--c-text3)' }}> · {new Date(a.created_at).toLocaleString()}</span>
                    </div>
                  ))
                }
              </div>
            )}

            {tab === 'analytics' && (
              <div style={{ display:'flex', flexDirection:'column', gap:16 }}>
                {dauMau && (
                  <div style={{ display:'grid', gridTemplateColumns:'1fr 1fr', gap:10 }}>
                    <StatBox label="DAU" value={dauMau.dau} />
                    <StatBox label="MAU" value={dauMau.mau} />
                  </div>
                )}
                <div>
                  <p style={{ fontSize:12, fontWeight:700, marginBottom:8 }}>Onboarding funnel</p>
                  {funnel.map(f => (
                    <div key={f.name} style={{ display:'flex', justifyContent:'space-between', fontSize:12, padding:'4px 0', borderBottom:'1px solid var(--c-border)' }}>
                      <span style={{ textTransform:'capitalize' }}>{f.name.replace(/_/g,' ')}</span>
                      <span>{f.count} ({f.pct_of_first.toFixed(0)}%)</span>
                    </div>
                  ))}
                </div>
                {retention.length > 0 && (
                  <div>
                    <p style={{ fontSize:12, fontWeight:700, marginBottom:8 }}>Retention (7d-ago cohort)</p>
                    {retention.map(p => (
                      <div key={p.days_after} style={{ display:'flex', justifyContent:'space-between', fontSize:12, padding:'4px 0', borderBottom:'1px solid var(--c-border)' }}>
                        <span>Day {p.days_after}</span>
                        <span>{p.retained_count}/{p.cohort_size} ({p.retained_pct.toFixed(0)}%)</span>
                      </div>
                    ))}
                  </div>
                )}
                {economy && (
                  <div>
                    <p style={{ fontSize:12, fontWeight:700, marginBottom:8 }}>Economy (last {economy.range_days}d)</p>
                    <div style={{ display:'grid', gridTemplateColumns:'1fr 1fr', gap:10 }}>
                      <StatBox label="Gifts sent" value={economy.total_gifts_sent} />
                      <StatBox label="Diamonds issued" value={economy.total_diamonds_issued} />
                      <StatBox label="Coins purchased" value={economy.total_coins_purchased} />
                      <StatBox label="Purchases" value={economy.total_purchases} />
                    </div>
                  </div>
                )}
              </div>
            )}

            {tab === 'withdrawals' && (
              <div style={{ display:'flex', flexDirection:'column', gap:10 }}>
                <p style={{ fontSize:11, color:'var(--c-text3)' }}>Manual payout MVP — approve, execute the transfer out-of-band, then mark paid with a reference. Rejecting or failing a payout returns the held diamonds to the creator's available balance via a ledger reversal, never a direct balance edit.</p>
                <div style={{ display:'flex', gap:6, flexWrap:'wrap' }}>
                  {['', 'requested', 'approved', 'processing', 'paid', 'rejected', 'failed'].map(s => (
                    <button key={s || 'all'} onClick={() => setPayoutStatusFilter(s)} style={{ padding:'4px 10px', borderRadius:'99px', fontSize:11, fontWeight: payoutStatusFilter===s?700:500, background: payoutStatusFilter===s?'var(--c-primary)':'var(--c-surface2)', textTransform:'capitalize' }}>{s || 'all'}</button>
                  ))}
                </div>
                {payouts.length === 0
                  ? <p style={{ color:'var(--c-text3)', fontSize:12 }}>No payouts in this filter.</p>
                  : payouts.map(p => (
                    <div key={p.id} style={{ background:'var(--c-surface2)', borderRadius:'var(--r-md)', padding:12, display:'flex', flexDirection:'column', gap:6 }}>
                      <div style={{ display:'flex', justifyContent:'space-between', fontSize:13 }}>
                        <span><strong>{p.account_id}</strong> — {p.amount_diamonds} 💎</span>
                        <span style={{ textTransform:'capitalize', fontWeight:600 }}>{p.status}</span>
                      </div>
                      <p style={{ fontSize:11, color:'var(--c-text3)' }}>Requested {new Date(p.requested_at).toLocaleString()}{p.reviewed_by ? ' · reviewed by ' + p.reviewed_by : ''}</p>
                      {p.payout_reference && <p style={{ fontSize:11, color:'var(--c-text2)' }}>ref: {p.payout_reference}</p>}
                      {p.failure_reason && <p style={{ fontSize:11, color:'var(--c-live)' }}>{p.failure_reason}</p>}
                      <div style={{ display:'flex', gap:6, flexWrap:'wrap' }}>
                        {p.status === 'requested' && (
                          <>
                            <button style={{ ...css.btn('secondary'), fontSize:11, padding:'4px 10px' }} onClick={() => payoutAction(p.id, 'approve', {})}>Approve</button>
                            <button style={{ ...css.btn('secondary'), fontSize:11, padding:'4px 10px', color:'var(--c-live)' }} onClick={() => { const reason = prompt('Reason for rejecting this payout?'); if (reason) payoutAction(p.id, 'reject', { reason }); }}>Reject</button>
                          </>
                        )}
                        {p.status === 'approved' && (
                          <button style={{ ...css.btn('secondary'), fontSize:11, padding:'4px 10px' }} onClick={() => payoutAction(p.id, 'processing', {})}>Mark processing</button>
                        )}
                        {p.status === 'processing' && (
                          <>
                            <input style={{ ...css.input, fontSize:11, padding:'4px 8px', width:140 }} placeholder="Payout reference" value={payoutRefDraft[p.id] || ''} onChange={e => setPayoutRefDraft({ ...payoutRefDraft, [p.id]: e.target.value })} />
                            <button style={{ ...css.btn('secondary'), fontSize:11, padding:'4px 10px' }} disabled={!payoutRefDraft[p.id]} onClick={() => payoutAction(p.id, 'paid', { payout_reference: payoutRefDraft[p.id] })}>Mark paid</button>
                            <button style={{ ...css.btn('secondary'), fontSize:11, padding:'4px 10px', color:'var(--c-live)' }} onClick={() => { const reason = prompt('Reason the payout failed?'); if (reason) payoutAction(p.id, 'failed', { reason }); }}>Mark failed</button>
                          </>
                        )}
                      </div>
                    </div>
                  ))
                }
              </div>
            )}

            {tab === 'refunds' && (
              <div style={{ display:'flex', flexDirection:'column', gap:12 }}>
                <p style={{ fontSize:11, color:'var(--c-text3)' }}>Look up a ledger transaction by ID and, if warranted, reverse it. A reversal posts a compensating transaction — the original is never edited or deleted — and fails cleanly if the funds have already been spent elsewhere.</p>
                <form onSubmit={lookupTransaction} style={{ display:'flex', gap:8 }}>
                  <input style={css.input} placeholder="Transaction ID (e.g. tx-12)" value={lookupTxId} onChange={e => setLookupTxId(e.target.value)} />
                  <button style={css.btn('secondary')}>Look up</button>
                </form>
                {txView && (
                  <div style={{ background:'var(--c-surface2)', borderRadius:'var(--r-md)', padding:12, display:'flex', flexDirection:'column', gap:8 }}>
                    <p style={{ fontSize:13 }}><strong>{txView.id}</strong> — {txView.kind}</p>
                    <p style={{ fontSize:11, color:'var(--c-text3)' }}>{new Date(txView.created_at).toLocaleString()}</p>
                    {(txView.entries || []).map((e, i) => (
                      <div key={i} style={{ display:'flex', justifyContent:'space-between', fontSize:12 }}>
                        <span>{e.account_id}</span>
                        <span style={{ color: e.amount < 0 ? 'var(--c-live)' : 'var(--c-success)' }}>{e.amount > 0 ? '+' : ''}{e.amount} {e.currency}</span>
                      </div>
                    ))}
                    <div style={{ display:'flex', gap:8, marginTop:4 }}>
                      <input style={css.input} placeholder="Reason for refund" value={refundReason} onChange={e => setRefundReason(e.target.value)} />
                      <button style={{ ...css.btn('secondary'), color:'var(--c-live)' }} disabled={!refundReason.trim()} onClick={reverseTransaction}>Reverse</button>
                    </div>
                  </div>
                )}
                {refundResult && (
                  <div style={{ background:'var(--c-surface2)', borderRadius:'var(--r-md)', padding:12 }}>
                    <p style={{ fontSize:13, color:'var(--c-success)', fontWeight:600 }}>Reversed — new transaction {refundResult.id}</p>
                  </div>
                )}
              </div>
            )}
          </>
        )}
      </div>
    </div>
  );
}

// ── Profile modal ───────────────────────────────────────────────────────────
// Clicking a host's name/avatar previously did nothing anywhere in the app,
// despite the full profile/social API (GetProfile, followers/following,
// block/unblock) having existed since Phase 3.
function ProfileModal({ accountID, viewerAccount, onClose, onOpenProfile }) {
  const [data, setData] = useState(null);
  const [err, setErr] = useState('');
  const [tab, setTab] = useState(null); // null | 'followers' | 'following'
  const [list, setList] = useState([]);
  const [listLoading, setListLoading] = useState(false);
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    api('GET', '/users/' + accountID, null, !!viewerAccount)
      .then(setData)
      .catch(e => setErr(e.message || 'This profile is not available.'));
  }, [accountID, viewerAccount]);
  useEffect(() => { load(); }, [load]);

  useEffect(() => {
    if (!tab) return;
    setListLoading(true);
    api('GET', '/users/' + accountID + '/' + tab, null, !!viewerAccount)
      .then(d => setList(d.items || []))
      .catch(() => setList([]))
      .finally(() => setListLoading(false));
  }, [tab, accountID, viewerAccount]);

  async function toggleFollow() {
    if (!viewerAccount || busy) return;
    setBusy(true);
    try {
      const d = data.relationship?.following
        ? await api('DELETE', '/follows/' + accountID)
        : await api('POST', '/follows', { followee_id: accountID });
      setData(v => ({ ...v, relationship: d.relationship }));
    } catch (e) { alert(e.message || 'Could not update follow status.'); }
    finally { setBusy(false); }
  }

  async function toggleBlock() {
    if (!viewerAccount || busy) return;
    setBusy(true);
    try {
      if (data.relationship?.blocked) {
        await api('DELETE', '/blocks/' + accountID);
      } else {
        await api('POST', '/blocks', { blocked_id: accountID });
      }
      load();
    } catch (e) { alert(e.message || 'Could not update block status.'); }
    finally { setBusy(false); }
  }

  return (
    <div style={{ position:'fixed', inset:0, background:'rgba(0,0,0,0.85)', zIndex:1200, display:'flex', alignItems:'center', justifyContent:'center', padding:20 }} onClick={onClose}>
      <div style={{ background:'var(--c-surface)', borderRadius:'var(--r-xl)', width:'100%', maxWidth:420, maxHeight:'85vh', overflowY:'auto', padding:24 }} onClick={e => e.stopPropagation()}>
        <div style={{ display:'flex', justifyContent:'flex-end' }}>
          <button onClick={onClose} style={{ color:'var(--c-text2)' }}>✕</button>
        </div>

        {err && <p style={{ color:'var(--c-accent)', fontSize:13, textAlign:'center', padding:20 }}>{err}</p>}

        {data && !tab && (
          <div style={{ display:'flex', flexDirection:'column', alignItems:'center', gap:10, marginTop:-8 }}>
            <div style={{ width:72, height:72, borderRadius:'50%', background:'var(--c-primary)', display:'flex', alignItems:'center', justifyContent:'center', fontSize:28, fontWeight:700 }}>
              {data.profile.display_name?.[0] || '?'}
            </div>
            <p style={{ fontWeight:800, fontSize:18 }}>{data.profile.display_name}{data.profile.is_creator && ' 💎'}</p>
            {data.profile.handle && <p style={{ color:'var(--c-text3)', fontSize:13 }}>@{data.profile.handle}</p>}
            {data.profile.bio && <p style={{ color:'var(--c-text2)', fontSize:13, textAlign:'center' }}>{data.profile.bio}</p>}

            <div style={{ display:'flex', gap:24, marginTop:8 }}>
              <button style={{ textAlign:'center' }} onClick={() => setTab('followers')}>
                <p style={{ fontWeight:700 }}>{(data.counts?.follower_count ?? 0).toLocaleString()}</p>
                <p style={{ fontSize:11, color:'var(--c-text3)' }}>Followers</p>
              </button>
              <button style={{ textAlign:'center' }} onClick={() => setTab('following')}>
                <p style={{ fontWeight:700 }}>{(data.counts?.following_count ?? 0).toLocaleString()}</p>
                <p style={{ fontSize:11, color:'var(--c-text3)' }}>Following</p>
              </button>
            </div>

            {!data.is_owner && viewerAccount && (
              <div style={{ display:'flex', gap:8, marginTop:12, width:'100%' }}>
                <button style={{ ...(data.relationship?.following ? css.btn('secondary') : css.btn()), flex:1 }} disabled={busy} onClick={toggleFollow}>
                  {data.relationship?.following ? 'Following' : 'Follow'}
                </button>
                <button style={{ ...css.btn('secondary'), flex:1, color: data.relationship?.blocked ? 'var(--c-accent)' : 'var(--c-text)' }} disabled={busy} onClick={toggleBlock}>
                  {data.relationship?.blocked ? 'Unblock' : 'Block'}
                </button>
              </div>
            )}
          </div>
        )}

        {data && tab && (
          <div>
            <button onClick={() => setTab(null)} style={{ color:'var(--c-text2)', fontSize:13, marginBottom:12 }}>← Back</button>
            <p style={{ fontWeight:700, fontSize:14, marginBottom:10, textTransform:'capitalize' }}>{tab}</p>
            {listLoading ? (
              <p style={{ color:'var(--c-text3)', fontSize:12 }}>Loading…</p>
            ) : list.length === 0 ? (
              <p style={{ color:'var(--c-text3)', fontSize:12 }}>No one here yet.</p>
            ) : (
              list.map(entry => (
                <button
                  key={entry.account_id}
                  style={{ display:'flex', alignItems:'center', gap:10, padding:'8px 4px', width:'100%', textAlign:'left', borderBottom:'1px solid var(--c-border)' }}
                  onClick={() => onOpenProfile ? onOpenProfile(entry.account_id) : null}
                >
                  <div style={{ width:32, height:32, borderRadius:'50%', background:'var(--c-primary)', display:'flex', alignItems:'center', justifyContent:'center', fontSize:13, fontWeight:700 }}>
                    {entry.display_name?.[0] || '?'}
                  </div>
                  <span style={{ fontSize:13 }}>{entry.display_name || entry.account_id}</span>
                </button>
              ))
            )}
          </div>
        )}
      </div>
    </div>
  );
}

// ── Match modal (Phase 12) ──────────────────────────────────────────────────
// The whole matchmaking backend (opt-in, server-offered candidates, mutual
// connect/skip, history) has existed since Phase 12 with zero UI anywhere
// in the app — the same class of gap as the Follow button and profile view.
function MatchModal({ onClose, onOpenProfile }) {
  const [tab, setTab] = useState('match');
  const [privacy, setPrivacy] = useState(null);
  const [request, setRequest] = useState(null);
  const [history, setHistory] = useState([]);
  const [err, setErr] = useState('');
  const [minAge, setMinAge] = useState('');
  const [maxAge, setMaxAge] = useState('');
  const [genderPref, setGenderPref] = useState('');
  const [busy, setBusy] = useState(false);
  const pollRef = useRef(null);

  useEffect(() => {
    api('GET', '/me/privacy').then(setPrivacy).catch(() => {});
    // Preferences persist across sessions (GET/PATCH /me/match-preferences
    // previously had a working backend with no UI ever calling it) — prefill
    // the search form with whatever was saved last time.
    api('GET', '/me/match-preferences').then(p => {
      if (p.min_age) setMinAge(String(p.min_age));
      if (p.max_age) setMaxAge(String(p.max_age));
      if (p.gender_preference) setGenderPref(p.gender_preference);
    }).catch(() => {});
  }, []);

  useEffect(() => {
    if (tab !== 'history') return;
    api('GET', '/match/history').then(d => setHistory(d.items || [])).catch(() => {});
  }, [tab]);

  useEffect(() => {
    clearInterval(pollRef.current);
    if (!request || request.state !== 'searching') return;
    pollRef.current = setInterval(() => {
      api('GET', '/match/requests/' + request.request_id).then(setRequest).catch(() => {});
    }, 2000);
    return () => clearInterval(pollRef.current);
  }, [request?.request_id, request?.state]); // eslint-disable-line react-hooks/exhaustive-deps

  async function toggleMatchable() {
    if (!privacy) return;
    try {
      const updated = await api('PATCH', '/me/privacy', { ...privacy, matchable: !privacy.matchable });
      setPrivacy(updated);
    } catch (e) { setErr(e.message || 'Could not update setting.'); }
  }

  async function startMatching(e) {
    e.preventDefault();
    setErr(''); setBusy(true);
    try {
      const preferences = {};
      if (minAge) preferences.min_age = parseInt(minAge, 10);
      if (maxAge) preferences.max_age = parseInt(maxAge, 10);
      if (genderPref) preferences.gender_preference = genderPref;
      await api('PATCH', '/me/match-preferences', preferences).catch(() => {}); // persist for next time; non-fatal if it fails
      setRequest(await api('POST', '/match/requests', { preferences }));
    } catch (e2) { setErr(e2.message || 'Could not start matching.'); }
    finally { setBusy(false); }
  }

  async function cancelRequest() {
    if (!request) return;
    setBusy(true);
    try {
      await api('DELETE', '/match/requests/' + request.request_id);
      setRequest(null);
    } catch (e) { setErr(e.message || 'Could not cancel.'); }
    finally { setBusy(false); }
  }

  async function decide(decision) {
    if (!request?.candidate) return;
    setBusy(true);
    try {
      setRequest(await api('POST', '/match/decisions', {
        request_id: request.request_id, candidate_id: request.candidate.candidate_id, decision,
      }));
    } catch (e) { setErr(e.message || 'Could not submit decision.'); }
    finally { setBusy(false); }
  }

  return (
    <div style={{ position:'fixed', inset:0, background:'rgba(0,0,0,0.85)', zIndex:1000, display:'flex', alignItems:'center', justifyContent:'center', padding:20 }} onClick={onClose}>
      <div style={{ background:'var(--c-surface)', borderRadius:'var(--r-xl)', width:'100%', maxWidth:420, maxHeight:'85vh', overflowY:'auto', padding:24 }} onClick={e => e.stopPropagation()}>
        <div style={{ display:'flex', justifyContent:'space-between', alignItems:'center', marginBottom:16 }}>
          <h2 style={{ fontWeight:800 }}>💘 Match</h2>
          <button onClick={onClose} style={{ color:'var(--c-text2)' }}>✕</button>
        </div>

        <div style={{ display:'flex', gap:4, marginBottom:16 }}>
          <button onClick={() => setTab('match')} style={{ padding:'6px 14px', borderRadius:'99px', fontSize:12, fontWeight: tab==='match'?700:500, background: tab==='match'?'var(--c-primary)':'var(--c-surface2)' }}>Match</button>
          <button onClick={() => setTab('history')} style={{ padding:'6px 14px', borderRadius:'99px', fontSize:12, fontWeight: tab==='history'?700:500, background: tab==='history'?'var(--c-primary)':'var(--c-surface2)' }}>History</button>
        </div>

        {err && <p style={{ color:'var(--c-accent)', fontSize:12, marginBottom:12 }}>{err}</p>}

        {tab === 'match' && (
          <div style={{ display:'flex', flexDirection:'column', gap:16 }}>
            <div style={{ display:'flex', justifyContent:'space-between', alignItems:'center', background:'var(--c-surface2)', borderRadius:'var(--r-md)', padding:'10px 14px' }}>
              <span style={{ fontSize:13 }}>Opt in to matching</span>
              <button onClick={toggleMatchable} style={{ width:40, height:22, borderRadius:99, background: privacy?.matchable ? 'var(--c-primary)' : 'var(--c-border)', position:'relative', transition:'background 0.15s' }}>
                <span style={{ position:'absolute', top:2, left: privacy?.matchable ? 20 : 2, width:18, height:18, borderRadius:'50%', background:'#fff', transition:'left 0.15s' }} />
              </button>
            </div>

            {!privacy?.matchable && (
              <p style={{ fontSize:12, color:'var(--c-text3)' }}>Turn matching on to be offered as a candidate to other opted-in users, and to start searching yourself.</p>
            )}

            {!request && (
              <form onSubmit={startMatching} style={{ display:'flex', flexDirection:'column', gap:10 }}>
                <div style={{ display:'flex', gap:8 }}>
                  <input style={css.input} type="number" min="18" placeholder="Min age (optional)" value={minAge} onChange={e => setMinAge(e.target.value)} />
                  <input style={css.input} type="number" min="18" placeholder="Max age (optional)" value={maxAge} onChange={e => setMaxAge(e.target.value)} />
                </div>
                <select style={css.input} value={genderPref} onChange={e => setGenderPref(e.target.value)}>
                  <option value="">Any gender</option>
                  <option value="male">Male</option>
                  <option value="female">Female</option>
                  <option value="nonbinary">Non-binary</option>
                </select>
                <button style={css.btn()} disabled={busy || !privacy?.matchable}>Start Matching</button>
              </form>
            )}

            {request && request.state === 'searching' && !request.candidate && (
              <div style={{ textAlign:'center', padding:24 }}>
                <div style={{ fontSize:32, marginBottom:12 }}>🔍</div>
                <p style={{ fontWeight:700, marginBottom:4 }}>Searching for a match…</p>
                <p style={{ fontSize:12, color:'var(--c-text3)', marginBottom:16 }}>We'll offer you a candidate as soon as one's available.</p>
                <button style={css.btn('secondary')} disabled={busy} onClick={cancelRequest}>Cancel</button>
              </div>
            )}

            {request && request.state === 'searching' && request.candidate && (
              <div style={{ textAlign:'center', padding:16, background:'var(--c-surface2)', borderRadius:'var(--r-lg)' }}>
                <div style={{ width:64, height:64, borderRadius:'50%', background:'var(--c-primary)', display:'flex', alignItems:'center', justifyContent:'center', fontSize:26, fontWeight:700, margin:'0 auto 12px' }}>
                  {request.candidate.candidate_id[0]?.toUpperCase()}
                </div>
                <p style={{ fontWeight:700, marginBottom:4 }}>{request.candidate.candidate_id}</p>
                <p style={{ fontSize:11, color:'var(--c-text3)', marginBottom:16 }}>wants to connect — if you both say yes, you're matched</p>
                <div style={{ display:'flex', gap:10, justifyContent:'center' }}>
                  <button style={css.btn('secondary')} disabled={busy} onClick={() => decide('skip')}>Skip</button>
                  <button style={css.btn()} disabled={busy} onClick={() => decide('connect')}>Connect</button>
                </div>
              </div>
            )}

            {request && request.state === 'matched' && (
              <div style={{ textAlign:'center', padding:24 }}>
                <div style={{ fontSize:32, marginBottom:12 }}>🎉</div>
                <p style={{ fontWeight:700, marginBottom:4 }}>You matched with {request.matched_with}!</p>
                <div style={{ display:'flex', gap:10, justifyContent:'center', marginTop:16 }}>
                  <button style={css.btn('secondary')} onClick={() => onOpenProfile && onOpenProfile(request.matched_with)}>View Profile</button>
                  <button style={css.btn()} onClick={() => setRequest(null)}>Done</button>
                </div>
              </div>
            )}

            {request && (request.state === 'cancelled' || request.state === 'timeout') && (
              <div style={{ textAlign:'center', padding:24 }}>
                <p style={{ color:'var(--c-text3)', marginBottom:16 }}>{request.state === 'timeout' ? 'No match found in time.' : 'Search cancelled.'}</p>
                <button style={css.btn()} onClick={() => setRequest(null)}>Start Again</button>
              </div>
            )}
          </div>
        )}

        {tab === 'history' && (
          <div style={{ display:'flex', flexDirection:'column', gap:8 }}>
            {history.length === 0
              ? <p style={{ color:'var(--c-text3)', fontSize:12 }}>No match history yet.</p>
              : history.map(h => (
                <div key={h.request_id} style={{ display:'flex', justifyContent:'space-between', fontSize:13, padding:'8px 0', borderBottom:'1px solid var(--c-border)' }}>
                  <span style={{ textTransform:'capitalize' }}>{h.state}{h.matched_with ? ' · ' + h.matched_with : ''}</span>
                  <span style={{ color:'var(--c-text3)', fontSize:11 }}>{new Date(h.created_at).toLocaleDateString()}</span>
                </div>
              ))
            }
          </div>
        )}
      </div>
    </div>
  );
}

// ── Spend limits modal (Phase 9 follow-up) ──────────────────────────────────
// The self-set purchase caps + cooling-off backend (doc 11 §9) has existed
// since Phase 9 with no UI anywhere — same class of gap as matchmaking and
// private video before it.
function SpendLimitsModal({ onClose }) {
  const [limits, setLimits] = useState(null);
  const [daily, setDaily] = useState('');
  const [weekly, setWeekly] = useState('');
  const [monthly, setMonthly] = useState('');
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    api('GET', '/me/spend-limits').then(d => {
      setLimits(d);
      setDaily(d.daily_cap || '');
      setWeekly(d.weekly_cap || '');
      setMonthly(d.monthly_cap || '');
    }).catch(e => setErr(e.message || 'Could not load spend limits.'));
  }, []);
  useEffect(() => { load(); }, [load]);

  async function saveCaps(e) {
    e.preventDefault();
    setBusy(true); setErr('');
    try {
      const updated = await api('PATCH', '/me/spend-limits', {
        daily_cap: daily ? parseInt(daily, 10) : 0,
        weekly_cap: weekly ? parseInt(weekly, 10) : 0,
        monthly_cap: monthly ? parseInt(monthly, 10) : 0,
      });
      setLimits(updated);
    } catch (e2) { setErr(e2.message || 'Could not update spend limits.'); }
    finally { setBusy(false); }
  }

  async function toggleCoolingOff() {
    setBusy(true); setErr('');
    try {
      const updated = await api('PATCH', '/me/spend-limits', { cooling_off: !limits.cooling_off });
      setLimits(updated);
    } catch (e) { setErr(e.message || 'Could not update cooling-off.'); }
    finally { setBusy(false); }
  }

  const hasPending = !!(limits && (limits.pending_daily_cap || limits.pending_weekly_cap || limits.pending_monthly_cap));

  return (
    <div style={{ position:'fixed', inset:0, background:'rgba(0,0,0,0.85)', zIndex:1000, display:'flex', alignItems:'center', justifyContent:'center', padding:20 }} onClick={onClose}>
      <div style={{ background:'var(--c-surface)', borderRadius:'var(--r-xl)', width:'100%', maxWidth:400, padding:24 }} onClick={e => e.stopPropagation()}>
        <div style={{ display:'flex', justifyContent:'space-between', alignItems:'center', marginBottom:16 }}>
          <h2 style={{ fontWeight:800 }}>💳 Spend Limits</h2>
          <button onClick={onClose} style={{ color:'var(--c-text2)' }}>✕</button>
        </div>

        {err && <p style={{ color:'var(--c-accent)', fontSize:12, marginBottom:12 }}>{err}</p>}

        {limits && (
          <div style={{ display:'flex', flexDirection:'column', gap:16 }}>
            <div style={{ display:'flex', justifyContent:'space-between', alignItems:'center', background:'var(--c-surface2)', borderRadius:'var(--r-md)', padding:'10px 14px' }}>
              <div>
                <p style={{ fontSize:13, fontWeight:600 }}>Cooling-off</p>
                <p style={{ fontSize:11, color:'var(--c-text3)' }}>Blocks all coin purchases for 24h once activated</p>
              </div>
              <button onClick={toggleCoolingOff} disabled={busy} style={{ width:40, height:22, borderRadius:99, background: limits.cooling_off ? 'var(--c-accent)' : 'var(--c-border)', position:'relative', flexShrink:0 }}>
                <span style={{ position:'absolute', top:2, left: limits.cooling_off ? 20 : 2, width:18, height:18, borderRadius:'50%', background:'#fff', transition:'left 0.15s' }} />
              </button>
            </div>

            <form onSubmit={saveCaps} style={{ display:'flex', flexDirection:'column', gap:10 }}>
              <p style={{ fontSize:12, color:'var(--c-text3)' }}>Coin purchase caps — 0 means no cap. Lowering applies immediately; raising takes effect after a 48-hour cooling-off period.</p>
              <label style={{ fontSize:12 }}>Daily cap
                <input style={{ ...css.input, marginTop:4 }} type="number" min="0" value={daily} onChange={e => setDaily(e.target.value)} />
              </label>
              <label style={{ fontSize:12 }}>Weekly cap
                <input style={{ ...css.input, marginTop:4 }} type="number" min="0" value={weekly} onChange={e => setWeekly(e.target.value)} />
              </label>
              <label style={{ fontSize:12 }}>Monthly cap
                <input style={{ ...css.input, marginTop:4 }} type="number" min="0" value={monthly} onChange={e => setMonthly(e.target.value)} />
              </label>
              <button style={css.btn()} disabled={busy}>Save</button>
            </form>

            {hasPending && (
              <div style={{ background:'var(--c-surface2)', borderRadius:'var(--r-md)', padding:'10px 14px', fontSize:12, color:'var(--c-text2)' }}>
                A raised cap is pending until {new Date(limits.pending_effective_at).toLocaleString()}: daily {limits.pending_daily_cap}, weekly {limits.pending_weekly_cap}, monthly {limits.pending_monthly_cap}.
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

// ── Account Settings modal ──────────────────────────────────────────────────
// Age assurance, wallet transaction history, and account deletion all had
// complete, working backends (doc 10 §2b's age gate, doc 12 Phase 9's
// user-visible ledger, GDPR-style 30-day-grace deletion) with no UI
// anywhere in the app to reach them.
function AccountSettingsModal({ onClose }) {
  const [tab, setTab] = useState('age');
  const [ageStatus, setAgeStatus] = useState(null);
  const [dob, setDob] = useState('');
  const [txHistory, setTxHistory] = useState([]);
  const [deletionMsg, setDeletionMsg] = useState('');
  const [err, setErr] = useState('');
  const [busy, setBusy] = useState(false);

  const loadAge = useCallback(() => {
    api('GET', '/auth/age/status').then(d => setAgeStatus(d.status)).catch(e => setErr(e.message));
  }, []);
  useEffect(() => { loadAge(); }, [loadAge]);

  useEffect(() => {
    if (tab === 'history') {
      api('GET', '/wallet/transactions').then(d => setTxHistory(d.items || [])).catch(e => setErr(e.message));
    }
  }, [tab]);

  async function declareAge(e) {
    e.preventDefault();
    if (!dob) return;
    setBusy(true); setErr('');
    try {
      await api('POST', '/auth/age/declare', { dob });
      loadAge();
    } catch (e2) { setErr(e2.message || 'Could not declare age.'); }
    finally { setBusy(false); }
  }

  async function devAssure() {
    setBusy(true); setErr('');
    try {
      await api('POST', '/auth/age/dev-assure', {});
      loadAge();
    } catch (e2) { setErr(e2.message || 'Could not assure age.'); }
    finally { setBusy(false); }
  }

  async function requestDeletion() {
    if (!confirm('This will permanently delete your account in 30 days. Continue?')) return;
    setBusy(true); setErr('');
    try {
      const d = await api('POST', '/me/delete', {});
      setDeletionMsg(d.message);
    } catch (e2) { setErr(e2.message || 'Could not start account deletion.'); }
    finally { setBusy(false); }
  }

  async function cancelDeletion() {
    setBusy(true); setErr('');
    try {
      const d = await api('POST', '/me/delete/cancel', {});
      setDeletionMsg(d.message);
    } catch (e2) { setErr(e2.message || 'Could not cancel account deletion.'); }
    finally { setBusy(false); }
  }

  const statusColor = { undeclared:'var(--c-text3)', declared:'var(--c-accent)', assured:'var(--c-success)' };

  return (
    <div style={{ position:'fixed', inset:0, background:'rgba(0,0,0,0.85)', zIndex:1000, display:'flex', alignItems:'center', justifyContent:'center', padding:20 }} onClick={onClose}>
      <div style={{ background:'var(--c-surface)', borderRadius:'var(--r-xl)', width:'100%', maxWidth:460, maxHeight:'85vh', overflowY:'auto', padding:20 }} onClick={e => e.stopPropagation()}>
        <div style={{ display:'flex', justifyContent:'space-between', alignItems:'center', marginBottom:16 }}>
          <h2 style={{ fontWeight:800 }}>⚙️ Account Settings</h2>
          <button onClick={onClose} style={{ color:'var(--c-text2)' }}>✕</button>
        </div>

        <div style={{ display:'flex', gap:4, marginBottom:16 }}>
          {['age','history','delete'].map(t => (
            <button key={t} onClick={() => setTab(t)} style={{ padding:'6px 14px', borderRadius:'99px', fontSize:12, fontWeight: tab===t?700:500, background: tab===t?'var(--c-primary)':'var(--c-surface2)' }}>
              {t === 'age' ? 'Age' : t === 'history' ? 'Transactions' : 'Delete Account'}
            </button>
          ))}
        </div>

        {err && <p style={{ color:'var(--c-accent)', fontSize:12, marginBottom:12 }}>{err}</p>}

        {tab === 'age' && (
          <div style={{ display:'flex', flexDirection:'column', gap:12 }}>
            <p style={{ fontSize:13 }}>Status: <span style={{ fontWeight:700, color: statusColor[ageStatus] || 'var(--c-text)', textTransform:'capitalize' }}>{ageStatus || '…'}</span></p>
            {ageStatus === 'undeclared' && (
              <form onSubmit={declareAge} style={{ display:'flex', flexDirection:'column', gap:10 }}>
                <label style={{ fontSize:12 }}>Date of birth
                  <input style={{ ...css.input, marginTop:4 }} type="date" value={dob} onChange={e => setDob(e.target.value)} />
                </label>
                <button style={css.btn()} disabled={busy || !dob}>Declare age</button>
              </form>
            )}
            {ageStatus === 'declared' && (
              <div style={{ background:'var(--c-surface2)', borderRadius:'var(--r-md)', padding:12 }}>
                <p style={{ fontSize:12, color:'var(--c-text2)', marginBottom:8 }}>No real age-assurance vendor is wired into this dev build. Use this dev-only button to simulate full assurance.</p>
                <button style={css.btn('secondary')} onClick={devAssure} disabled={busy}>🛠 Dev: Assure Age</button>
              </div>
            )}
          </div>
        )}

        {tab === 'history' && (
          <div style={{ display:'flex', flexDirection:'column', gap:8 }}>
            {txHistory.length === 0
              ? <p style={{ color:'var(--c-text3)', fontSize:12 }}>No transactions yet.</p>
              : txHistory.map(t => (
                <div key={t.transaction_id} style={{ display:'flex', justifyContent:'space-between', fontSize:13, padding:'6px 0', borderBottom:'1px solid var(--c-border)' }}>
                  <div>
                    <p style={{ textTransform:'capitalize' }}>{t.kind.replace(/_/g,' ')}</p>
                    <p style={{ fontSize:11, color:'var(--c-text3)' }}>{new Date(t.created_at).toLocaleString()}</p>
                  </div>
                  <span style={{ color: t.amount < 0 ? 'var(--c-live)' : 'var(--c-success)', fontWeight:600 }}>{t.amount > 0 ? '+' : ''}{t.amount} {t.currency}</span>
                </div>
              ))
            }
          </div>
        )}

        {tab === 'delete' && (
          <div style={{ display:'flex', flexDirection:'column', gap:12 }}>
            <p style={{ fontSize:12, color:'var(--c-text2)' }}>Deleting your account starts a 30-day grace period. You can cancel any time before it ends.</p>
            {deletionMsg && <p style={{ fontSize:12, color:'var(--c-success)' }}>{deletionMsg}</p>}
            <div style={{ display:'flex', gap:8 }}>
              <button style={{ ...css.btn('secondary'), color:'var(--c-live)' }} onClick={requestDeletion} disabled={busy}>Request Deletion</button>
              <button style={css.btn('secondary')} onClick={cancelDeletion} disabled={busy}>Cancel Deletion</button>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

// ── Buy Coins modal ──────────────────────────────────────────────────────────
// The full purchase/order backend (GET /products, POST /orders, POST
// /orders/{id}/verify) had no UI at all — the only wallet-funding button in
// the app was the "dev top-up (not a real purchase)" cheat. This drives the
// real order state machine; in this dev build (no live Stripe checkout
// redirect wired up) the "payment" step is a purchase-token prompt whose
// behavior is explained inline, exactly like every other dev-only escape
// hatch in the app rather than pretending to be a real checkout.
function BuyCoinsModal({ onClose, onPurchased }) {
  const [products, setProducts] = useState([]);
  const [order, setOrder] = useState(null);
  const [token, setToken] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');

  useEffect(() => {
    api('GET', '/products', null, false).then(d => setProducts(d.items || [])).catch(e => setErr(e.message));
  }, []);

  async function buy(sku) {
    setBusy(true); setErr(''); setOrder(null);
    try {
      const d = await api('POST', '/orders', { sku }, true, {
        'Idempotency-Key': 'order-' + Date.now() + '-' + Math.random().toString(36).slice(2),
      });
      setOrder(d);
    } catch (e2) { setErr(e2.message || 'Could not start order.'); }
    finally { setBusy(false); }
  }

  async function verify(e) {
    e.preventDefault();
    if (!order || !token.trim()) return;
    setBusy(true); setErr('');
    try {
      const d = await api('POST', '/orders/' + order.order_id + '/verify', { purchase_token: token.trim() });
      setOrder(d);
      if (d.status === 'credited') {
        setToken('');
        if (onPurchased) onPurchased(d.coins);
      }
    } catch (e2) { setErr(e2.message || 'Could not verify purchase.'); }
    finally { setBusy(false); }
  }

  return (
    <div style={{ position:'fixed', inset:0, background:'rgba(0,0,0,0.85)', zIndex:1500, display:'flex', alignItems:'center', justifyContent:'center', padding:20 }} onClick={onClose}>
      <div style={{ background:'var(--c-surface)', borderRadius:'var(--r-xl)', width:'100%', maxWidth:380, padding:20 }} onClick={e => e.stopPropagation()}>
        <div style={{ display:'flex', justifyContent:'space-between', alignItems:'center', marginBottom:16 }}>
          <h2 style={{ fontWeight:800 }}>💰 Buy Coins</h2>
          <button onClick={onClose} style={{ color:'var(--c-text2)' }}>✕</button>
        </div>

        {err && <p style={{ color:'var(--c-accent)', fontSize:12, marginBottom:12 }}>{err}</p>}

        {!order && (
          <div style={{ display:'flex', flexDirection:'column', gap:8 }}>
            {products.map(p => (
              <button key={p.sku} disabled={busy} onClick={() => buy(p.sku)} style={{ display:'flex', justifyContent:'space-between', alignItems:'center', background:'var(--c-surface2)', borderRadius:'var(--r-md)', padding:'12px 14px' }}>
                <span style={{ fontWeight:700 }}>{p.coins.toLocaleString()} coins</span>
                <span style={{ color:'var(--c-text2)' }}>{(p.price_minor / 100).toFixed(2)} {p.price_currency}</span>
              </button>
            ))}
          </div>
        )}

        {order && order.status !== 'credited' && (
          <div style={{ display:'flex', flexDirection:'column', gap:10 }}>
            <p style={{ fontSize:12, color:'var(--c-text2)' }}>Order {order.order_id} — status: <strong style={{ textTransform:'capitalize' }}>{order.status}</strong></p>
            <p style={{ fontSize:11, color:'var(--c-text3)' }}>No live payment provider is connected in this dev build. Enter a purchase token to simulate the store's confirmation — anything works except a token starting with "fail-" (rejected) or "pending-" (stays pending for a couple of tries before succeeding).</p>
            <form onSubmit={verify} style={{ display:'flex', gap:8 }}>
              <input style={css.input} placeholder="Purchase token" value={token} onChange={e => setToken(e.target.value)} />
              <button style={css.btn()} disabled={busy || !token.trim()}>Verify</button>
            </form>
          </div>
        )}

        {order && order.status === 'credited' && (
          <div style={{ textAlign:'center', padding:'12px 0' }}>
            <p style={{ fontSize:14, color:'var(--c-success)', fontWeight:700, marginBottom:8 }}>✓ {order.coins} coins credited</p>
            <button style={css.btn()} onClick={onClose}>Done</button>
          </div>
        )}
      </div>
    </div>
  );
}

function App() {
  const [tab, setTab] = useState('for_you');
  const [rooms, setRooms] = useState([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [cursor, setCursor] = useState('');
  const [hasMore, setHasMore] = useState(true);
  const [selectedRoom, setSelectedRoom] = useState(null);
  const [showAuth, setShowAuth] = useState(false);
  const [account, setAccount] = useState(null);
  const [searchResults, setSearchResults] = useState(null);
  const [showGoLive, setShowGoLive] = useState(false);
  const [goLiveTitle, setGoLiveTitle] = useState('');
  const [notifCount, setNotifCount] = useState(0);
  const [showMessages, setShowMessages] = useState(false);
  const [showCreator, setShowCreator] = useState(false);
  const [showModeration, setShowModeration] = useState(false);
  const [showSupport, setShowSupport] = useState(false);
  const [showAdmin, setShowAdmin] = useState(false);
  const [showAccountMenu, setShowAccountMenu] = useState(false);
  const [showSpendLimits, setShowSpendLimits] = useState(false);
  const [showAccountSettings, setShowAccountSettings] = useState(false);
  const [showNotifications, setShowNotifications] = useState(false);
  const [notifications, setNotifications] = useState([]);
  const [viewedProfileID, setViewedProfileID] = useState(null);
  const [showMatch, setShowMatch] = useState(false);

  // Load initial auth state
  useEffect(() => {
    if (_token) {
      api('GET', '/me').then(d => setAccount({ ...d.profile, id: d.profile.account_id })).catch(() => setToken(''));
    }
    api('GET', '/notifications/summary', null, false).then(d => setNotifCount(d.unread_count || 0)).catch(() => {});
  }, []);

  function signOut() {
    setToken('');
    setAccount(null);
    setShowAccountMenu(false);
  }

  async function openNotifications() {
    setShowNotifications(v => !v);
    setShowAccountMenu(false);
    if (showNotifications) return; // closing, nothing to fetch
    try {
      const d = await api('GET', '/notifications');
      const items = d.items || [];
      setNotifications(items);
      if (items.length > 0) {
        await api('POST', '/notifications/read', { up_to_id: items[0].id });
        setNotifCount(0);
      }
    } catch (e) { /* notification fetch failure shouldn't break the header */ }
  }

  // Fetch feed when tab changes (BT-05: cursor reset on tab switch)
  const fetchFeed = useCallback(async (newTab, newCursor = '') => {
    setLoading(true); setError('');
    try {
      const qs = new URLSearchParams({ tab: newTab, limit: '12' });
      if (newCursor) qs.set('cursor', newCursor);
      const data = await api('GET', '/feed?' + qs, null, false);
      if (newCursor) {
        setRooms(prev => [...prev, ...(data.items || [])]);
      } else {
        setRooms(data.items || []);
        window.scrollTo(0, 0); // BT-05: scroll to top on tab change
      }
      setCursor(data.next_cursor || '');
      setHasMore(!!data.next_cursor);
    } catch(err) {
      setError(err.message || 'Failed to load feed. Is the server running?');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { fetchFeed(tab); }, [tab]);

  function changeTab(newTab) {
    setTab(newTab);
    setCursor('');
    setRooms([]);
    setHasMore(true);
    setSearchResults(null);
  }

  async function goLive() {
    if (!account) { setShowAuth(true); return; }
    if (!goLiveTitle.trim()) return;
    try {
      await api('POST', '/streams', { title: goLiveTitle, language: 'en', region_code: 'US', tags: [] });
      setShowGoLive(false); setGoLiveTitle('');
      fetchFeed(tab);
      alert('Room created! Look for "' + goLiveTitle.trim() + '" in the feed. Chat, likes and gifts inside it are fully live — actual video capture/playback isn\'t built (no media server in this environment).');
    } catch(err) {
      alert(err.message || 'Could not create room.');
    }
  }

  return (
    <div style={{ maxWidth:1200, margin:'0 auto', padding:'0 16px' }}>
      {/* ── Header ── */}
      <header style={{ position:'sticky', top:0, zIndex:100, background:'var(--c-bg)', borderBottom:'1px solid var(--c-border)', padding:'12px 0', marginBottom:20 }}>
        <div style={{ display:'flex', alignItems:'center', gap:12, maxWidth:1200, margin:'0 auto', padding:'0 16px' }}>
          {/* Logo */}
          <div style={{ display:'flex', alignItems:'center', gap:8, marginRight:8 }}>
            <span style={{ fontSize:22 }}>⚡</span>
            <span style={{ fontWeight:900, fontSize:18, background:'linear-gradient(135deg,var(--c-primary),var(--c-accent))', WebkitBackgroundClip:'text', WebkitTextFillColor:'transparent', letterSpacing:-0.5 }}>Lumena</span>
          </div>
          {/* Search */}
          <div style={{ flex:1, maxWidth:440 }}>
            <SearchBar onResults={setSearchResults} />
          </div>
          {/* Right */}
          <div style={{ display:'flex', gap:8, marginLeft:'auto', alignItems:'center' }}>
            {/* Messages (Phase 7) */}
            <button style={{ position:'relative', padding:'8px', borderRadius:'var(--r-md)', background:'var(--c-surface2)', color:'var(--c-text)', fontSize:18 }} onClick={() => account ? setShowMessages(true) : setShowAuth(true)}>
              ✉️
            </button>
            {/* Notification bell */}
            <div style={{ position:'relative' }}>
              <button style={{ position:'relative', padding:'8px', borderRadius:'var(--r-md)', background:'var(--c-surface2)', color:'var(--c-text)', fontSize:18 }} onClick={() => account ? openNotifications() : setShowAuth(true)}>
                🔔
                {notifCount > 0 && <span style={{ position:'absolute', top:4, right:4, width:8, height:8, borderRadius:'50%', background:'var(--c-accent)' }} />}
              </button>
              {showNotifications && (
                <div style={{ position:'absolute', top:'110%', right:0, width:300, maxHeight:360, overflowY:'auto', background:'var(--c-surface)', border:'1px solid var(--c-border)', borderRadius:'var(--r-lg)', boxShadow:'0 12px 32px rgba(0,0,0,0.4)', zIndex:150, padding:8 }}>
                  {notifications.length === 0
                    ? <p style={{ color:'var(--c-text3)', fontSize:12, padding:12, textAlign:'center' }}>No notifications yet.</p>
                    : notifications.map(n => (
                      <div key={n.id} style={{ padding:'10px 8px', borderBottom:'1px solid var(--c-border)', fontSize:13 }}>
                        <p>{n.body}</p>
                        <p style={{ color:'var(--c-text3)', fontSize:11, marginTop:2 }}>{new Date(n.created_at).toLocaleString()}</p>
                      </div>
                    ))
                  }
                </div>
              )}
            </div>
            {/* Creator dashboard (Phase 14) */}
            {account && (
              <button style={{ ...css.btn('secondary'), display:'flex', alignItems:'center', gap:6, fontSize:13 }} onClick={() => setShowCreator(true)}>
                💰 Creator
              </button>
            )}
            {/* Matchmaking (Phase 12) */}
            {account && (
              <button style={{ ...css.btn('secondary'), display:'flex', alignItems:'center', gap:6, fontSize:13 }} onClick={() => setShowMatch(true)}>
                💘 Match
              </button>
            )}
            {/* Trust & safety console (Phase 15) */}
            {account && (
              <button style={{ ...css.btn('secondary'), display:'flex', alignItems:'center', gap:6, fontSize:13 }} onClick={() => setShowModeration(true)}>
                🛡 Safety
              </button>
            )}
            {/* Support tickets (Phase 16) */}
            {account && (
              <button style={{ ...css.btn('secondary'), display:'flex', alignItems:'center', gap:6, fontSize:13 }} onClick={() => setShowSupport(true)}>
                🎫 Support
              </button>
            )}
            {/* Admin console (Phase 16, RBAC-gated) */}
            {account && (
              <button style={{ ...css.btn('secondary'), display:'flex', alignItems:'center', gap:6, fontSize:13 }} onClick={() => setShowAdmin(true)}>
                ⚙️ Admin
              </button>
            )}
            {/* Go Live */}
            <button style={{ ...css.btn(), display:'flex', alignItems:'center', gap:6, fontSize:13 }} onClick={() => account ? setShowGoLive(true) : setShowAuth(true)}>
              🔴 Go Live
            </button>
            {/* Auth */}
            {account ? (
              <div style={{ position:'relative' }}>
                <button style={{ ...css.btn('secondary'), display:'flex', alignItems:'center', gap:8, padding:'8px 14px' }} onClick={() => { setShowAccountMenu(v => !v); setShowNotifications(false); }}>
                  <div style={{ width:26, height:26, borderRadius:'50%', background:'var(--c-primary)', display:'flex', alignItems:'center', justifyContent:'center', fontSize:13, fontWeight:700 }}>
                    {account.display_name?.[0] || '?'}
                  </div>
                  <span style={{ fontSize:13, maxWidth:80, overflow:'hidden', textOverflow:'ellipsis', whiteSpace:'nowrap' }}>{account.display_name || account.id}</span>
                </button>
                {showAccountMenu && (
                  <div style={{ position:'absolute', top:'110%', right:0, width:180, background:'var(--c-surface)', border:'1px solid var(--c-border)', borderRadius:'var(--r-lg)', boxShadow:'0 12px 32px rgba(0,0,0,0.4)', zIndex:150, padding:6 }}>
                    <p style={{ padding:'8px 10px', fontSize:12, color:'var(--c-text3)', borderBottom:'1px solid var(--c-border)', marginBottom:4 }}>{account.id}</p>
                    <button style={{ width:'100%', textAlign:'left', padding:'8px 10px', borderRadius:'var(--r-md)', fontSize:13 }} onClick={() => { setShowAccountMenu(false); setViewedProfileID(account.id); }}>My Profile</button>
                    <button style={{ width:'100%', textAlign:'left', padding:'8px 10px', borderRadius:'var(--r-md)', fontSize:13 }} onClick={() => { setShowAccountMenu(false); setShowSpendLimits(true); }}>💳 Spend Limits</button>
                    <button style={{ width:'100%', textAlign:'left', padding:'8px 10px', borderRadius:'var(--r-md)', fontSize:13 }} onClick={() => { setShowAccountMenu(false); setShowAccountSettings(true); }}>⚙️ Settings</button>
                    <button style={{ width:'100%', textAlign:'left', padding:'8px 10px', borderRadius:'var(--r-md)', fontSize:13, color:'var(--c-accent)' }} onClick={signOut}>Sign Out</button>
                  </div>
                )}
              </div>
            ) : (
              <button style={{ ...css.btn('secondary'), fontSize:13 }} onClick={() => setShowAuth(true)}>Sign In</button>
            )}
          </div>
        </div>
      </header>

      {/* ── Search Results Overlay ── */}
      {searchResults !== null && (
        <div style={{ background:'var(--c-surface)', border:'1px solid var(--c-border)', borderRadius:'var(--r-lg)', padding:16, marginBottom:20 }}>
          <div style={{ display:'flex', justifyContent:'space-between', alignItems:'center', marginBottom:12 }}>
            <h3 style={{ fontWeight:700 }}>Search Results</h3>
            <button style={{ color:'var(--c-text2)' }} onClick={() => setSearchResults(null)}>✕</button>
          </div>
          {searchResults.length === 0
            ? <p style={{ color:'var(--c-text3)', fontSize:14 }}>No results found.</p>
            : searchResults.map(r => (
              <div key={r.id} style={{ display:'flex', alignItems:'center', gap:12, padding:'10px 0', borderBottom:'1px solid var(--c-border)' }}>
                <div style={{ width:36, height:36, borderRadius:'var(--r-sm)', background:'var(--c-primary)22', display:'flex', alignItems:'center', justifyContent:'center', fontSize:18 }}>
                  {r.type === 'room' ? '📡' : '👤'}
                </div>
                <div>
                  <p style={{ fontWeight:600, fontSize:14 }}>{r.display_name}</p>
                  <p style={{ color:'var(--c-text3)', fontSize:12 }}>{r.subtitle}</p>
                </div>
                {r.is_live && <div style={{ marginLeft:'auto' }}><LiveBadge /></div>}
              </div>
            ))
          }
        </div>
      )}

      {/* ── Tabs ── */}
      <TabBar active={tab} onChange={changeTab} />

      {/* ── Error ── */}
      {error && (
        <div style={{ background:'#ff5c7a22', border:'1px solid #ff5c7a44', borderRadius:'var(--r-md)', padding:16, marginBottom:20, display:'flex', gap:12, alignItems:'center' }}>
          <span style={{ color:'var(--c-accent)', fontSize:20 }}>⚠</span>
          <div>
            <p style={{ fontWeight:700, color:'var(--c-accent)' }}>Could not load feed</p>
            <p style={{ fontSize:13, color:'var(--c-text2)', marginTop:4 }}>{error}</p>
            <p style={{ fontSize:12, color:'var(--c-text3)', marginTop:4 }}>Is the Go server running? Start with: JWT_SECRET="..." go run ./cmd/api/main.go</p>
          </div>
          <button style={{ marginLeft:'auto', color:'var(--c-accent)', fontWeight:700 }} onClick={() => fetchFeed(tab)}>Retry</button>
        </div>
      )}

      {/* ── Following empty state ── */}
      {tab === 'following' && rooms.length === 0 && !loading && (
        <div style={{ textAlign:'center', padding:60 }}>
          <div style={{ fontSize:48, marginBottom:16 }}>🎥</div>
          <h2 style={{ fontWeight:800, marginBottom:8 }}>No one live yet</h2>
          <p style={{ color:'var(--c-text2)', marginBottom:20 }}>Follow creators to see when they go live here.</p>
          <button style={css.btn()} onClick={() => changeTab('explore')}>Discover Creators</button>
        </div>
      )}

      {/* ── Room grid ── */}
      {rooms.length > 0 && (
        <div style={{ display:'grid', gridTemplateColumns:'repeat(auto-fill, minmax(240px, 1fr))', gap:16, marginBottom:20 }}>
          {rooms.map(room => <RoomCard key={room.room_id} room={room} onClick={setSelectedRoom} />)}
        </div>
      )}

      {/* ── Loading skeleton ── */}
      {loading && rooms.length === 0 && (
        <div style={{ display:'grid', gridTemplateColumns:'repeat(auto-fill, minmax(240px, 1fr))', gap:16 }}>
          {Array.from({length:8}).map((_,i) => (
            <div key={i} style={{ background:'var(--c-surface)', borderRadius:'var(--r-lg)', overflow:'hidden' }}>
              <div style={{ paddingTop:'56.25%', background:'var(--c-surface2)', animation:'shimmer 1.5s infinite' }} />
              <div style={{ padding:'12px 14px' }}>
                <div style={{ height:13, borderRadius:4, background:'var(--c-surface2)', marginBottom:8, width:'75%' }} />
                <div style={{ height:11, borderRadius:4, background:'var(--c-surface2)', width:'50%' }} />
              </div>
            </div>
          ))}
        </div>
      )}

      {/* ── Load more ── */}
      {hasMore && !loading && rooms.length > 0 && (
        <div style={{ textAlign:'center', padding:24 }}>
          <button style={css.btn('secondary')} onClick={() => fetchFeed(tab, cursor)}>Load more</button>
        </div>
      )}

      {/* ── Room modal ── */}
      {selectedRoom && <RoomModal room={selectedRoom} account={account} onClose={() => setSelectedRoom(null)} />}

      {/* ── Auth modal ── */}
      {showAuth && <AuthModal onSuccess={acc => { setAccount(acc); setShowAuth(false); }} onClose={() => setShowAuth(false)} />}

      {/* ── Messages modal (Phase 7) ── */}
      {showMessages && account && <MessagesModal account={account} onClose={() => setShowMessages(false)} />}

      {/* ── Creator dashboard modal (Phase 14) ── */}
      {showCreator && account && <CreatorDashboardModal onClose={() => setShowCreator(false)} />}

      {/* ── Moderation console modal (Phase 15) ── */}
      {showModeration && account && <ModerationConsoleModal onClose={() => setShowModeration(false)} />}

      {/* ── Support tickets modal (Phase 16) ── */}
      {showSupport && account && <SupportModal onClose={() => setShowSupport(false)} />}

      {/* ── Admin console modal (Phase 16) ── */}
      {showAdmin && account && <AdminConsoleModal onClose={() => setShowAdmin(false)} />}

      {/* ── Profile modal (own profile, via account menu) ── */}
      {viewedProfileID && <ProfileModal accountID={viewedProfileID} viewerAccount={account} onClose={() => setViewedProfileID(null)} onOpenProfile={setViewedProfileID} />}

      {/* ── Match modal (Phase 12) ── */}
      {showMatch && account && <MatchModal onClose={() => setShowMatch(false)} onOpenProfile={setViewedProfileID} />}

      {/* ── Spend limits modal (Phase 9 follow-up) ── */}
      {showSpendLimits && account && <SpendLimitsModal onClose={() => setShowSpendLimits(false)} />}
      {showAccountSettings && account && <AccountSettingsModal onClose={() => setShowAccountSettings(false)} />}

      {/* ── Go Live modal ── */}
      {showGoLive && (
        <div style={{ position:'fixed', inset:0, background:'rgba(0,0,0,0.85)', zIndex:2000, display:'flex', alignItems:'center', justifyContent:'center', padding:20 }} onClick={() => setShowGoLive(false)}>
          <div style={{ background:'var(--c-surface)', borderRadius:'var(--r-xl)', padding:32, width:'100%', maxWidth:400 }} onClick={e => e.stopPropagation()}>
            <h2 style={{ fontWeight:800, marginBottom:4 }}>🔴 Start Streaming</h2>
            <p style={{ color:'var(--c-text2)', fontSize:13, marginBottom:20 }}>No real camera/video — this creates a room card with live chat, likes and gifts.</p>
            <div style={{ display:'flex', flexDirection:'column', gap:12 }}>
              <input style={css.input} placeholder="Stream title…" value={goLiveTitle} onChange={e => setGoLiveTitle(e.target.value)} />
              <div style={{ display:'flex', gap:8 }}>
                <button style={{ ...css.btn('secondary'), flex:1 }} onClick={() => setShowGoLive(false)}>Cancel</button>
                <button style={{ ...css.btn(), flex:1 }} onClick={goLive} disabled={!goLiveTitle.trim()}>Go Live</button>
              </div>
            </div>
          </div>
        </div>
      )}

      {/* ── Phase progress bar ── */}
      <div style={{ borderTop:'1px solid var(--c-border)', marginTop:40, padding:'24px 0', display:'flex', flexDirection:'column', gap:8 }}>
        <p style={{ fontWeight:700, fontSize:14, marginBottom:8 }}>🚀 Build Progress — v1.0.0, all 20 roadmap phases</p>
        <div style={{ display:'flex', gap:6, flexWrap:'wrap' }}>
          {[
            { n:1, label:'Architecture', done:true },
            { n:2, label:'Auth', done:true },
            { n:3, label:'Profiles', done:true },
            { n:4, label:'Feed', done:true },
            { n:5, label:'Streaming', done:true },
            { n:6, label:'Real-time', done:true },
            { n:7, label:'Chat', done:true },
            { n:8, label:'Gifts', done:true },
            { n:9, label:'Wallet', done:true },
            { n:10, label:'Attachments', done:true },
            { n:11, label:'Private Video', done:true },
            { n:12, label:'Matchmaking', done:true },
            { n:13, label:'Translation', done:true },
            { n:14, label:'Creator Dashboard', done:true },
            { n:15, label:'Moderation', done:true },
            { n:16, label:'Admin Dashboard', done:true },
            { n:17, label:'Analytics', done:true },
            { n:18, label:'Fraud Prevention', done:true },
            { n:19, label:'Performance Testing', done:true },
            { n:20, label:'Production Deployment', done:true },
          ].map(p => (
            <div key={p.n} style={{ padding:'4px 10px', borderRadius:'var(--r-sm)', fontSize:11, fontWeight:600, background: p.done ? 'var(--c-success)22' : 'var(--c-surface2)', color: p.done ? 'var(--c-success)' : 'var(--c-text3)', border: '1px solid ' + (p.done ? 'var(--c-success)44' : 'var(--c-border)') }}>
              {p.done ? '✓' : p.n} {p.label}
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}

// ── Animation keyframes ──────────────────────────────────────────────────────
const style = document.createElement('style');
style.textContent = '@keyframes shimmer{0%,100%{opacity:1}50%{opacity:0.5}} @keyframes pulse{0%,100%{opacity:1}50%{opacity:0.4}}';
document.head.appendChild(style);

ReactDOM.createRoot(document.getElementById('root')).render(<App />);
</script>
</body>
</html>`
}
