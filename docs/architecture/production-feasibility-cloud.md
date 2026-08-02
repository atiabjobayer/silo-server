# Silo Production Feasibility — Cloud Deployment

## 50 TB Owned Library · 1,000 Users · 500 Concurrent Streams · Bangladesh-Only Delivery

**Date:** 2026-07-31
**Companion documents:** `production-feasibility-onprem.md`, `production-deployment-recommendation.md`
**Scope:** Running Silo as a commercial streaming service on public cloud, with all media stored as real media files (MP4/MKV) on shared storage, delivered exclusively to Bangladeshi IP space.

> **Pricing disclaimer.** Every figure in this document is a planning estimate assembled in July 2026 from public list pricing. Cloud list prices change, and Bangladeshi bandwidth and colocation pricing is not publicly published — it is quoted per customer. Treat the *models and ratios* here as the durable output, and re-run the arithmetic against live quotes before committing capital. Where a number is a genuine estimate rather than a published rate, it is marked **(est.)**.

---

## 1. Executive Summary

**Verdict: technically feasible on AWS, but economically the worst of the realistic options, and it cannot deliver the user experience a Bangladesh-only service needs.**

Three findings drive that conclusion, and all three come from the specifics of this workload rather than from general cloud skepticism:

1. **Egress dominates the bill.** At the planning workload (~235 TB/month delivered), AWS Mumbai data transfer alone is **~$15,700/month** — more than the entire compute, database, and storage footprint combined. Egress is ~47 % of the on-demand AWS bill and does not benefit from Reserved Instances or Savings Plans.

2. **Silo requires 50 TB of shared POSIX storage, which is the second-worst thing to buy in a cloud.** Silo addresses media by absolute filesystem path (`MediaPath` is baked into the signed stream token and passed straight to `http.ServeFile` and to ffmpeg). It cannot read from S3. That forces EFS (~$18,000/month at this size), FSx (~$5,000/month), or self-managed ZFS-on-EBS (~$5,400/month). The equivalent owned hardware — including chassis, redundancy, and a full replica node — works out to roughly **$150 per usable TB, once**, against **~$1,980 per usable TB over three years** on EBS `st1`. That is a ~13× difference on the fully-loaded comparison.

3. **There is no AWS region in Bangladesh, and the nearest region defeats BDIX.** Serving Bangladeshi users from Mumbai means every byte crosses the border as international transit. In Bangladesh, that is not merely a latency question (≈40–55 ms vs ≈5–20 ms in-country) — it is a *throughput* question. Bangladeshi consumer broadband packages routinely deliver far higher speeds to BDIX-peered domestic content than to international destinations. A BD-only streaming service hosted outside BD is competing against its own users' ISP shaping.

**Headline numbers:**

| Option | Monthly | 3-year TCO | Latency from Dhaka |
| --- | --- | --- | --- |
| AWS Mumbai, on-demand | ~$33,700 | ~$1,213,000 | 40–55 ms, international |
| AWS Mumbai, 3-yr commit + negotiated egress | ~$22,600 | ~$813,000 | 40–55 ms, international |
| Rented bare metal, Singapore (OVH/Vultr-class) | ~$7,000–9,000 | ~$252,000–324,000 | 60–80 ms, international |
| **On-prem, Dhaka Tier-3 colo** (see companion doc) | ~$16,800 all-in | **~$560,000 net** | **5–20 ms, BDIX** |

If cloud is chosen anyway, **do not choose AWS**. A rented bare-metal provider with generous bandwidth allowances costs roughly one third of committed AWS for this workload. AWS's advantages — elasticity, managed services, breadth — are of limited value to a workload whose cost is 47 % egress and whose peak is predictable to the hour.

---

## 2. What the Silo Codebase Actually Requires

Any sizing exercise that ignores these constraints will produce a deployment that does not work. Each item below is derived from the code, with references.

### 2.1 Every streaming node needs the full library mounted at an identical absolute path

When a client calls `/playback/start`, the API server signs a stream token whose claims include the raw filesystem path of the media file:

```go
// internal/api/handlers/playback.go:1731
tokenClaims.MediaPath = effectiveFile.FilePath
```

The proxy node then serves that path directly off its own filesystem:

```go
// internal/proxy/server.go:153
http.ServeFile(w, r, claims.MediaPath)
```

Transcode nodes do the same thing with ffmpeg. The API server also needs the mount for the scanner, `ffprobe`, and external/downloaded subtitle files (which, per `internal/api/handlers/playback.go:1755-1757`, deliberately stay on the API server rather than moving to the proxy).

**Consequence:** the 50 TB library must be mounted, at the same absolute path, on every API node, every proxy node, and every transcode node. There is no object-storage path for media — `internal/s3client` exists, but its consumers are artwork, branding, metadata, and user-DB buckets only, never media playback.

**This is the single most expensive architectural constraint in a cloud deployment**, and the one most worth engineering around (see §8.1).

### 2.2 Proxy nodes are client-facing; Silo does its own stream load balancing

The stream URL handed to the client points *directly* at the selected proxy node's public URL:

```go
// internal/api/handlers/playback.go:1748
resp.StreamURL = proxyNode.URL + "/stream/direct/" + token
```

Node selection happens inside Silo's own planner (`internal/nodepool/planner.go`), which implements least-connections transcode selection, round-robin proxy selection, co-location groups, per-node `MaxJobs` caps, and per-node `MaxBandwidthKbps` admission control against a 60-second rolling egress meter.

**Consequences:**
- Each proxy node needs its own public DNS name, public IP, and TLS certificate. They are not behind a load balancer.
- An external load balancer in front of the proxy tier would *bypass* Silo's capacity admission and defeat the bandwidth caps. Do not put an ALB in front of proxy nodes.
- The ALB/NLB is only needed in front of the **API** tier.

### 2.3 The API tier requires sticky sessions

`SessionManager` holds sessions in a plain in-process map:

```go
// internal/playback/session.go:155-157
type SessionManager struct {
	sessions map[string]*Session
	mu       sync.RWMutex
```

`RealtimeHub` (`internal/playback/realtime_hub.go:33-37`) is likewise an in-process map of WebSocket connections keyed by session ID. The `playback_sessions_sync` table is a one-way reporting projection for the admin live view (`internal/worker/reconciler.go`), not a shared session store.

**Consequence:** every request in a playback session's lifecycle — progress reports, audio-track switches, stop, the realtime WebSocket — must land on the API server that created the session. **Enable sticky sessions on the ALB** (application-controlled cookie, or source-IP affinity on an NLB).

### 2.4 Per-user concurrent-stream limits are enforced per API process, not globally

`SessionManager.ActiveCount(userID)` and `TranscodeCount(userID)` count only the sessions in *that process's* map (`internal/playback/session.go:1089-1102`). The optional policy admission decider receives those same in-memory counts (`AdmissionRequest` doc comment: "Counts are computed by SessionManager from live in-memory sessions").

**Consequence:** with N API servers, a user who deliberately spreads sessions across servers can open up to **N × their plan limit**. For a paid service with tiered "how many screens" pricing, this is a direct revenue leak and an anti-abuse hole. Sticky sessions mitigate it substantially but do not close it (a determined user can force re-balancing). Treat closing this as a **pre-launch engineering task**, not an infrastructure setting. See §9.1.

### 2.5 Local transcode fallback must be disabled on a multi-API deployment

By default an API server will transcode locally when no pooled transcode node is eligible (`internal/nodepool/planner.go:365-377`, defaulting to allowed). The project's own architecture notes flag the hazard: without sticky affinity, two front-ends can run divergent ffmpeg processes against the same session directory (`docs/architecture/restart-resilient-playback.md`, issue P-1, "Integrated-transcode split-brain across front-ends", disposition *deferred*).

**Consequence:** set `playback.local_transcode_fallback = false` and rely exclusively on dedicated `--mode=transcode` nodes. Node-routed sessions are immune to the split-brain because the transcode node URL is pinned in the token.

### 2.6 There is no source-media caching layer

Silo caches transcode *output* (HLS segments on the transcode node's local disk) and subtitle extracts, but nothing caches *source* media reads. Every direct-play stream reads the source file from shared storage for the entire duration of playback.

**Consequence:** the shared storage backend must sustain the full aggregate source-read bandwidth of all concurrent sessions — roughly 600–800 MB/s at 500 streams. This is a real constraint on EFS/FSx throughput-mode selection and on EBS volume striping.

### 2.7 HLS segments accumulate for the whole session

The HLS muxer runs with `-hls_list_size 0` and `-hls_flags independent_segments+temp_file` (`internal/playback/transcode.go:397-403`). There is no `delete_segments`. Segments therefore accumulate on the transcode node's disk for the entire duration of a session; the directory is removed when the session ends (10-minute idle reaper, `internal/transcodenode/server.go:71`) or by the orphan sweep after `MaxTokenTTL` = 24 h (`internal/playback/recipecard.go:168`).

**Consequence:** a 2-hour session at 6 Mbps leaves ~5.4 GB on disk by the end. Size transcode-node scratch at **1–2 TB of NVMe per node**, not the 120 GB used in the single-box Proxmox guide.

### 2.8 Hardware acceleration is fully supported, with one important exception

`ResolveHWAccelWithFFmpeg` prefers **nvenc > qsv > vaapi > none** (`internal/playback/gpudetect.go`). CPU fallback is `libx264 -preset veryfast -crf 23` (`internal/playback/transcode.go:657`).

The exception: **subtitle burn-in forces a GPU→CPU→GPU round trip.** For NVENC the filter chain becomes `hwdownload,format=yuv420p,<subtitle filters>,format=nv12,hwupload_cuda` (`internal/playback/transcode.go:809-810`). Burn-in sessions therefore consume meaningful CPU *and* GPU and reduce per-GPU session density substantially. If a large share of the library carries PGS/bitmap subtitles that clients cannot render natively, GPU sizing must be increased.

### 2.9 Managed PostgreSQL breaks Silo's auto-tuning

Silo applies pgtune-style settings via `ALTER SYSTEM` at startup (`cmd/silo/main.go:236`, `maybeApplyPostgresTuning`). RDS and Aurora do not grant the superuser rights this needs.

**Consequence:** on RDS, set `POSTGRES_TUNE=off` and replicate the tuning by hand in an RDS parameter group. Confirm the RDS PostgreSQL version ships `pgvector` (Silo uses `pgvector-go` for recommendation embeddings) and `pg_partman`-style partition management is not needed — Silo manages its own log partitions in application code (`internal/partman`).

### 2.10 Redis is mandatory for proxy and transcode nodes

Proxy and transcode nodes run without a database but **require** Redis (`cmd/silo/main.go:623-627`: "redis is required for this mode"). They use it for the node config watcher, the node session tracker, and the jellycompat recipe handoff store (`internal/noderecipe`).

**Consequence:** Redis must be reachable from every node, and central and nodes must share the same Redis instance. It is on the critical path for streaming, not merely a cache. Size for availability, not just capacity.

### 2.11 Kubernetes fits Silo's node model badly

Proxy and transcode nodes are registered as **rows in PostgreSQL with static URLs** (`internal/nodepool/repository.go`), health-checked every 30 s at `{url}/api/v1/health`, and selected by Silo's own capacity-aware planner. Kubernetes pod churn — new IPs on every reschedule — fights this directly. Fronting a pool with a stable Service URL "fixes" registration but destroys the thing that makes the pool work: per-node job caps, per-node bandwidth admission, and co-location groups all collapse into one opaque endpoint round-robined by kube-proxy.

**Consequence:** run proxy and transcode nodes as **pets with stable DNS names** (EC2 instances in an ASG with stable ENIs/DNS, or plain instances), not as Kubernetes Deployments. This removes most of the argument for EKS. The API tier *could* run on Kubernetes, but with sticky sessions required and only three replicas, the operational benefit is thin.

---

## 3. Workload Model

All sizing in both documents uses this shared model. Adjust these inputs and the rest follows arithmetically.

### 3.1 Stream-mix scenarios

The single largest cost lever in the whole analysis is **what fraction of streams direct-play versus transcode**. Because the content is your own production-house material, you control this completely by choosing your delivery encode.

| Mix | Direct play | Remux (video copy) | Full video transcode | When this happens |
| --- | --- | --- | --- | --- |
| **A — Optimized** | 70 % (350) | 20 % (100) | 10 % (50) | Library normalized to H.264 High@L4.1 + AAC in MP4, with a pre-built ladder |
| **B — Realistic (planning target)** | 50 % (250) | 25 % (125) | 25 % (125) | Mixed containers/codecs; some bandwidth-capped mobile clients force downscales |
| **C — Unoptimized** | 25 % (125) | 25 % (125) | 50 % (250) | HEVC/10-bit/DTS masters served to mixed clients; heavy subtitle burn-in |

**All sizing below uses Scenario B.** Scenario A cuts GPU spend roughly in half; Scenario C roughly doubles it.

### 3.2 Bitrate model

Silo's built-in ladder (`internal/playback/plan_v3.go:797`):

| Rung | Bitrate |
| --- | --- |
| 480p | 1,500 kbps |
| 720p | 2,000 kbps |
| 1080p | 6,000 kbps |
| 2160p | 20,000 kbps |

**Critical caveat:** direct play delivers the **source file's** bitrate, not a ladder rung. If you direct-play production masters at 15–25 Mbps, your egress bill is 3–5× the model below. **Producing a delivery-grade ladder from your masters is a prerequisite, not an optimization.**

Assuming a proper delivery ladder and a mobile-heavy Bangladeshi audience:

| Case | Avg delivered bitrate | Peak egress @ 500 concurrent |
| --- | --- | --- |
| Low (720p-dominant) | 3.5 Mbps | 1.75 Gbps |
| **Planning** | **5 Mbps** | **2.5 Gbps** |
| High (1080p-dominant) | 8 Mbps | 4.0 Gbps |
| Danger (direct-play masters) | 15+ Mbps | 7.5+ Gbps |

### 3.3 Monthly egress volume

Monthly volume is driven by **average** concurrency, not peak. Peak sizes the hardware; average sizes the bandwidth bill.

GB per stream-hour = bitrate (Mbps) × 0.45.

| Avg concurrency | Peak:avg ratio | Monthly stream-hours | @3.5 Mbps | @5 Mbps | @8 Mbps |
| --- | --- | --- | --- | --- | --- |
| 60 | 8.3 | 43,800 | 69 TB | 99 TB | 158 TB |
| 100 | 5.0 | 73,000 | 115 TB | 164 TB | 263 TB |
| **143** | **3.5** | **104,390** | 164 TB | **235 TB** | 376 TB |
| 200 | 2.5 | 146,000 | 230 TB | 329 TB | 526 TB |

**Planning figure: 235 TB/month.**

> **A note on the 500-concurrent target.** 500 concurrent from 1,000 registered users is a 50 % simultaneous-use rate, which is far above what consumer streaming services see (typically 10–20 % at peak). Either the 500 figure is deliberate headroom for growth to ~3,000–5,000 subscribers, or usage will be unusually intense. This matters because **infrastructure is sized by peak concurrency while the bandwidth bill is driven by average concurrency** — so if you build for 500 peak and only reach 150, your capital is under-utilized but your monthly bill drops proportionally. Both documents size for 500 peak as requested, and §7 shows how per-user cost collapses as you add subscribers against the same peak.

### 3.4 Compute model per stream

Measured against a modern server core (EPYC 9004 / Xeon Scalable 4th gen class, ~3.0 GHz+):

| Path | CPU per session | Runs on |
| --- | --- | --- |
| Direct play (`sendfile`) | ~0.02 core | Proxy node |
| Progressive remux (copy video, AAC audio) | ~0.15 core | Proxy node |
| HLS remux (copy video, segment) | ~0.20 core | Transcode node |
| 1080p→1080p, x264 veryfast, CPU | ~2.0 cores | Transcode node |
| 1080p→720p, x264 veryfast, CPU | ~1.2 cores | Transcode node |
| 4K→1080p, x264 veryfast, CPU | ~5.0 cores | Transcode node |
| Any transcode with NVENC/QSV | ~0.5 core + GPU engine share | Transcode node |
| **Additional, subtitle burn-in** | **+0.8–1.5 cores** | Transcode node |

Because the throttler pauses ffmpeg once it is 60 s ahead of the client (`internal/playback/throttle.go:16`, minimum threshold 60 s), *sustained* transcode cost is close to 1× realtime. The initial burst before throttling engages runs at maximum speed, so a synchronized wave of session starts (a popular premiere) produces a CPU spike well above steady-state. Size for the burst, not the average.

### 3.5 GPU transcode density (1080p H.264, realtime)

| GPU | Concurrent 1080p sessions **(est.)** | Notes |
| --- | --- | --- |
| NVIDIA L4 | 28–35 | AWS `g6` family; best cloud option |
| NVIDIA A10G | 20–28 | AWS `g5` family |
| NVIDIA A2 | 18–22 | Low-profile, low-power |
| Intel Flex 140 | 30–36 | Excellent density/watt; limited cloud availability |
| Intel Arc A380 (QSV) | 15–20 | ~$150 card; on-prem only |

**Watch the NVENC session cap.** NVIDIA GeForce drivers historically limit concurrent NVENC sessions (3–8 depending on driver generation). Datacenter and professional cards (L4, A2, A10, RTX A-series) have no such cap. This rules out consumer GPUs for on-prem builds and is a non-issue on AWS `g5`/`g6`.

### 3.6 Storage I/O requirement

Storage reads are at the **source** bitrate, not the delivered bitrate. Assuming ~10 Mbps average source across the library:

- 500 concurrent × 10 Mbps = 5 Gbps = **~625 MB/s sustained aggregate read**
- Transcode sessions read faster than realtime until the 60 s throttle engages, so burst demand is materially higher
- **Design target: 1.5–2 GB/s sustained read** for headroom

---

## 4. AWS Reference Architecture (ap-south-1, Mumbai)

Region choice: **ap-south-1 (Mumbai)** at ~40–55 ms from Dhaka, versus ap-southeast-1 (Singapore) at ~55–75 ms. Mumbai also has better India–Bangladesh terrestrial connectivity. There is no AWS Local Zone or region in Bangladesh.

```
                    ┌──────────────────────────────────────────┐
                    │  Route 53 (geo/IP restricted to BD)      │
                    └───────────────┬──────────────────────────┘
                                    │
              ┌─────────────────────┼──────────────────────────┐
              │                     │                          │
     ┌────────▼────────┐   ┌────────▼─────────┐   ┌───────────▼──────────┐
     │  ALB (sticky)   │   │  proxy-01..04    │   │  WAF / geo-block     │
     │  → API tier     │   │  PUBLIC, direct  │   │  (BD IP allowlist)   │
     └────────┬────────┘   │  client-facing   │   └──────────────────────┘
              │            └────────┬─────────┘
     ┌────────▼────────┐            │  (reverse-proxies HLS)
     │  api-01..03     │            │
     │  --mode=api     │   ┌────────▼─────────┐
     └────────┬────────┘   │  tc-01..06       │
              │            │  --mode=transcode│
              │            │  g6.4xlarge (L4) │
              │            └────────┬─────────┘
              │                     │
     ┌────────┴─────────────────────┴──────────────────────────┐
     │            Shared POSIX media (50 TB, read-mostly)      │
     │     self-managed ZFS/NFS on EC2 + EBS st1  (see §4.5)   │
     └──────────────────────┬──────────────────────────────────┘
                            │
        ┌───────────────────┼───────────────────┐
        │                                       │
┌───────▼─────────┐                   ┌─────────▼─────────┐
│ RDS PostgreSQL  │                   │ ElastiCache Redis │
│ r7g.2xl Multi-AZ│                   │ r7g.large ×2      │
└─────────────────┘                   └───────────────────┘
```

### 4.1 API tier

| Item | Value |
| --- | --- |
| Instance | `c7i.2xlarge` (8 vCPU, 16 GB) |
| Count | **3** |
| Mode | `--mode=api` |
| Load balancer | ALB with **stickiness enabled** (§2.3) |
| Media mount | Required (scanner, ffprobe, external subtitles) |
| Setting | `playback.local_transcode_fallback = false` (§2.5) |

Three instances gives N+1 for a rolling deploy plus one failure. The API tier is not CPU-hungry — it serves catalog JSON, artwork (via libvips/bimg), and playback planning. Artwork resizing is the heaviest thing it does; the `imagecache` package and S3-backed artwork bucket absorb most of that.

### 4.2 Proxy tier

| Item | Value |
| --- | --- |
| Instance | `c7i.4xlarge` (16 vCPU, 32 GB, up to 12.5 Gbps) |
| Count | **4** |
| Mode | `--mode=proxy` |
| Public | **Yes** — own EIP, DNS name, TLS cert each |
| Media mount | **Required** (direct play + progressive remux run here) |
| Silo config | `MaxBandwidthKbps` = 1,500,000 per node (1.5 Gbps) |

Sizing: 125 remux × 0.20 core + 250 direct × 0.02 core ≈ **30 cores**, plus TLS termination and network interrupt handling. Four `c7i.4xlarge` = 64 vCPU, comfortable. The binding constraint is egress: 4 nodes × 1.5 Gbps cap = 6 Gbps of admission capacity against a 2.5 Gbps peak requirement, tolerating one node failure at 4.5 Gbps.

Set `MaxBandwidthKbps` deliberately below the instance's network ceiling. Silo's admission control uses a 60-second rolling average (`internal/proxy/egress.go`, `meterWindowSeconds = 60`) with a matching reservation bridge, so it reacts slowly. Leaving headroom prevents overshoot during a burst of session starts.

### 4.3 Transcode tier

| Item | Value |
| --- | --- |
| Instance | `g6.4xlarge` (1× NVIDIA L4, 16 vCPU, 64 GB) |
| Count | **6** |
| Mode | `--mode=transcode` |
| Media mount | Required |
| Scratch | 1–2 TB gp3 per node for HLS segments (§2.7) |
| Silo config | `MaxJobs` = 30 per node |

Sizing for Scenario B (125 concurrent video transcodes):
- 125 ÷ 28 sessions per L4 = 4.5 GPUs → **5 for capacity, 6 for N+1**
- CPU check: 125 × 0.5 core (NVENC overhead) = 63 cores; 6 × 16 vCPU = 96 vCPU ✓
- Burn-in headroom: the extra 16 vCPU per L4 exists precisely to absorb subtitle burn-in round trips (§2.8)

Why `g6.4xlarge` and not `g6.xlarge`: `g6.xlarge` has only 4 vCPU per L4, which starves ffmpeg's muxing, filtering, and HLS segmenting the moment any session needs a CPU-side filter. The 16 vCPU variant is the right shape for this workload.

**Spot instances are tempting here and should be used carefully.** Transcode nodes are restart-resilient by design — the node self-reconstructs a lost session from the forwarded `X-Silo-Stream-Token` (`internal/proxy/server.go:355`, `internal/transcodenode/server.go:596`). But reconstruction re-transcodes from the requested segment, which means a Spot reclamation produces a visible rebuffer for every session on that node. Use Spot for **burst capacity above the baseline**, keep the baseline on-demand or Reserved.

### 4.4 Database

| Item | Value |
| --- | --- |
| Service | RDS PostgreSQL, `db.r7g.2xlarge` (8 vCPU, 64 GB), Multi-AZ |
| Storage | 1 TB gp3 |
| Extensions | `pgvector` (required — recommendation embeddings) |
| Config | `POSTGRES_TUNE=off`; replicate tuning in a parameter group (§2.9) |
| Pooling | PgBouncer on the API instances, or RDS Proxy |

Load estimate: 500 sessions reporting progress every ~5 s = ~100 writes/s, plus session-sync reconciliation, partitioned activity/policy logs, and read-heavy catalog browsing for 1,000 users. This is modest — `db.r7g.2xlarge` has substantial headroom and is chosen for growth rather than current need.

Expected database size: for a 50 TB library at production-house file sizes (large files, relatively few of them — likely 20,000–50,000 media files), catalog metadata plus partitioned logs plus embeddings lands around **50–200 GB**. The 1 TB allocation is for log growth and comfort.

**Connection pooling matters.** `planstore.SessionLockCapacity` deliberately caps advisory-lock holders at half the pool (`internal/playback/planstore/postgres.go:26-36`) because each holder pins a connection. With three API servers each holding a pool, size `max_connections` accordingly and put PgBouncer in front.

### 4.5 Shared media storage — the hard problem

Silo needs 50 TB of shared POSIX storage readable at ~625 MB/s sustained (§3.6) from ~13 instances. Four options, all bad in different ways:

| Option | Monthly (50 TB, ap-south-1) | Verdict |
| --- | --- | --- |
| **EFS Standard** | ~$18,000 **(est.)** | Disqualifying. EFS is priced for small shared filesystems. |
| **FSx for OpenZFS (SSD)** | ~$5,000 **(est.)** | Workable but expensive; managed, less operational burden |
| **Self-managed ZFS/NFS on EC2 + EBS st1** | **~$5,400** | **Recommended.** Best cost/throughput; you operate it |
| **S3 + Mountpoint** | ~$1,250 | Cheapest by far — **but Silo cannot use it** (§8.1) |

**Recommended build (self-managed):**

| Component | Spec | Monthly |
| --- | --- | --- |
| NFS/ZFS servers | 2× `m7i.4xlarge` (16 vCPU, 64 GB), active/standby | ~$1,431 |
| Bulk capacity | 65 TB EBS `st1` @ ~$0.055/GB **(est.)** | ~$3,575 |
| Metadata/cache | 4 TB EBS `gp3` | ~$400 |
| **Subtotal** | | **~$5,406** |

Notes:
- `st1` throughput scales at 40 MB/s per TB, capped at 500 MB/s per volume. Reaching 625 MB/s+ requires **striping several large volumes** — plan 4–6 volumes of 12–16 TB each in a ZFS stripe of mirrors or RAIDZ.
- 65 TB provisioned for 50 TB usable accounts for ZFS parity/overhead plus growth headroom.
- Give the NFS servers large ARC (the 64 GB RAM on `m7i.4xlarge`) — under the Zipf-distributed access typical of media catalogues, a well-warmed ARC absorbs a large share of reads and takes real pressure off `st1`.
- Consider `i4i`/`i7i` instance-store NVMe as an L2ARC tier if read latency proves problematic.

### 4.6 Redis

| Item | Value |
| --- | --- |
| Service | ElastiCache for Redis, `cache.r7g.large` |
| Count | 2 (primary + replica, Multi-AZ, automatic failover) |
| Monthly | ~$330 **(est.)** |

Memory need is small (node session tracker, config watcher, recipe store, rate limits). Redundancy — not capacity — is why this is two nodes: a Redis outage takes every proxy and transcode node offline (§2.10).

### 4.7 Networking and edge

| Item | Monthly **(est.)** |
| --- | --- |
| ALB (API tier only) + LCUs | ~$125 |
| NAT Gateway ×2 + processing | ~$182 |
| Route 53, ACM, misc | ~$50 |
| **Subtotal** | **~$357** |

**Bangladesh-only enforcement.** Use AWS WAF with a geo-match rule allowing only `BD`, attached to the ALB — plus, because proxy nodes are *not* behind the ALB (§2.2), a matching restriction on each proxy node (security-group-level is impractical for a country CIDR set, so enforce in the proxy's own reverse proxy or via a WAF-enabled CloudFront distribution in front of each). This is a genuine wrinkle: Silo's architecture puts public listeners on every proxy node, so your geo-blocking must be applied per node, not once at the edge.

---

## 5. AWS Cost Summary

### 5.1 On-demand

| Line item | Qty | Monthly |
| --- | --- | --- |
| Transcode — `g6.4xlarge` @ $1.5889/hr | 6 | $6,959 |
| Proxy — `c7i.4xlarge` @ ~$0.86/hr **(est.)** | 4 | $2,511 |
| API — `c7i.2xlarge` @ ~$0.43/hr **(est.)** | 3 | $942 |
| Storage instances — `m7i.4xlarge` @ ~$0.98/hr **(est.)** | 2 | $1,431 |
| EBS — 65 TB `st1` + 4 TB `gp3` | — | $3,975 |
| RDS — `db.r7g.2xlarge` Multi-AZ + 1 TB | 1 | $1,454 |
| ElastiCache — `cache.r7g.large` | 2 | $330 |
| Networking — ALB, NAT, DNS | — | $357 |
| **Infrastructure subtotal** | | **$17,959** |
| **Data transfer out — 235 TB** | | **$15,740** |
| **TOTAL** | | **~$33,700/month** |

### 5.2 Egress calculation detail

AWS ap-south-1 data transfer out to internet, tiered:

| Tier | Rate | Volume | Cost |
| --- | --- | --- | --- |
| First 10 TB | $0.109/GB | 10,000 GB | $1,090 |
| Next 40 TB | $0.085/GB | 40,000 GB | $3,400 |
| Next 100 TB | $0.070/GB | 100,000 GB | $7,000 |
| Over 150 TB | $0.050/GB | 85,000 GB | $4,250 |
| **Total** | | **235,000 GB** | **$15,740** |

**CloudFront does not help.** CloudFront's India price class runs roughly $0.085–0.109/GB at these volumes — comparable to or worse than direct EC2 egress, because the tiering is shallower. CloudFront earns its keep when caching offloads origin work; for a long-tail video catalogue with per-session signed tokens and per-session HLS renditions, the cache hit rate on *transcoded* output is near zero (every session has its own segment directory). It can help for direct-play of popular titles, but not enough to change the conclusion.

### 5.3 With 3-year commitments

| Line item | On-demand | Committed |
| --- | --- | --- |
| EC2 (all tiers) — 3-yr Compute Savings Plan, ~45 % | $11,843 | $6,514 |
| EBS (no discount available) | $3,975 | $3,975 |
| RDS — 3-yr Reserved, ~35 % | $1,454 | $945 |
| ElastiCache — 3-yr Reserved, ~35 % | $330 | $215 |
| Networking | $357 | $357 |
| Egress — negotiated private pricing @ ~$0.045/GB **(est.)** | $15,740 | $10,575 |
| **TOTAL** | **$33,699** | **~$22,581** |

**3-year AWS TCO: ~$813,000 committed / ~$1,213,000 on-demand.**

Negotiated egress pricing at ~$0.045/GB assumes an annual commitment in the $120k+ range, which this workload comfortably clears. Engage AWS sales before signing anything at list price — private pricing agreements on data transfer are where the real negotiation happens for video workloads.

---

## 6. Cheaper Cloud Alternatives

AWS's pricing structure is poorly matched to this workload. Providers that bundle generous bandwidth allowances with dedicated hardware are dramatically cheaper.

### 6.1 Comparison of egress economics

| Provider | Effective egress rate | Cost for 235 TB/month |
| --- | --- | --- |
| AWS ap-south-1 (list) | ~$0.067/GB blended | ~$15,740 |
| AWS ap-south-1 (negotiated) **(est.)** | ~$0.045/GB | ~$10,575 |
| DigitalOcean (Bangalore) | $0.01/GB over pooled quota | ~$2,350 |
| Vultr (Mumbai) | $0.01/GB over pooled quota | ~$2,350 |
| Akamai/Linode (Mumbai) | $0.005/GB over pooled quota | ~$1,175 |
| OVHcloud bare metal (Singapore) | Largely unmetered | ~$0–500 |
| Hetzner (Europe only) | €1/TB | ~€235 |

**The spread is 13–60×.** No amount of compute optimization matters next to this.

### 6.2 The catch: GPU availability in South Asia

Cheap-egress providers generally do **not** offer GPU instances in Mumbai or Singapore. This is the real constraint on moving off AWS.

Options, in order of preference:

1. **Rented bare metal with your own GPUs.** Some providers (Vultr Bare Metal, OVH Advance/Scale) offer dedicated servers with GPUs or allow GPU add-ons in select regions. Verify availability in Singapore/Mumbai specifically.
2. **Split the tiers.** Keep GPU transcode on AWS `g6` (small footprint, ~$7,000/month), and put the egress-heavy proxy tier on a cheap-bandwidth provider. This works because Silo's proxy nodes are stateless and independently addressed — but it introduces cross-provider traffic between proxy and transcode nodes for HLS sessions, which is itself egress. Only worthwhile if the direct-play share is high (Scenario A).
3. **CPU-only transcode on cheap dedicated hardware.** 125 concurrent transcodes × 2 cores = 250 cores. At ~$960/month for a 32-core dedicated instance, that is ~$7,700/month — no cheaper than AWS GPU, and worse.
4. **Reduce the transcode share to near zero** by normalizing the library (Scenario A drops it to 50 concurrent transcodes, 2 GPUs). This is the highest-leverage move available and is worth doing regardless of hosting choice.

### 6.3 Rented bare metal reference build (Singapore)

| Role | Qty | Spec | Monthly **(est.)** |
| --- | --- | --- | --- |
| Transcode | 4 | Dedicated, 32c + GPU | $3,600 |
| Proxy | 4 | Dedicated, 16c, 10 Gbps unmetered | $1,200 |
| API | 3 | Dedicated, 8c | $450 |
| Database | 2 | Dedicated, 32c/256 GB, NVMe | $800 |
| Storage | 2 | Dedicated, 60 TB HDD + NVMe cache | $1,000 |
| Bandwidth | — | Largely included | $0–500 |
| **Total** | | | **~$7,050–9,000** |

**3-year TCO: ~$252,000–324,000** — roughly **one third of committed AWS**, and cheaper than the on-prem build's net TCO.

**But:** Singapore is 60–80 ms from Dhaka, and all traffic is international transit. See §8.2 for why that is a bigger problem in Bangladesh than the latency number suggests.

### 6.4 Bangladesh-local cloud and colocation providers

For a BD-only service, in-country hosting is worth investigating even if you do not buy hardware. Providers offering IaaS/VPS/colocation with BDIX peering include Aamra Networks, Summit Communications, Link3 Technologies, Exabyte, BDCOM, and the Bangladesh Data Center Company (BDCCL) national facility.

**Renting VMs in a BD data centre is a genuine middle path**: it captures the BDIX advantage without the capital outlay, at the cost of less hardware control and typically no GPU availability. Get quotes. If a BD provider can supply GPU-equipped dedicated servers, that combination is likely the single best option available and would beat both AWS and the on-prem build.

---

## 7. Scaling and Per-User Cost

### 7.1 The key structural fact

**Infrastructure is sized by peak concurrency. Revenue is driven by subscriber count.** These are only loosely coupled, through the peak concurrency ratio *R* (peak concurrent streams ÷ registered users). Per-user cost therefore collapses as you add subscribers without changing *R*'s numerator.

### 7.2 Per-user cost at fixed 500-concurrent capacity

Using committed AWS at ~$22,581/month:

| Subscribers | Implied *R* | Cost per user/month |
| --- | --- | --- |
| 1,000 | 50 % | **$22.58** |
| 2,500 | 20 % | $9.03 |
| 5,000 | 10 % | $4.52 |
| 10,000 | 5 % | $2.26 |

The 10 %–20 % band is where real consumer streaming services sit. **A 500-concurrent AWS build is economically sensible at 2,500–5,000 subscribers, not at 1,000.** At 1,000 subscribers you are paying for capacity you cannot fill.

### 7.3 Marginal cost of additional capacity (AWS)

Cost to add **one concurrent stream of peak capacity**, sustained:

| Component | Derivation | Monthly per concurrent stream |
| --- | --- | --- |
| Egress | 0.286 avg concurrent × 730 h × 2.25 GB × $0.045 | $21.15 |
| Transcode GPU | $1.5889/hr × 730 ÷ 28 sessions × 25 % mix, autoscaled to average | $2.96 |
| Proxy + API | amortized | ~$1.00 |
| Storage / DB | ~0 until 2,000+ concurrent | $0 |
| **Total marginal** | | **~$25/concurrent stream/month** |

Marginal cost per additional **subscriber** = $25 × *R*:

| *R* (peak concurrency ratio) | Marginal cost per subscriber/month |
| --- | --- |
| 50 % | $12.50 |
| 25 % | $6.25 |
| 12.5 % | $3.13 |

**Compare to on-prem's ~$8/concurrent stream/month** (companion document, §10) — AWS is **~3× more expensive at the margin**, and the gap is essentially all egress. That ratio is the number to remember: it does not improve with scale, because egress pricing tiers flatten out and on-prem bandwidth gets cheaper per Mbps as you commit more.

---

## 8. Catches, Risks, and Things That Will Bite

### 8.1 Silo cannot use object storage for media — and fixing that is the highest-value cloud change

S3 at $0.025/GB-month would cost **$1,250/month** for 50 TB versus $5,406 for self-managed NFS — and it would eliminate the storage servers, the EBS striping, the NFS single-point-of-failure, and the capacity planning entirely.

It is blocked by §2.1: `MediaPath` is a POSIX path, consumed by `http.ServeFile` and by ffmpeg's file input.

Two paths forward:
- **Mountpoint for Amazon S3 / s3fs.** Works for sequential reads; ffmpeg's seek behaviour on remux and on transcode start (`-ss` before input) will be poor. Worth *benchmarking* before dismissing — Silo's direct-play path is `http.ServeFile`, which is largely sequential with Range requests, and might behave acceptably. The transcode path is riskier.
- **Teach Silo to resolve media to a URL.** The `.strm` machinery already does exactly this — `resolveTranscodeInputPath` (`internal/playback/transcode.go:231`) turns a `.strm` shortcut into a remote HTTP URL that ffmpeg opens directly. Extending that to presigned S3 URLs for direct play and transcode input is a **contained, well-precedented change** to a codebase that already has the abstraction. This is the single highest-leverage engineering investment for a cloud deployment.

Note the second option also has direct on-prem value: it would let proxy nodes stream from MinIO/Ceph RGW rather than requiring a POSIX mount everywhere.

### 8.2 International transit is a product problem in Bangladesh, not just a latency number

This is the finding most likely to be underestimated.

Bangladeshi consumer ISPs commonly sell packages that differentiate sharply between **BDIX (domestic, peered) traffic** and **international traffic**. Domestic-peered content frequently delivers several times the throughput of international content on the same subscription, and many packages advertise "BDIX unlimited" or similar tiers. There is an entire hosting industry in Bangladesh built specifically around BDIX-peered hosting for exactly this reason.

**A streaming service hosted in Mumbai or Singapore is, from the end user's perspective, international content.** Your 5 Mbps stream competes for the user's international bandwidth allocation — which on a mid-tier BD broadband package may be substantially constrained, and on mobile is subject to the operator's own transit economics.

Consequences you should plan around:
- Higher rebuffer rates and more ABR downshifts than your bandwidth model predicts
- Worse experience specifically for lower-tier subscribers — your most price-sensitive segment
- No ability to negotiate directly with BD ISPs for peering, because you have no BD presence

**This alone is a strong argument for in-country hosting**, independent of cost.

### 8.3 Mobile operator peering

Grameenphone, Robi, and Banglalink carry the majority of Bangladeshi internet users. Their peering arrangements with BDIX are partial. Even an in-country deployment should not assume BDIX covers the whole market — budget for direct peering negotiations or paid domestic transit to reach mobile subscribers well. This affects the on-prem bandwidth estimate as much as the cloud one.

### 8.4 Per-user stream limits do not hold across API servers

Restating §2.4 because of its commercial impact: if your pricing has a "streams per plan" dimension, that limit is **not enforced globally** in a multi-API deployment. Options:

1. Sticky sessions (necessary anyway) — reduces but does not eliminate the gap
2. Move admission counting to Redis or Postgres before launch — the right fix
3. Run a single API server — caps you at one instance's capacity and removes rolling deploys
4. Accept it and monitor for abuse via `playback_sessions_sync`, which *does* have a global view

For a commercial launch, option 2 is the correct answer and should be scheduled as pre-launch work.

### 8.5 Redis is a hard dependency for the entire streaming plane

A Redis outage stops proxy and transcode nodes from starting and breaks the node session tracker and recipe store. Multi-AZ ElastiCache with automatic failover is the minimum. Test failover behaviour explicitly — confirm that in-flight sessions survive a Redis failover, and that nodes recover without manual restart.

### 8.6 Health-check and admission control granularity

Health checks run every 30 s (`internal/nodepool/health.go:65`); the egress meter is a 60 s rolling average; planner reservations bridge 60–90 s. Admission control is therefore **coarse**. A synchronized wave of session starts — a premiere, a push notification, a scheduled release — can substantially overshoot a node's `MaxBandwidthKbps` before the meter catches up. Set caps conservatively (§4.2) and consider staggering release-time notifications.

### 8.7 Spot interruptions cause visible rebuffers

Covered in §4.3. Transcode nodes reconstruct correctly but not invisibly.

### 8.8 Cost of being wrong about the stream mix

Scenario C (50 % transcode) roughly doubles GPU spend to ~$14,000/month and adds ~$7,000/month to the AWS bill. **Measure your actual direct-play rate against real client devices before committing to instance reservations.** Silo reports `play_method` per session in `playback_sessions_sync` — instrument this from day one of any pilot.

### 8.9 Regulatory and data-residency considerations

Bangladesh has an evolving regulatory posture on data localization, telecom licensing, and content distribution. A commercial streaming service operating in Bangladesh may face licensing requirements (BTRC), content classification obligations, and pressure toward in-country data storage. **Get local legal advice before choosing a hosting jurisdiction** — this may turn out to be a constraint rather than a preference, and it points the same direction as the technical and economic analysis.

---

## 9. Pre-Launch Engineering Checklist

Independent of hosting choice, these should be resolved before taking paying customers:

| # | Item | Why | Effort |
| --- | --- | --- | --- |
| 1 | Global per-user stream-limit enforcement (§8.4) | Revenue leak | Medium |
| 2 | Build a delivery encode ladder from masters (§3.2) | 3–5× egress reduction | Medium |
| 3 | Set `playback.local_transcode_fallback = false` (§2.5) | Prevents split-brain | Trivial |
| 4 | Sticky sessions on the API load balancer (§2.3) | Correctness | Trivial |
| 5 | Per-node geo-blocking on proxy nodes (§4.7) | BD-only requirement | Low |
| 6 | Transcode scratch sized to 1–2 TB/node (§2.7) | Prevents disk-full outages | Trivial |
| 7 | Instrument `play_method` distribution (§8.8) | Validates all sizing | Low |
| 8 | Evaluate S3/URL-based media resolution (§8.1) | Large cost reduction | Medium-High |
| 9 | Load-test at 50 → 150 → 500 concurrent | Validates the model | Medium |
| 10 | Redis failover drill (§8.5) | Streaming-plane availability | Low |

---

## 10. Verdict

AWS **can** run this workload. The reference architecture in §4 is sound and would work.

But at 1,000 subscribers the committed AWS cost of **~$22,600/month is $22.58 per subscriber per month** — a figure that is difficult to reconcile with Bangladeshi consumer streaming price points. Even at 5,000 subscribers ($4.52/user) the economics are only workable because the cost has been spread, not reduced.

The decisive problems are structural rather than fixable by tuning:

- **47 % of the on-demand bill is egress**, which no reservation discounts and which is 13–60× cheaper on other providers
- **Shared POSIX storage at 50 TB is a cloud anti-pattern**, costing more per month than owning the drives outright costs once
- **There is no AWS region in Bangladesh**, so a Bangladesh-only service is served as international content to an audience whose ISPs treat domestic and international traffic very differently

**Recommendation: do not build this on AWS.** See the companion on-prem analysis and the recommendation document for the preferred path, which is a Dhaka Tier-3 colocation build with cloud used for burst transcode and disaster recovery rather than as the primary platform.

---

## Sources

- [AWS Data Transfer Out to Internet Pricing 2026 — EgressCost.com](https://egresscost.com/aws/data-transfer-pricing/)
- [AWS Pricing in Mumbai (2026) — PrecisionTech](https://precisiontech.in/cloud/amazon-aws-cloud/aws-pricing/aws-pricing-in-mumbai/)
- [g6.4xlarge Specs & Pricing in ap-south-1 — DoiT Compute](https://www.doit.com/compute/spot/ap-south-1/g6.4xlarge)
- [g6.xlarge Specs & Pricing in ap-south-1 — DoiT Compute](https://www.doit.com/compute/spot/ap-south-1/g6.xlarge)
- [Amazon EC2 G6 Instances — AWS](https://aws.amazon.com/ec2/instance-types/g6/)
- [BDIX — Bangladesh Internet Exchange, Data Center Map](https://www.datacentermap.com/ixp/bdix/)
