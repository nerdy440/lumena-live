# 09 — Streaming Architecture

Two independent transport systems serve two fundamentally different problems:

- **1:many broadcast** (one host → thousands of viewers): latency of 2–4s is acceptable; CDN scale is essential; WebRTC on the ingest side simplifies the broadcaster SDK.
- **1:1 private video**: latency must be <300ms; CDN is irrelevant; WebRTC peer-to-peer (with server relay fallback) is the correct choice.

They share nothing below the application signaling layer.

---

## 1. 1:many broadcast — architecture

```
Broadcaster device
   │ WHIP (WebRTC-HTTP Ingest Protocol)
   ▼
INGEST EDGE (SFU node, regional)
   │ extracts media tracks
   ├─ video → TRANSCODER (H.264/H.265/AV1 ladder)
   └─ audio → AAC 128k
           │
           ▼
     ORIGIN PACKAGER (CMAF / LL-HLS)
           │
           ▼
     CDN (edge cache)
           │
           ▼
     Viewer devices (LL-HLS)
```

**Why WHIP for ingest?** WHIP is an IETF standard (RFC 9725). It reuses WebRTC's built-in congestion control (GCC), network path selection, packet loss recovery (NACK/RTX), and TLS. The broadcaster gets good network behavior without a custom RTMP implementation, and we get a single ingest protocol rather than RTMP + SRT + WebRTC per platform. RTMP fallback is supported for third-party streaming tools (OBS etc.) but the native SDK uses WHIP.

**Transcoder ladder (adaptive bitrate):**

| Rendition | Video | Audio |
|---|---|---|
| 1080p | 4500 kbps H.264 | AAC 192k |
| 720p | 2500 kbps H.264 | AAC 128k |
| 480p | 1200 kbps H.264 | AAC 96k |
| 360p | 600 kbps H.264 | AAC 96k |
| 144p (safety) | 200 kbps H.264 | AAC 64k |

H.265 and AV1 renditions added in Phase 2 when viewer decode coverage justifies it. The 144p safety rendition is the answer to "network too weak" — we offer a degraded stream rather than nothing.

**LL-HLS target latency:** 2s from capture to playback. Segment duration 1s, 6 parts per segment. This is within perceptual "live" for a social stream.

---

## 2. Ingest flow (broadcaster SDK)

```
POST /streams → {stream_id, state:"scheduled"}
POST /streams/{id}/start → {ingest_url, stream_key, expires_at}

// ingest_url is: wss://ingest-{region}.lumena.live/whip/{session_token}
// session_token is single-use, 5-minute TTL, signed with stream_id
// stream_key is NEVER logged
```

The broadcaster SDK:
1. Opens a WebRTC `PeerConnection` to the ingest edge.
2. Negotiates via WHIP exchange (`POST /whip/{token}` with SDP offer → SDP answer).
3. Sends video + audio tracks.
4. Monitors `RTCOutboundRtpStreamStats` to surface bitrate/fps/dropped frames in the UI.
5. On ICE failure: waits 3s, attempts ICE restart via WHIP PATCH.
6. On ingest disconnect: reconnects with backoff; the packager holds a 10s stale-segment buffer to prevent viewer-side rebuffering on brief interruptions.

The ingest edge publishes a `BROADCAST_HEALTH` event every 5s to the broadcaster's WebSocket: `{bitrate_kbps, fps, dropped_frames, rtt_ms, quality:"good|degraded|poor"}`.

---

## 3. Viewer playback

Viewers receive an LL-HLS manifest URL from `POST /rooms/{id}/join`. The player:

1. Fetches the multivariant playlist.
2. Starts at the rendition matching the network quality estimate.
3. Switches renditions based on download throughput vs. segment duration.
4. On stall >2s: tries next lower rendition before surfacing a rebuffering UI.
5. On stall >10s: surfaces the `RECONNECTING` connection chip and retries the manifest.
6. On manifest 404: the stream has ended — renders `ROOM_005` stream-ended screen.

**Audio-focus lifecycle (the C1 fix):** the player holds an `AudioSession` / `AudioFocus` token for its lifetime. Volume changes go through the platform's volume control only — never through the player's volume property during teardown. The player is released on `onStop`, not on `onPause`, and media keys are unregistered before the player object is released. The ordering is: `releaseMediaKeys → releaseAudioFocus → player.release()`. This exact ordering is enforced by a lifecycle test that stress-tests 1000 volume-key events during playback.

---

## 4. Real-time video moderation

Frame samples are extracted by the ingest edge (not the CDN — the edge has the raw frames before they're packaged) and sent to the moderation service:

```
Ingest edge: every 10s → capture 1 frame → POST moderation-service /classify/frame
{stream_id, frame_base64, timestamp}

Response (async via Kafka, target <2s):
{stream_id, timestamp, classes:[{label, confidence}], action:"none|warn|terminate"}
```

**On `action:"terminate"`:** the ingest edge immediately drops the stream. A `STREAM_ENDED{reason:"terminated", rule_id:"CS-01"}` event is emitted. The system does not wait for a human reviewer before stopping a stream that a classifier has flagged for CSAM or graphic violence — this is the one place where automated action *precedes* human review, because the harm of a 10-minute delay in a live context is concrete. The human review follows immediately after termination, with the ability to overturn and restore (reinstatement within 15 minutes means viewers experience a brief interruption, not a permanent loss). The frame evidence is retained for the review.

---

## 5. Private video — WebRTC architecture

```
Caller                    LUMENA SERVER                    Callee
  │                            │                              │
  ├── POST /pv/requests ───────►│                              │
  │                            ├── PV_REQUEST via WS ─────────►
  │                            │                              │
  │◄─────────────── PV_ACCEPTED (or DECLINED) ───────────────┤
  │                            │                              │
  │                       [gate chain]                        │
  │                       [billing hold]                      │
  │                            │                              │
  ├── SDP_OFFER ───────────────►├── SDP_OFFER ───────────────►│
  │◄─────────────────────────── SDP_ANSWER ──────────────────┤
  │                            │ (ICE candidates exchange)    │
  │◄══════════════════ WebRTC media (P2P or relayed) ════════►│
  │                            │                              │
  │                       [billing intervals]                 │
  │                            │                              │
  ├── POST /pv/sessions/{id}/end►                              │
  │                            ├── [settle ledger]            │
  │◄──────────────────── PV_SESSION_ENDED ───────────────────►│
```

**STUN/TURN strategy:**
- Try P2P via STUN first (free, lowest latency).
- If symmetric NAT on either side: TURN relay (encrypted, billed per GB — budget in the infrastructure cost model).
- TURN credentials are short-lived (4h), per-session, from `GET /private-video/sessions/{id}/ice`.
- The server never sees the media stream in the P2P case. In the relayed case the TURN server proxies encrypted SRTP — the payload is opaque to it.

**Safety exception:** frame classification for CSAM/nudity in private sessions (PV-07). In jurisdictions where legal obligations require it, a sample of frames is extracted at the TURN relay (encrypted relay → server must relay → server can see). This is disclosed in the privacy policy. Where P2P is established, frame sampling is done client-side via a local model with hash-matching (PhotoDNA-compatible hashes sent to the server; the raw frames are not). This is complex and the implementation plan is in doc 10 §2.

**Billing intervals** (implemented in `pv_active_intervals`):
- Server records `started_at` when both peers have ICE in `connected` state.
- Server records `ended_at` on disconnect, re-records `started_at` on reconnect within 45s.
- Settlement: `SUM(ended_at - started_at)` in complete minutes, rounded up, times `rate_coins_per_min`.
- Pre-authorized amount ensures the caller cannot exceed their hold.
- Unused hold is released atomically in the settlement transaction.

---

## 6. Stream health states (broadcaster view)

| State | Trigger | Action |
|---|---|---|
| `GOOD` | bitrate ≥ 80% target, rtt <100ms, drops <2% | Green indicator |
| `DEGRADED` | any threshold missed | Amber indicator, suggest lower quality setting |
| `POOR` | bitrate <40% target OR rtt >500ms | Red indicator, offer audio-only mode |
| `RECONNECTING` | ingest disconnect | Amber spinner, backoff, `STREAM_PAUSED` to viewers |
| `TERMINATED` | moderation or host action | Stream ends, reason displayed |

---

## 7. Recording & evidence

Live stream recording (opt-in by broadcaster, separate consent disclosure): uploaded to S3, retained per regional law, accessible only via `GET /creator/recordings/{id}`.

Moderation evidence snapshots (mandatory, no user control): frame at termination + 60s rolling buffer retained for 90 days, accessible only to the moderation team + appeals. CSAM evidence retained per legal obligation, reported to NCMEC immediately.

---

## 8. Capacity planning

| Metric | Phase 1 target | Notes |
|---|---|---|
| Concurrent broadcasts | 500 | 2 ingest nodes, 4 CPU each |
| Concurrent viewers | 50,000 | CDN-served; ingest node count is independent |
| Transcoder throughput | 500 × 5 renditions | GPU transcoder, 1 card per 50 streams |
| Private video sessions | 1,000 | TURN relay: 1 Gbps per node |
| p99 ingest → viewer latency | <2.5s | LL-HLS 1s segments |
| p99 WebRTC connect time (PV) | <4s | incl. ICE |

Scale trigger: add ingest edge nodes at >70% concurrent broadcast capacity. TURN nodes at >60% bandwidth. CDN capacity is elastic.
