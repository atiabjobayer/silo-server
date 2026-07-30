# Silo Production-Scale Feasibility Report

## Remote Media Origin · 1000 Users · 500 Concurrent Streams · AWS/DO + K8s

**Date:** 2026-07-29
**Scope:** Deploying Silo at production scale where media files live on a local-ISP-accessible HTTP server and must not be cloned to cloud storage.

---

## 1. Executive Summary

**Verdict: Feasible, but the home ISP upload link is the hard throughput ceiling.**
No amount of cloud compute can paper over a 100 Mbps (or even 1 Gbps) residential upload pipe when 500 concurrent viewers each need 5–20 Mbps of source data pulled through it.

The realistic path is a **hot-content caching layer in AWS** combined with a **site-to-site tunnel** for long-tail requests. Under typical content-consumption distributions (Zipf/Pareto), ~80 % of concurrent streams can be served from cache, leaving ~100 streams that must pull through the tunnel. At ~10 Mbps average per stream that is ~1 Gbps tunnel throughput — achievable only with a business-grade symmetric fibre connection at the origin site, not a typical residential plan.

---

## 2. Silo Distributed Architecture (Relevant to This Scenario)

```
┌──────────┐     ┌──────────────┐     ┌────────────────┐
│  Client  │────▶│  Proxy Node  │────▶│ Transcode Node │
│ (browser)│     │  (AWS/DO)    │     │  (AWS/DO K8s)  │
└──────────┘     └──────────────┘     └───────┬────────┘
                                              │ HTTP GET
                                     ┌────────▼────────┐
                                     │  Media Origin    │
                                     │  (Home ISP)      │
                                     └─────────────────┘
```

### How Silo reads media for `.strm` sources

1. Scanner indexes the `.strm` file → stores the remote URL in the catalog DB.
2. When playback starts, the transcode node resolves the `.strm` path to its HTTP URL (`resolveTranscodeInputPath`).
3. ffmpeg opens the remote URL as an input and produces HLS segments.
4. HLS segments are served to the client via the proxy node.

**Key insight:** Every active transcode session opens a long-lived HTTP connection to the media origin and reads the full file (for remux/copy) or faster-than-real-time (for full video transcode). The transcode node is the only component that touches the media origin.

---

## 3. Pressure Points & Bottleneck Analysis

### 3.1 The Home ISP Upload Link (THE Critical Bottleneck)

This is the single constraint that governs everything else.

| Concurrent Streams | Avg Source Bitrate | Total Egress from Origin |
| ------------------ | ------------------ | ------------------------ |
| 100                | 5 Mbps             | 500 Mbps                 |
| 100                | 15 Mbps            | 1.5 Gbps                 |
| 500                | 5 Mbps             | 2.5 Gbps                 |
| 500                | 15 Mbps            | 7.5 Gbps                 |

**Realistic residential upload:** 20–100 Mbps (most plans). Even a "gigabit" residential plan is typically 1000/50 (down/up).

**What this means:** Without caching, 500 concurrent streams would need **25–150× more upload bandwidth** than a typical residential connection provides. This gap cannot be closed by adding more cloud compute.

### 3.2 Concurrent HTTP Connections to Media Origin

Each transcode session holds an open HTTP connection to the origin. At 500 streams:

- 500 TCP connections to the origin server
- 500× HTTP GET requests for large binary streams
- Origin server must handle this connection count and the associated disk IO

Most consumer NAS devices or simple HTTP servers will buckle under 500 concurrent large-file reads. The origin server itself may need to be scaled (multiple mirrors, load-balanced).

### 3.3 Tunnel Bandwidth (Home → AWS)

A site-to-site VPN (WireGuard, IPsec) carries all uncached traffic. Tunnel overhead is ~4 % for WireGuard. If you need 1 Gbps of media throughput through the tunnel, you need a symmetric connection at the home end capable of sustaining that.

### 3.4 Transcode Compute (AWS/DO Side — Scalable)

CPU-based video encoding (no GPU in most cloud K8s):

- **Remux (video copy + audio transcode):** ~0.1–0.3 CPU cores per stream
- **1080p h264 → h264 transcode:** ~2–4 CPU cores per stream
- **4K transcode:** ~8–16 CPU cores per stream

At 500 concurrent streams, mostly remux/copy:

- **Best case (remux):** 500 × 0.2 = ~100 CPU cores
- **Worst case (full transcode, 1080p):** 500 × 3 = ~1500 CPU cores

Kubernetes can scale transcode pods horizontally. Each pod handles one session (one ffmpeg process). With K8s Node Autoscaling and HPA, this is manageable — but expensive.

### 3.5 HLS Segment Delivery (Proxy Nodes)

Proxy nodes serve HLS segments to clients. Each segment is small (~200 KB–1 MB for 2-second segments). At 500 concurrent streams:

- ~250 segment requests/second (500 streams, 2s segments, requesting every 2s)
- ~125 MB/s egress from proxy nodes
- Can be fronted by CloudFront or a CDN for global delivery

This is the least concerning bottleneck — standard CDN/web-serving patterns handle this easily.

### 3.6 Database (PostgreSQL)

Silo stores durable state in PostgreSQL: user accounts, catalog metadata, playback sessions, watch progress.

- 1000 users, occasional writes (playback progress every few seconds)
- ~500 active session rows
- Read-heavy catalog queries (browse, search)

A managed PostgreSQL instance (AWS RDS db.r6g.xlarge or equivalent) handles this comfortably. Connection pooling (PgBouncer) is recommended for 500+ concurrent backend connections.

### 3.7 Redis

Silo uses Redis for session coordination, recipe cards (transcode reconstruction tokens), and cache-style data. It is not the source of truth for durable state. A managed Redis cluster (AWS ElastiCache) with 2–4 GB memory is sufficient.

---

## 4. Network Topology: Tunnelling Local ISP to AWS

### 4.1 Recommended Approach: WireGuard Site-to-Site

```
┌──────────────────────┐         WireGuard Tunnel        ┌─────────────────────┐
│  Home Network         │◀═══════════════════════════════▶│  AWS VPC             │
│                       │                                │                      │
│  ┌─────────────────┐ │                                │  ┌────────────────┐  │
│  │ Media Origin    │ │                                │  │ Origin Proxy   │  │
│  │ (HTTP server)   │ │                                │  │ (nginx/cache)  │  │
│  │ 192.168.1.100   │ │                                │  │ 10.0.1.10      │  │
│  └─────────────────┘ │                                │  └───────┬────────┘  │
│                       │                                │          │           │
│  ┌─────────────────┐ │                                │  ┌───────▼────────┐  │
│  │ WireGuard Peer  │ │                                │  │ Transcode     │  │
│  │ (small VM/RPi)  │ │                                │  │ Nodes (K8s)   │  │
│  └─────────────────┘ │                                │  └───────────────┘  │
└──────────────────────┘                                └─────────────────────┘
```

**Home-side requirements:**

- A small always-on machine (Raspberry Pi 5, Intel NUC, or old PC) running WireGuard
- Or a router that supports WireGuard natively (MikroTik, OPNsense, OpenWrt)
- Static public IP or Dynamic DNS
- Port forward UDP 51820 (or custom) to the WireGuard peer

**AWS-side requirements:**

- VPC with public + private subnets
- EC2 instance or container running WireGuard as the cloud-side peer
- Route table entry pointing the media origin's internal IP (e.g., `192.168.1.0/24`) through the WireGuard interface
- Security group allowing UDP 51820 from the home IP

**Alternative:** Tailscale (simpler setup, built on WireGuard, but adds a dependency). Cloudflare Tunnel (only proxies HTTP, good as a fallback).

### 4.2 Origin Proxy Pattern

Instead of having transcode nodes hit the home server directly over the tunnel, deploy an **origin proxy** in AWS:

```
Transcode Node → Origin Proxy (AWS) → WireGuard Tunnel → Media Origin (Home)
                    │
                    ├── Cache hit: serve from local disk/S3
                    └── Cache miss: pull through tunnel, cache result
```

The origin proxy:

- Terminates the WireGuard connection
- Caches frequently-requested byte ranges
- Handles connection pooling/reuse to the home server
- Provides a single point to monitor tunnel throughput

---

## 5. Caching Strategy (The Key to Feasibility)

Without caching, this architecture is impossible on a residential connection. With caching, it becomes practical.

### 5.1 Content Popularity Distribution

Media consumption follows a power-law (Zipf) distribution:

- **Top 10 % of titles** → ~70–80 % of all watch time
- **Top 20 % of titles** → ~85–90 % of all watch time
- **Long tail (remaining 80 %)** → ~10–15 % of watch time

At 100 TB total library, assuming ~50,000 titles:

- **Hot set (top 10 % = ~5,000 titles):** ~10 TB
- **Warm set (next 10 %):** ~10 TB
- **Cold set (remaining 80 %):** ~80 TB

### 5.2 Tiered Caching Architecture

```
┌──────────────────────────────────────────────────────────────┐
│                     REQUEST FLOW                              │
│                                                               │
│  Transcode Node                                               │
│      │                                                        │
│      ▼                                                        │
│  ┌─────────────────┐    miss    ┌─────────────────┐          │
│  │  L1: Node-local │──────────▶│  L2: Shared      │          │
│  │  disk cache     │            │  S3/EFS cache    │          │
│  │  (500 GB NVMe)  │◀──────────│  (hot 10 TB)     │          │
│  └─────────────────┘   fill     └────────┬────────┘          │
│                                          │ miss               │
│                                          ▼                    │
│                                 ┌─────────────────┐          │
│                                 │  L3: Origin      │          │
│                                 │  Proxy (nginx)   │          │
│                                 │  → WireGuard     │          │
│                                 │  → Home Server   │          │
│                                 └─────────────────┘          │
└──────────────────────────────────────────────────────────────┘
```

**L1 — Node-local cache (NVMe SSD):**

- Each transcode node has a local disk cache
- ffmpeg output segments land here anyway (transcode output directory)
- Could also cache source byte ranges for frequently accessed files
- ~500 GB per node, LRU eviction
- Hit rate: ~60 % for popular content under active watch

**L2 — Shared warm cache (S3 Standard or EFS):**

- Stores the most-accessed files in their entirety
- Populated by a background job that analyses watch patterns
- Or populated lazily: first request pulls from origin, persists to S3, subsequent requests hit S3
- ~10 TB storage
- S3 cost: ~$230/month for storage, negligible for retrieval within AWS

**L3 — Origin (home server via tunnel):**

- Handles cache misses (long-tail content, new releases not yet cached)
- ~15 % of requests under Zipf distribution
- At 500 concurrent, ~75 streams pull through tunnel
- At 10 Mbps average: ~750 Mbps tunnel throughput needed

### 5.3 Byte-Range Caching for Partial Reads

ffmpeg reads media files sequentially but can seek. For remux sessions, ffmpeg reads the entire file at roughly real-time speed. The origin proxy can cache byte ranges using standard HTTP `Range` request semantics:

- nginx `proxy_cache` with `slice` module enabled
- First viewer of a file warms the cache progressively
- Second viewer hitting the same file benefits from cached ranges
- Cache key includes URL + byte range

This means even for the same file, different viewers at different playback positions can share cached data.

### 5.4 Cache Warming Strategies

| Strategy                 | How                                                               | Best For                              |
| ------------------------ | ----------------------------------------------------------------- | ------------------------------------- |
| **Lazy (on-demand)**     | First viewer pulls from origin; subsequent viewers hit cache      | Simple, no pre-work                   |
| **Watchlist-driven**     | When user adds to watchlist, pre-fetch first 5 % of file to cache | Reduces cold-start latency            |
| **Popularity-driven**    | Background job promotes most-watched titles to L2 cache daily     | Maximises cache hit rate              |
| **New-release pre-warm** | When new content is indexed, pre-fetch to cache during off-peak   | Eliminates launch-day thundering herd |

### 5.5 Effective Tunnel Throughput After Caching

| Scenario                                   | Concurrent Streams | Est. Cache Hit Rate | Streams Through Tunnel | Tunnel BW Needed |
| ------------------------------------------ | ------------------ | ------------------- | ---------------------- | ---------------- |
| Pessimistic (uniform distribution)         | 500                | 0 %                 | 500                    | 2.5–7.5 Gbps     |
| Realistic (Zipf, lazy cache)               | 500                | 60 %                | 200                    | 1–3 Gbps         |
| Optimistic (Zipf, warm cache, pre-warming) | 500                | 85 %                | 75                     | 375–1125 Mbps    |
| Target (business fibre + warm cache)       | 500                | 85 %                | 75                     | ~750 Mbps        |

**Bottom line:** With a 1 Gbps symmetric business fibre connection at the origin site and an 85 % cache hit rate, the tunnel can sustain 500 concurrent streams.

---

## 6. Kubernetes Deployment Sizing (AWS)

### 6.1 Node Pools

| Pool         | Instance Type                           | Count         | Purpose                                  |
| ------------ | --------------------------------------- | ------------- | ---------------------------------------- |
| API          | c7i.xlarge (4 vCPU, 8 GB)               | 3             | Silo API servers (stateless, behind ALB) |
| Proxy        | c7i.large (2 vCPU, 4 GB)                | 3–5           | HLS segment proxying to clients          |
| Transcode    | c7i.4xlarge (16 vCPU, 32 GB)            | 8–15          | ffmpeg transcode workers                 |
| Origin Proxy | c7i.xlarge (4 vCPU, 8 GB) + 500 GB NVMe | 1–2           | WireGuard termination + nginx cache      |
| DB           | db.r6g.xlarge (4 vCPU, 32 GB)           | 1 (+ replica) | Managed PostgreSQL (RDS)                 |
| Redis        | cache.r6g.large (2 vCPU, 13 GB)         | 1 (cluster)   | Managed Redis (ElastiCache)              |

### 6.2 Transcode Node Sizing Detail

Each transcode pod runs one ffmpeg process per session:

- **Remux session:** ~0.2 CPU, ~512 MB RAM
- **1080p transcode session:** ~3 CPU, ~1 GB RAM
- **4K transcode session:** ~10 CPU, ~2 GB RAM

A `c7i.4xlarge` (16 vCPU) can handle:

- ~60–70 concurrent remux sessions, OR
- ~4–5 concurrent 1080p transcode sessions, OR
- ~1–2 concurrent 4K transcode sessions

With 500 concurrent streams (mostly remux due to h264→h264 copy path):

- **8 nodes × 16 vCPU** ≈ 128 vCPU total → ~500 remux sessions (at ~0.2 CPU each)

Add headroom for transcode sessions: **12–15 transcode nodes**.

### 6.3 Autoscaling

- **API/Proxy nodes:** HPA on CPU (target 70 %), min 2, max 10
- **Transcode nodes:** Custom metrics on active session count, or KEDA with Redis queue length
- **Cluster Autoscaler:** Scale EC2 instances based on pending pods

### 6.4 Storage

| What               | Where                                      | Size                     |
| ------------------ | ------------------------------------------ | ------------------------ |
| Transcode segments | Node-local ephemeral (instance store NVMe) | ~100 GB per node         |
| L2 media cache     | S3 Standard                                | ~10 TB                   |
| Docker images      | ECR                                        | ~2 GB                    |
| DB backups         | S3                                         | ~50 GB (daily snapshots) |

---

## 7. Cost Estimates (AWS us-east-1, Monthly)

### 7.1 Compute (On-Demand Pricing)

| Resource                      | Unit Price | Quantity | Monthly     |
| ----------------------------- | ---------- | -------- | ----------- |
| API nodes (c7i.xlarge)        | $0.178/hr  | 3        | ~$392       |
| Proxy nodes (c7i.large)       | $0.089/hr  | 4        | ~$260       |
| Transcode nodes (c7i.4xlarge) | $0.712/hr  | 12       | ~$6,240     |
| Origin proxy (c7i.xlarge)     | $0.178/hr  | 2        | ~$260       |
| **Compute subtotal**          |            |          | **~$7,152** |

_Note: Reserved Instances (1-year) reduce compute by ~30–40 %. Spot Instances for transcode nodes (interruptible workloads) can reduce by ~60–70 %._

### 7.2 Managed Services

| Service                                      | Monthly           |
| -------------------------------------------- | ----------------- |
| RDS PostgreSQL (db.r6g.xlarge, Multi-AZ)     | ~$420             |
| ElastiCache Redis (cache.r6g.large, cluster) | ~$190             |
| S3 (10 TB standard, minimal requests)        | ~$250             |
| EKS control plane                            | ~$73              |
| ALB + NAT Gateway + Data Transfer            | ~$400–800         |
| **Services subtotal**                        | **~$1,333–1,733** |

### 7.3 Grand Total

| Scenario                             | Monthly           |
| ------------------------------------ | ----------------- |
| On-Demand, conservative              | **~$8,500–9,000** |
| 1-year Reserved + Spot for transcode | **~$4,500–5,500** |

### 7.4 Home-Side Costs

| Item                              | One-Time  | Monthly     |
| --------------------------------- | --------- | ----------- |
| Business fibre (1 Gbps symmetric) | —         | ~$200–500   |
| WireGuard endpoint (NUC/RPi)      | ~$200–500 | ~$5 (power) |
| Static IP (if needed)             | —         | ~$10–20     |

---

## 8. Request Flow: Where the Hits Land

For a typical stream, here is the full request path and where bandwidth is consumed:

```
Step 1: Client → CloudFront/ALB (AWS)
  GET /api/v1/playback/start
  ↓ (small JSON, negligible BW)

Step 2: API Server → PostgreSQL (AWS internal)
  Session creation, file lookup
  ↓ (no external BW)

Step 3: API Server → Transcode Node (AWS internal)
  POST /transcode/start with recipe
  ↓ (small JSON, negligible BW)

Step 4: Transcode Node → Origin Proxy → Home Server (TUNNEL)
  ffmpeg opens HTTP connection to media URL
  Reads entire file (remux at 1×) or faster (transcode at 3×)
  ↓ ↓ ↓ THIS IS THE BOTTLENECK ↓ ↓ ↓
  Egress from home: 5–20 Mbps per stream
  Total through tunnel: see §5.5

Step 5: Transcode Node → Local Disk (AWS, node-local)
  HLS segments written to NVMe
  ↓ (no external BW)

Step 6: Proxy Node → Client (Internet)
  HLS segments served to viewer
  ↓
  Egress from AWS: 5–20 Mbps per stream
  AWS data transfer: 500 streams × 10 Mbps = 5 Gbps egress
  (Use CloudFront to reduce data transfer cost)
```

**Key takeaway:** The home ISP upload link is the **only** choke point. Everything else (AWS compute, AWS internal networking, client egress) scales horizontally with money. The tunnel is limited by physics (your ISP plan).

---

## 9. Risk Assessment

| Risk                                       | Severity | Likelihood                | Mitigation                                                                                     |
| ------------------------------------------ | -------- | ------------------------- | ---------------------------------------------------------------------------------------------- |
| **Home ISP upload saturation**             | Critical | Certain (without caching) | L1+L2 caching, business fibre, cache warming                                                   |
| **Tunnel disconnection**                   | High     | Low                       | Redundant tunnel (WireGuard + Tailscale fallback), health checks, alerting                     |
| **Origin server overload (500 conns)**     | High     | Medium                    | Deploy multiple mirrors at origin, load-balance, or use a CDN-like origin setup                |
| **Home ISP dynamic IP change**             | Medium   | Medium                    | Dynamic DNS (ddclient), automated WireGuard reconfiguration                                    |
| **Home power/internet outage**             | Critical | Low                       | Accept as downtime; or run a small cache-warming instance at a colo/datacentre near the origin |
| **AWS cost overrun**                       | Medium   | Medium                    | Budget alerts, Spot Instances for transcode, Reserved Instances for baseline                   |
| **Copyright/DMCA exposure**                | Critical | Unknown                   | Consult legal counsel; hosting cached copies of media you don't own may create liability       |
| **Cold-start latency for uncached titles** | Medium   | Certain                   | Accept 10–30 s startup for long-tail content; communicate to users                             |

---

## 10. Alternative Architectures Considered

### 10.1 Colocation Near Origin

Instead of AWS, place servers in a datacentre that peers directly with the local ISP. This eliminates the tunnel bottleneck — the media origin is on the same network as the transcode servers.

**Pros:** No tunnel needed; full bandwidth to origin.
**Cons:** Less elastic than cloud; hardware procurement; more ops overhead.

### 10.2 Hybrid: Transcode at Edge (Home), Serve from Cloud

Run a few transcode nodes at the home site (where media is local and fast to read), push HLS segments to S3, and let cloud proxy nodes serve them globally.

```
Home Server (transcode) → S3 (segments) → CloudFront → Clients
```

**Pros:** Home upload is now segments (2s chunks), not full files. Segments can be uploaded asynchronously with buffering. Cloud handles all client egress.
**Cons:** Home still needs upload bandwidth (500 streams × 5 Mbps = 2.5 Gbps upload for segments alone). You're running ffmpeg at home. More complex.

### 10.3 Peer-to-Peer Segment Distribution

Clients share HLS segments among themselves (WebRTC data channel). The origin only needs to seed the first few viewers.

**Pros:** Dramatically reduces origin bandwidth.
**Cons:** Complex client-side logic; not supported by Silo today; latency-sensitive; NAT traversal issues.

### 10.4 Clone to Cloud Over Time

Start with the tunnel + caching approach. Over weeks/months, as content is watched, it naturally accumulates in the cloud cache. Eventually the hot set is fully cloud-resident and the tunnel is only needed for new/long-tail content. This is essentially **progressive migration without a bulk copy.**

---

## 11. Recommendations

### Immediate (Week 1–2)

1. **Measure your home ISP upload bandwidth** — run iperf3 to an AWS instance to get real numbers, not plan-advertised speeds.
2. **Deploy a WireGuard tunnel** between a small home machine and an AWS EC2 instance. Verify throughput and latency.
3. **Test a single transcode node** reading from the home HTTP server through the tunnel. Measure startup latency and stream stability.
4. **Profile the origin server** — how many concurrent HTTP connections can it handle? What is its disk IO capacity?

### Short-Term (Month 1–2)

5. **Deploy the origin proxy** (nginx with `proxy_cache` and `slice` module) in AWS. Point transcode nodes at it.
6. **Implement L1 node-local caching** — use the transcode node's ephemeral disk to cache source byte ranges.
7. **Set up S3 as L2 cache** — background job to promote hot content.
8. **Deploy the K8s cluster** with 3 API, 3 proxy, and 5 transcode nodes to start.
9. **Load-test with 50 concurrent streams** before scaling up.

### Medium-Term (Month 3–6)

10. **Upgrade home connection** to business-grade symmetric fibre (1 Gbps or better). This is the single highest-ROI investment.
11. **Implement cache warming** driven by watchlist additions and popularity metrics.
12. **Add CloudFront** in front of proxy nodes for global client delivery.
13. **Set up monitoring** for tunnel throughput, cache hit rates, origin connection count, and per-stream latency.

### Long-Term (Month 6+)

14. **Evaluate colocation** — if the cloud bill exceeds $8k/month, a colo cage near the ISP's peering point may be cheaper.
15. **Progressive cloud migration** — as the hot set naturally accumulates in S3, the tunnel becomes a cold-storage fallback rather than the primary data path.

---

## 12. Conclusion

This architecture is **feasible but constrained** by the home ISP upload link. The key numbers:

| Metric                             | Without Caching | With Caching (Realistic) |
| ---------------------------------- | --------------- | ------------------------ |
| Tunnel throughput needed           | 2.5–7.5 Gbps    | 375–1125 Mbps            |
| Cloud compute cost/month           | ~$7,000         | ~$4,500–7,000            |
| Feasible on residential internet?  | ❌ No           | ⚠️ Borderline            |
| Feasible on 1 Gbps business fibre? | ❌ No           | ✅ Yes                   |

**The single most important decision is the home-side internet connection.** Everything else — K8s, caching, load balancing — is standard cloud engineering that works. But if your upload pipe is 50 Mbps, no architecture can serve 500 concurrent streams from a remote origin.

Start by measuring your real upload throughput, then build the caching layer before scaling out transcode nodes. The tunnel should carry cache misses only, not every stream.
