# Silo Production Feasibility — On-Premises / Colocation

## 50 TB Owned Library · 1,000 Users · 500 Concurrent Streams · Tier-3 Data Centre, Bangladesh

**Date:** 2026-07-31
**Companion documents:** `production-feasibility-cloud.md`, `production-deployment-recommendation.md`
**Scope:** Buying and operating owned hardware in a Bangladeshi Tier-3 colocation facility, from blank machines to a running commercial streaming service.

> **Pricing disclaimer.** Hardware prices are July 2026 street estimates for enterprise components. **Bangladeshi colocation, bandwidth, and peering pricing is not publicly published — every figure in §8 and §9 marked (est.) must be replaced with a written quote before you commit.** The models, ratios, and sizing logic are the durable output of this document; the currency amounts are a starting point for negotiation. Exchange rate assumed: **USD 1 ≈ BDT 122**.

---

## 1. Executive Summary

**Verdict: recommended. On-premises in a Dhaka Tier-3 facility is both the cheapest sustainable option and — decisively — the only option that gives Bangladeshi users BDIX-quality delivery.**

| Metric | Value |
| --- | --- |
| Initial capital outlay (hardware + setup) | **~$250,000** (≈ BDT 3.05 crore) |
| Monthly operating cost | **~$9,850** (≈ BDT 12.0 lakh) |
| Fully-loaded monthly cost (OPEX + CAPEX amortized over 36 mo) | **~$16,800** |
| 3-year TCO, net of residual hardware value | **~$560,000** |
| vs. AWS Mumbai (3-yr committed) | **~$813,000** — on-prem saves ~31 % |
| vs. AWS Mumbai (on-demand) | **~$1,213,000** — on-prem saves ~54 % |
| Cost per subscriber/month @ 1,000 users | **$16.80** |
| Cost per subscriber/month @ 5,000 users | **$3.36** |
| **Marginal cost per additional concurrent stream** | **~$8/month** (~77 % of it bandwidth) |
| Latency from Dhaka | **5–20 ms, over BDIX** |

Three things make this the right answer:

1. **Storage economics are not close.** The full storage tier here — two nodes, 24 drives, NVMe cache, chassis and all — costs **~$150 per usable TB, once**, against **~$1,980 per usable TB over three years** for self-managed ZFS-on-EBS in AWS (and far worse on EFS). At the marginal level it is starker still: adding capacity costs ~$22 per usable TB in drives. The storage hardware pays for itself in roughly four months and then keeps working for five years.

2. **Bandwidth economics are not close either.** Delivering ~235 TB/month costs ~$15,700 on AWS Mumbai. In Bangladesh, delivering that traffic domestically over BDIX peering and local transit is estimated at **$1,500–5,500/month** — and unlike cloud egress, the price *falls* per Mbps as you commit more.

3. **BDIX is a product feature, not a line item.** Bangladeshi consumer ISP packages routinely deliver far higher throughput to BDIX-peered domestic content than to international destinations. A BD-only streaming service hosted abroad is served to its own users as international traffic. In-country hosting is the difference between competing with Netflix on delivery quality and competing with your users' ISP shaping.

**The honest counter-argument** is that rented bare metal in Singapore costs less in pure TCO (~$252,000–324,000 over three years, no capital outlay). That option is examined in the cloud document, §6.3. It loses on BDIX, and for a Bangladesh-only consumer service that is the deciding factor — but if capital is genuinely unavailable, it is a legitimate fallback and is discussed in the recommendation document.

---

## 2. Architectural Constraints That Shape the Build

These are derived from the Silo codebase. The cloud companion document (§2) covers each in full detail with code references; this section summarizes the ones that determine physical topology.

| # | Constraint | Effect on the build |
| --- | --- | --- |
| 1 | Media is addressed by **absolute POSIX path** baked into the stream token; there is no object-storage path for media | 50 TB must be mounted at an identical path on **every** API, proxy, and transcode node → shared filesystem is mandatory (§6) |
| 2 | Clients connect **directly to proxy nodes** by public URL; Silo's own planner load-balances streams | Every proxy node needs a public IP, DNS name, and TLS cert. **No load balancer in front of the proxy tier** — it would bypass Silo's capacity admission |
| 3 | API session state (`SessionManager`, `RealtimeHub`) is a **per-process in-memory map** | API tier needs **sticky sessions** at the load balancer |
| 4 | Per-user stream limits are counted **per API process** | Multi-API deployment leaks stream limits — pre-launch fix required (§12.3) |
| 5 | API servers will transcode locally by default | Set `playback.local_transcode_fallback = false`; use dedicated transcode nodes only |
| 6 | **No source-media caching layer** | Shared storage must sustain full aggregate read bandwidth (~625 MB/s, design for 1.5–2 GB/s) |
| 7 | HLS segments accumulate for the whole session (`-hls_list_size 0`, no `delete_segments`) | 1–2 TB NVMe scratch **per transcode node** |
| 8 | HW accel preference **nvenc > qsv > vaapi**; subtitle burn-in forces a GPU→CPU→GPU round trip | GPU transcode is essential; burn-in-heavy content needs extra headroom |
| 9 | Nodes are registered as **static URL rows in PostgreSQL**, health-checked every 30 s | **Do not run nodes as Kubernetes pods** — pod churn breaks registration and a Service URL destroys per-node capacity admission (§7.1) |
| 10 | Redis is a **hard dependency** for proxy and transcode modes | Redis is on the streaming critical path; needs HA, not just capacity |
| 11 | Silo self-tunes PostgreSQL via `ALTER SYSTEM` | Self-managed PostgreSQL works *better* than managed — this feature is usable on-prem and unusable on RDS |

Constraint 11 is worth noting as a genuine on-prem advantage: Silo was built assuming it controls its own database.

---

## 3. Workload Model

Identical to the cloud document (§3) so the two are directly comparable. Summarized here.

**Stream mix — Scenario B (planning target):**

| Path | Share | Count @ 500 concurrent |
| --- | --- | --- |
| Direct play | 50 % | 250 |
| Remux (video copy) | 25 % | 125 |
| Full video transcode | 25 % | 125 |

**Bitrate:** planning average **5 Mbps** delivered → **2.5 Gbps peak egress**. Silo's ladder is 480p/1.5 · 720p/2 · 1080p/6 · 2160p/20 Mbps.

> **Prerequisite:** direct play delivers the *source* bitrate. Direct-playing production masters at 15–25 Mbps triples to quintuples every bandwidth number here. **Producing a delivery-grade encode ladder from your masters is a precondition of this plan, not an optimization.**

**Monthly egress:** at a 3.5 peak:average ratio → 143 average concurrent → **~235 TB/month**.

**Per-stream compute** (modern server core):

| Path | CPU | GPU |
| --- | --- | --- |
| Direct play | 0.02 core | — |
| Progressive remux | 0.15 core | — |
| HLS remux | 0.20 core | — |
| 1080p transcode, CPU (x264 veryfast) | 2.0 cores | — |
| 1080p transcode, NVENC/QSV | 0.5 core | 1 session |
| + subtitle burn-in | +0.8–1.5 cores | — |

**GPU density (1080p H.264 realtime) (est.):** NVIDIA L4 28–35 · Intel Flex 140 30–36 · NVIDIA A2 18–22 · Intel Arc A380 15–20.

**Storage I/O:** ~625 MB/s sustained aggregate read at 500 concurrent (source bitrate ~10 Mbps average); **design for 1.5–2 GB/s**.

---

## 4. Physical Topology

```
                        Internet / BDIX / IIG
                                 │
                  ┌──────────────┴──────────────┐
                  │   Edge firewall pair (HA)   │  ← BD-only IP allowlist
                  │   BGP, /24 announcement     │
                  └──────────────┬──────────────┘
                                 │
                  ┌──────────────┴──────────────┐
                  │  ToR switch pair (25/100G)  │  MLAG
                  └──┬────┬────┬────┬────┬──────┘
                     │    │    │    │    │
     ┌───────────────┘    │    │    │    └──────────────────┐
     │                    │    │    │                       │
┌────▼─────┐   ┌──────────▼┐ ┌─▼────────┐  ┌───────────┐ ┌─▼────────┐
│ lb-01/02 │   │api-01..03 │ │proxy-    │  │ tc-01..03 │ │ db-01/02 │
│ HAProxy  │   │--mode=api │ │01..04    │  │--mode=    │ │ Postgres │
│ keepalive│   │           │ │PUBLIC IP │  │ transcode │ │ Patroni  │
└────┬─────┘   └─────┬─────┘ └────┬─────┘  │ 2× L4 GPU │ └────┬─────┘
     │               │            │        └─────┬─────┘      │
     └───────────────┴────────────┴──────────────┴────────────┘
                                 │
                  ┌──────────────┴──────────────┐
                  │  Storage: nas-01 / nas-02   │  ZFS + NFS over 25G
                  │  50 TB usable, 120 TB raw   │  identical mount path
                  └─────────────────────────────┘
                                 │
                  ┌──────────────┴──────────────┐
                  │  redis-01/02 (Sentinel ×3)  │
                  │  minio-01 (artwork bucket)  │
                  └─────────────────────────────┘
```

**Note the asymmetry:** the load balancer pair serves **only** the API tier. Proxy nodes are addressed directly by clients on public IPs, because Silo's planner selects them (`internal/api/handlers/playback.go:1748`). This is unusual and must be reflected in the IP plan, the firewall rules, and the TLS certificate strategy.

---

## 5. Server Specifications and Counts

### 5.1 Recommended build ("Tier 1") — sized for 500 concurrent with N+1 redundancy

| Role | Qty | CPU | RAM | Storage | Network | GPU |
| --- | --- | --- | --- | --- | --- | --- |
| **Storage (NAS)** | 2 | AMD EPYC 9124 (16c/32t, 3.0 GHz) | 256 GB DDR5 ECC | 12× 20 TB SAS 7.2k + 2× 3.84 TB NVMe (L2ARC) + 2× 1.6 TB NVMe (special vdev, mirrored) | 2× 25 GbE | — |
| **Transcode** | 3 | AMD EPYC 9354P (32c/64t, 3.25 GHz) | 128 GB DDR5 ECC | 2× 1 TB NVMe (RAID1, HLS scratch) | 2× 25 GbE | 2× NVIDIA L4 |
| **Proxy** | 4 | AMD EPYC 9124 (16c/32t) | 64 GB DDR5 ECC | 2× 960 GB NVMe (RAID1) | 2× 25 GbE | — |
| **API** | 3 | AMD EPYC 9124 (16c/32t) | 64 GB DDR5 ECC | 2× 960 GB NVMe (RAID1) | 2× 25 GbE | — |
| **Database** | 2 | AMD EPYC 9354P (32c/64t) | 256 GB DDR5 ECC | 4× 3.84 TB NVMe (RAID10) | 2× 25 GbE | — |
| **Redis / utility** | 2 | AMD EPYC 8024P (8c) or Xeon E-class | 32 GB ECC | 2× 960 GB NVMe (RAID1) | 2× 10 GbE | — |
| **Load balancer** | 2 | 8c | 32 GB ECC | 2× 480 GB NVMe (RAID1) | 2× 25 GbE | — |

**Total: 18 servers, ~24 rack units.**

### 5.2 Sizing derivations

**Transcode nodes — 3 × 2× L4:**
- Requirement: 125 concurrent video transcodes (Scenario B)
- Capacity: 6 L4 GPUs × 28 sessions **(est.)** = 168 concurrent → 34 % headroom
- With one node failed: 4 GPUs × 28 = 112 — slightly under the 125 target, which is the correct place to accept degradation (ABR downshift, not outage)
- CPU check: 125 × 0.5 core NVENC overhead = 63 cores; 3 × 32c = 96 cores ✓ with room for burn-in round trips
- Set Silo `MaxJobs = 55` per node

**Why L4 and not consumer GPUs:** NVIDIA GeForce drivers historically cap concurrent NVENC sessions at 3–8. Datacenter/professional cards (L4, A2, A10, RTX A-series) have no cap. A consumer card is not a cost saving here — it is a hard capacity ceiling.

**Budget alternative:** 3 nodes × 2× **Intel Arc A380** (~$150/card, ~15–20 sessions each **(est.)**) gives ~90–120 concurrent transcodes for ~$900 total in GPUs instead of ~$15,000. Silo's QSV path is fully implemented (`internal/playback/transcode.go:597-615`). This is a genuine option worth piloting — the risk is driver maturity and lower density, not missing functionality. Intel **Flex 140** is the datacenter version if available in Bangladesh.

**Proxy nodes — 4 × 16c:**
- CPU: 125 remux × 0.15 + 250 direct × 0.02 = 24 cores, plus TLS and NIC interrupt handling
- Egress: peak 2.5 Gbps; set `MaxBandwidthKbps = 1,500,000` (1.5 Gbps) per node
- 4 nodes × 1.5 Gbps = 6 Gbps admission capacity; survives one failure at 4.5 Gbps
- Note: **progressive remux runs ffmpeg on the proxy node** (`internal/proxy/server.go:175`), which is why these need 16 cores rather than 4

**API nodes — 3 × 16c:** N+1 for rolling deploys plus one failure. Artwork resizing via libvips is the heaviest work; catalog queries and playback planning are light.

**Database — 2 × 32c / 256 GB:** Write load is ~100/s (progress reports) plus session sync, partitioned activity/policy logs, and read-heavy catalog browsing. Expected database size 50–200 GB for a 50 TB library at production-house file counts. This is heavily over-provisioned *for today* and correctly sized *for 10× growth* — and unlike cloud, over-provisioning costs nothing recurring.

**Redis — 2 nodes + 3 Sentinels:** Tiny memory footprint, but a Redis outage stops every proxy and transcode node (§2, constraint 10). Run Sentinel quorum across the 2 Redis nodes plus one API node.

### 5.3 Lean build ("Tier 0") — minimum viable, accepts single points of failure

If capital is constrained, or for a pilot phase:

| Role | Qty | Change from Tier 1 |
| --- | --- | --- |
| Storage | 1 + 1 cold spare | Single active NAS; manual failover; ZFS replication to the spare |
| Transcode | 2 | ~112 concurrent transcodes — meets Scenario B with no headroom |
| Proxy | 2 | 3 Gbps admission capacity; no failure headroom |
| API | 2 | N+1 only |
| Database | 1 + 1 replica | Streaming replica, manual promotion |
| Redis | co-located on API nodes | Accepts correlated failure |
| Load balancer | 2 (keep) | Cheapest insurance in the build |

**Tier 0 hardware: ~$120,000.** Suitable for launch at lower concurrency with a clear upgrade path — every Tier 1 addition is an incremental node purchase, not a re-architecture.

---

## 6. Storage Design — The Hard Part

Silo has no source-media cache (§2, constraint 6), so the storage tier must serve every byte of every direct-play stream for the whole session, plus every transcode's source read.

### 6.1 Capacity

| Layer | Configuration | Raw | Usable |
| --- | --- | --- | --- |
| Capacity vdevs | 12× 20 TB in 2× RAIDZ2 (6 drives each) | 240 TB | **160 TB** |
| L2ARC (read cache) | 2× 3.84 TB NVMe | 7.7 TB | 7.7 TB |
| Special vdev (metadata + small blocks) | 2× 1.6 TB NVMe, mirrored | 3.2 TB | 1.6 TB |
| ARC (RAM) | 256 GB, ~200 GB usable for ARC | — | 200 GB |

160 TB usable against a 50 TB library gives room to roughly triple the catalogue before adding a shelf. RAIDZ2 tolerates two simultaneous drive failures per vdev — appropriate for 20 TB drives, whose long rebuild times make RAIDZ1 unsafe.

### 6.2 Throughput — how this actually meets 625 MB/s

The mechanism is caching, not spindles. Media consumption follows a Zipf/power-law distribution: roughly 10 % of titles account for 70–80 % of watch time.

| Tier | Serves | Expected share of reads **(est.)** | Throughput |
| --- | --- | --- | --- |
| ARC (200 GB RAM) | Currently-hot files, metadata | 15–25 % | Effectively unlimited |
| L2ARC (7.7 TB NVMe) | Hot catalogue set | 45–60 % | 3–6 GB/s |
| RAIDZ2 HDD | Long tail, first read of anything | 20–35 % | ~800 MB/s–1.2 GB/s sequential across 12 spindles |

Under the modelled mix, HDDs need to supply roughly 130–220 MB/s of the 625 MB/s aggregate — well within a 12-spindle RAIDZ2's sequential capability, even accounting for the seek penalty of many concurrent readers.

**Tuning that matters:**
- Set `recordsize=1M` on the media dataset — large sequential reads, no small-block traffic
- Set `atime=off` — eliminates a write per read
- Set `primarycache=all`, `secondarycache=all` on the media dataset
- Set `compression=off` for the media dataset (already-compressed video wastes CPU) but `lz4` on metadata datasets
- Size L2ARC headers against ARC: 7.7 TB of L2ARC at 1 MB records consumes roughly 1–2 GB of ARC for headers — comfortable at 256 GB RAM, but it is why RAM is 256 GB and not 128 GB

### 6.3 Sharing the filesystem

**NFSv4.1 over 25 GbE**, exported read-only to API, proxy, and transcode nodes at an identical mount path.

- Read-only export for streaming nodes is both a safety and a correctness measure — nothing but the ingest path should write to the library
- `nconnect=8` on clients to parallelize across TCP connections
- Jumbo frames (MTU 9000) end-to-end on the storage VLAN
- Mount with `ro,hard,nconnect=8,rsize=1048576,wsize=1048576`

**Why not Ceph:** CephFS scales further and eliminates the active/standby failover, but at 50–160 TB in a single rack it adds substantial operational complexity (MON/MGR/MDS/OSD topology, PG tuning, rebalance storms) for benefit you will not use. **Revisit Ceph past ~300 TB or when you outgrow a single rack.** ZFS + NFS with a warm standby is the right complexity level for this scale.

### 6.4 Redundancy and failover

- `nas-01` active, `nas-02` warm standby
- Continuous `zfs send`/`recv` replication, 15-minute snapshots
- Failover: promote `nas-02`, move the storage VIP, remount clients. Target RTO ~10–15 minutes, manual and rehearsed
- **Media is not backed up in the traditional sense** — 50 TB of backup is another 50 TB of hardware. The replica *is* the backup. Keep production masters archived separately (LTO tape or cold cloud storage at ~$0.004/GB-month ≈ $200/month for 50 TB) — this is a genuinely good use of cloud
- **Database backup is different and must be real:** PgBackRest with WAL archiving to MinIO, plus a nightly offsite copy

### 6.5 Transcode scratch

Per §2 constraint 7, HLS segments accumulate for the whole session. Worst case per transcode node: 55 concurrent sessions × 5.4 GB (2-hour session at 6 Mbps) ≈ **300 GB**, plus up to 24 hours of orphaned directories before the sweep reclaims them.

**2× 1 TB NVMe in RAID1 per transcode node** is correct. The 120 GB used in the existing single-box Proxmox guide would fill and cause session failures at this scale.

---

## 7. Software and Platform Stack

The question "what cloud infra will we use on blank machines" has a specific answer for Silo, and part of that answer is *less orchestration than you would expect*.

### 7.1 Do not use Kubernetes for the streaming tiers

Silo registers proxy and transcode nodes as **rows in PostgreSQL with static URLs** (`internal/nodepool/repository.go`), health-checks them every 30 s at `{url}/api/v1/health`, and selects among them with a capacity-aware planner implementing least-connections, per-node `MaxJobs`, per-node `MaxBandwidthKbps`, and co-location groups (`internal/nodepool/planner.go`).

Kubernetes fights this on both ends:
- Pod churn assigns new IPs on every reschedule, breaking static URL registration
- Fronting a pool with a stable Service URL fixes registration but **collapses the whole pool into one endpoint**, discarding per-node capacity admission and co-location grouping — the exact features that make the pool work

**Run proxy and transcode nodes as pets with stable DNS names.** This is not a limitation to work around; it is the architecture working as designed, and it removes most of the operational complexity a Kubernetes deployment would add.

### 7.2 Recommended stack

| Layer | Choice | Rationale |
| --- | --- | --- |
| **Hypervisor** | Proxmox VE cluster (3+ nodes for quorum), or bare metal for storage/transcode | You already have Proxmox experience (`docs/architecture/proxmox-deployment-guide.md`). Run storage and transcode on bare metal — GPU passthrough and ZFS both prefer it |
| **OS** | Ubuntu 26.04 LTS | Matches existing tooling; good GPU driver support |
| **Container runtime** | Docker + Compose per node | Silo ships Compose files; Compose is the right granularity for pets |
| **Configuration management** | Ansible | Node roles map cleanly to playbooks; no control plane to operate |
| **API load balancing** | HAProxy ×2 + keepalived (VRRP) | **Must use sticky sessions** — `balance source` or cookie-based affinity |
| **PostgreSQL HA** | Patroni + etcd (3 nodes), or streaming replica + manual promotion for Tier 0 | Self-managed PostgreSQL lets Silo's `ALTER SYSTEM` auto-tuning work (unusable on RDS) |
| **Connection pooling** | PgBouncer on each API node | `planstore.SessionLockCapacity` pins connections for advisory locks; pooling is not optional at 3+ API servers |
| **Redis HA** | Redis + Sentinel (3-node quorum) | Streaming plane hard-depends on Redis |
| **Object storage** | MinIO (single node, or 4-node erasure set) | Silo's S3 client is backend-agnostic; artwork/branding/metadata buckets only |
| **Metrics** | Prometheus + Grafana | Silo exposes `/metrics` on a dedicated mux (`cmd/silo/main.go:2291`) |
| **Logs** | Loki + Promtail | Complements Silo's `opslog` database pipeline |
| **Tracing** (optional) | OpenTelemetry Collector → Tempo/Jaeger | Opt-in via `SILO_OTEL_ENABLED` (`docs/architecture/observability.md`) |
| **Alerting** | Alertmanager → PagerDuty/Opsgenie or Telegram | Telegram is pragmatic and widely used in BD ops teams |
| **TLS** | Let's Encrypt via DNS-01 (wildcard) | Each proxy node needs a public cert; DNS-01 avoids per-node HTTP challenges |
| **Backup** | PgBackRest (DB) + ZFS send/recv (media) + offsite cold cloud (masters) | See §6.4 |
| **CI/CD** | GitHub Actions → registry → Ansible rolling deploy | Rolling deploy must respect API stickiness — drain, don't cut |

### 7.3 Key Silo configuration for this topology

| Setting | Value | Why |
| --- | --- | --- |
| `playback.local_transcode_fallback` | `false` | Prevents multi-API split-brain (§2, constraint 5) |
| Proxy node `MaxBandwidthKbps` | `1500000` | 1.5 Gbps per node; leaves headroom for the coarse 60 s meter |
| Transcode node `MaxJobs` | `55` | 2 GPUs × ~28 sessions |
| Proxy/transcode node `Group` | Set per rack/LAN segment | Keeps transcoded bytes from crossing the LAN twice |
| `POSTGRES_TUNE` | `on` (default) | Works on self-managed PostgreSQL — a real on-prem advantage |
| `SILO_TRUSTED_PROXIES` | LB CIDRs | Correct client IP resolution behind HAProxy |
| Transcode dir | 1 TB+ NVMe mount | §6.5 |

### 7.4 Monitoring — what to actually alert on

Beyond standard host metrics:

| Signal | Source | Why it matters |
| --- | --- | --- |
| Per-node `egress_kbps` vs `MaxBandwidthKbps` | Silo `/api/v1/health` | Admission control is coarse (60 s window) — you need to see overshoot |
| Per-node `active_jobs` vs `MaxJobs` | Silo `/api/v1/health` | Detects pool exhaustion before users do |
| `play_method` distribution | `playback_sessions_sync` table | Validates the entire Scenario B sizing model (§12.6) |
| Node health flap rate | `nodepool` health checker logs | 30 s check interval means flaps are expensive |
| ZFS ARC/L2ARC hit ratio | `arcstat` | Directly predicts whether HDDs are about to become the bottleneck |
| Transcode scratch free space | Node exporter | Full scratch = session failures (§6.5) |
| Redis availability | Sentinel | Streaming plane hard dependency |
| GPU encoder utilization | DCGM exporter / `intel_gpu_top` | The real transcode capacity signal, not CPU |

---

## 8. Capital Expenditure

### 8.1 Hardware bill of materials — Tier 1

| Line | Qty | Unit **(est.)** | Total |
| --- | --- | --- | --- |
| **Storage node** — 2U 12-bay, EPYC 9124, 256 GB, HBA, dual 25G | 2 | $7,000 | $14,000 |
| 20 TB SAS 7.2k enterprise HDD | 24 | $300 | $7,200 |
| 3.84 TB NVMe (L2ARC) | 4 | $400 | $1,600 |
| 1.6 TB NVMe mixed-use (special vdev) | 4 | $250 | $1,000 |
| **Transcode node** — 2U, EPYC 9354P, 128 GB, 2× 1 TB NVMe, dual 25G | 3 | $8,500 | $25,500 |
| NVIDIA L4 24 GB | 6 | $2,500 | $15,000 |
| **Proxy node** — 1U, EPYC 9124, 64 GB, 2× 960 GB NVMe, dual 25G | 4 | $5,500 | $22,000 |
| **API node** — 1U, EPYC 9124, 64 GB, 2× 960 GB NVMe, dual 25G | 3 | $5,000 | $15,000 |
| **Database node** — 2U, EPYC 9354P, 256 GB, 4× 3.84 TB NVMe, dual 25G | 2 | $12,000 | $24,000 |
| **Redis/utility node** — 1U, 8c, 32 GB | 2 | $3,000 | $6,000 |
| **Load balancer** — 1U, 8c, 32 GB, dual 25G | 2 | $2,500 | $5,000 |
| **ToR switch** — 48× 25G + 8× 100G uplink | 2 | $8,000 | $16,000 |
| **OOB management switch** — 24× 1G | 1 | $500 | $500 |
| **Edge firewall / router** — 10G-capable, HA pair | 2 | $3,000 | $6,000 |
| **Optics, DACs, patch cabling** — ~50 × 25G DAC + fibre | — | — | $3,000 |
| **Rack, PDUs (metered, redundant), KVM, rails** | — | — | $6,000 |
| **Hardware subtotal** | | | **$167,800** |

### 8.2 Landed cost — Bangladesh import considerations

This is routinely underestimated and can add a quarter to the bill.

| Item | Basis | Amount **(est.)** |
| --- | --- | --- |
| Customs duty, VAT, AIT, and supplementary duty on IT hardware | HS-code dependent; server/networking gear typically lands 15–31 % all-in | ~20 % → $33,560 |
| International freight + insurance (heavy, ~500 kg) | — | ~$4,500 |
| Letter of credit charges, bank margin, clearing agent | ~2 % | ~$3,400 |
| **Landed uplift subtotal** | | **~$41,460** |

> **Verify current SRO/duty rates with a clearing agent before ordering.** Bangladeshi duty schedules on IT hardware change with budget cycles, and some categories receive exemptions. There may be meaningful savings in how the order is classified and split. Also consider whether a local reseller with existing import channels is cheaper all-in than direct import — often it is, despite a higher headline price.

### 8.3 Spares inventory

Running your own hardware means carrying spares. Colocation means a technician cannot walk to a supply closet.

| Item | Qty | Cost **(est.)** |
| --- | --- | --- |
| 20 TB SAS HDD | 3 | $900 |
| NVMe drives (assorted) | 3 | $1,000 |
| DDR5 ECC DIMMs | 4 | $1,200 |
| Redundant PSU | 3 | $900 |
| 25G optics/DACs | 8 | $600 |
| Full cold-spare 1U node (proxy/API class) | 1 | $5,000 |
| Fans, cables, misc | — | $500 |
| **Spares subtotal** | | **~$10,100** |

### 8.4 One-time setup costs

| Item | Cost **(est.)** |
| --- | --- |
| Rack installation, structured cabling, commissioning | $3,000 |
| Colocation setup / installation fee | $1,500 |
| APNIC resources: ASN + IPv4 /24 (via BD sponsor / broker) + IPv6 /32 | $3,500–5,000 |
| BDIX membership + port setup + transport to exchange | $2,000 |
| Initial 50 TB library ingest (physical transport, load, scan, metadata) | $2,000 |
| Deployment engineering: Ansible, monitoring, runbooks, HA drills (2 engineers × 6 weeks) | $8,000–15,000 |
| Load testing and burn-in (50 → 150 → 500 concurrent) | $2,000 |
| Security review / penetration test | $3,000 |
| **Setup subtotal** | **~$25,000–33,500** |

> IPv4 is scarce and increasingly expensive. A /24 on the transfer market runs $30–50 per address, so a /24 could be $8,000–13,000 to *buy*. Getting one via an upstream ISP assignment instead is far cheaper and is what most BD deployments do — but ties you to that provider. Budget $3,500–5,000 assuming a sponsored assignment; confirm early, as this can gate the whole launch.

### 8.5 Total capital outlay

| Component | Tier 1 (recommended) | Tier 0 (lean) |
| --- | --- | --- |
| Hardware | $167,800 | ~$96,000 |
| Landed uplift (~25 %) | $41,460 | $23,700 |
| Spares | $10,100 | $7,000 |
| One-time setup | $29,000 | $22,000 |
| **TOTAL** | **~$248,400** | **~$148,700** |
| | ≈ BDT 3.03 crore | ≈ BDT 1.81 crore |

**Plan on ~$250,000 for the recommended build.**

---

## 9. Operating Expenditure

> **Every figure in this section is an estimate requiring local quotes.** Bangladeshi colocation and bandwidth pricing is quoted per customer and not published. These are planning placeholders.

### 9.1 Power budget

| Role | Qty | Watts each **(est.)** | Total |
| --- | --- | --- | --- |
| Storage | 2 | 400 W | 800 W |
| Transcode (2× L4 @ 72 W each + EPYC) | 3 | 700 W | 2,100 W |
| Proxy | 4 | 250 W | 1,000 W |
| API | 3 | 200 W | 600 W |
| Database | 2 | 400 W | 800 W |
| Redis/utility | 2 | 150 W | 300 W |
| Load balancer | 2 | 150 W | 300 W |
| Switches, firewall, OOB | — | — | 400 W |
| **IT load** | | | **~6.3 kW** |
| **Provisioned (with headroom)** | | | **~8 kW** |

At ~8 kW × 730 h = 5,840 kWh/month. At BDT 12/kWh commercial that is ~BDT 70,000 (~$575) at cost; colocation facilities typically bill power at a 1.5–2.5× multiplier or bundle it into rack pricing.

**8 kW is a meaningful power density** — above what many Bangladeshi colocation facilities provision per rack as standard (often 3–5 kW). You may need a high-density rack, two racks with the load split, or a negotiated power upgrade. **Raise this early in colocation discussions**; it is a common source of unpleasant surprises and may be the deciding factor between facilities.

### 9.2 Monthly operating costs

| Item | Low | **Planning** | High | Notes |
| --- | --- | --- | --- | --- |
| Colocation — 1 rack, 8 kW, Tier-3 Dhaka | $1,200 | **$1,800** | $2,800 | Power density drives this (§9.1) |
| Bandwidth — 2.5 Gbps peak, BDIX + domestic transit + small IIG | $1,500 | **$3,000** | $5,500 | See §9.3 |
| Staff — 2.5 FTE (SRE + NOC + part-time DBA) | $2,000 | **$3,000** | $4,500 | BD salaries; 24/7 needs more |
| Hardware maintenance, warranty, spares replenishment | $900 | **$1,300** | $1,800 | ~6–8 % of hardware CAPEX/year |
| Offsite backup + master archive (cold cloud) | $250 | **$500** | $900 | ~50 TB cold storage + DB backups |
| Monitoring, tooling, TLS, misc licences | $150 | **$250** | $400 | |
| **Monthly total** | **$6,000** | **~$9,850** | **$15,900** | ≈ BDT 12.0 lakh at planning |

### 9.3 Bandwidth — the estimate that matters most

Bangladeshi bandwidth pricing is regulated and tiered, and the domestic/international split is the whole story.

**What is publicly known:** BTRC has regulated IIG (International Internet Gateway) wholesale tariffs for years. Published rates have historically sat in the BDT 300–400 per Mbps/month range for smaller commitments, with volume tiers reaching roughly BDT 285–300/Mbps for the largest buyers. Industry proposals in late 2024 sought reductions to around **BDT 215/Mbps/month for Dhaka**. Domestic and BDIX-exchanged traffic is priced entirely differently — typically as a flat port fee rather than per-Mbps — and is dramatically cheaper.

**Why this matters enormously for you:** your servers are in Bangladesh and your users are in Bangladesh. **The streaming traffic is domestic.** You do not need IIG capacity for it. You need:

| Component | Purpose | Estimated cost |
| --- | --- | --- |
| BDIX membership + 10G port + transport | Reaches ISPs peering at BDIX — the bulk of BD broadband | $400–900/month **(est.)** |
| Domestic IP transit (NTTN/upstream ISP) | Reaches BD networks not at BDIX, including mobile operators | $1,000–3,500/month **(est.)** |
| Small IIG commit (100–200 Mbps) | Your own outbound: updates, metadata APIs, backups, plugin registries | $200–500/month **(est.)** |
| **Total** | | **$1,600–4,900/month** |

**Compare: AWS Mumbai charges ~$15,700/month for the same 235 TB.** Even at the pessimistic end of the domestic estimate, in-country delivery is **3–10× cheaper**, and it is the *good* kind of traffic from the user's perspective.

**The catch — mobile operator peering.** Grameenphone, Robi, and Banglalink carry the majority of Bangladeshi internet users, and their BDIX peering is partial. A meaningful share of your traffic may need to reach them via paid domestic transit or direct peering arrangements. **Budget for direct peering negotiations with at least the largest mobile operator**, and treat the domestic transit line as the one most likely to exceed estimate. This is the single largest uncertainty in the OPEX model.

### 9.4 Staffing reality

$3,000/month buys roughly 2.5 FTE at Bangladeshi market rates (a mid-senior SRE at BDT 100,000–180,000/month, a NOC/sysadmin at BDT 50,000–80,000/month, plus fractional DBA time).

**That is business-hours coverage with on-call, not 24/7 staffed operations.** True 24/7 NOC coverage requires 4–5 people and roughly doubles this line. For a launch-phase service, business hours plus a rehearsed on-call rotation is the right trade — but be explicit that this is the choice being made, and that it implies a hardware failure at 3 a.m. is a next-morning fix unless it triggers automatic failover.

**This is the cost line where on-prem genuinely loses to cloud**, and it should not be minimized. Managed services replace people. Owning hardware does not.

---

## 10. Financial Analysis

### 10.1 Three-year total cost of ownership

| Component | Amount |
| --- | --- |
| Initial capital outlay | $248,400 |
| Operating cost, 36 months @ $9,850 | $354,600 |
| **Gross 3-year TCO** | **$603,000** |
| Less residual hardware value at year 3 (~25 % of hardware+landed) | −$43,000 |
| **Net 3-year TCO** | **~$560,000** |

### 10.2 Comparison

| Option | 3-year TCO | vs on-prem | Latency / routing |
| --- | --- | --- | --- |
| **On-prem, Dhaka Tier-3** | **~$560,000** | — | **5–20 ms, BDIX** |
| AWS Mumbai, 3-yr committed | ~$813,000 | +45 % | 40–55 ms, international |
| AWS Mumbai, on-demand | ~$1,213,000 | +117 % | 40–55 ms, international |
| Rented bare metal, Singapore | ~$252,000–324,000 | **−42 % to −54 %** | 60–80 ms, international |

**Read this table honestly.** Rented bare metal in Singapore is cheaper than owning hardware in Dhaka. On-prem wins against AWS decisively, but against rented bare metal it wins on *delivery quality* — BDIX peering, in-country latency, and freedom from your users' international-bandwidth constraints — not on cost. Whether that is worth $240,000–300,000 over three years is a product decision, and it is the central question of the recommendation document.

The supporting arguments for owning: the hardware retains value, you are not exposed to a provider's pricing changes or exit, capacity is yours to over-provision at zero recurring cost, and Bangladeshi regulatory direction favours in-country data.

### 10.3 Cost per subscriber

Fully-loaded monthly cost = OPEX ($9,850) + CAPEX amortized over 36 months ($248,400 ÷ 36 = $6,900) = **$16,750/month**.

| Subscribers | Implied peak concurrency ratio | Cost/subscriber/month |
| --- | --- | --- |
| 1,000 | 50 % | **$16.75** |
| 2,000 | 25 % | $8.38 |
| 2,500 | 20 % | $6.70 |
| 5,000 | 10 % | **$3.35** |
| 10,000 | 5 % | $1.68 |

**The critical insight: infrastructure is sized by peak concurrency, but revenue scales with subscribers.** A 500-concurrent build at 1,000 subscribers is capacity you are paying for and not using. The same hardware serves 5,000 subscribers at a realistic 10 % peak concurrency ratio — at one fifth the per-user cost, with no additional capital.

**The business implication is direct: the fastest way to fix your unit economics is to add subscribers, not to cut infrastructure.** At BDT 200–400/month typical BD streaming price points ($1.64–3.28), the break-even is somewhere between 5,000 and 10,000 subscribers on infrastructure cost alone — before content, marketing, payment processing, or support.

### 10.4 Marginal cost of growth

Cost to add **one concurrent stream of peak capacity**, sustained:

| Component | Derivation | $/concurrent stream/month |
| --- | --- | --- |
| Bandwidth | 5 Mbps at ~$1.23/Mbps/month domestic **(est.)** | $6.15 |
| Transcode | $13,500 node ÷ 36 mo ÷ 280 concurrent served (at 25 % transcode mix) | $1.34 |
| Proxy | $5,500 node ÷ 36 mo ÷ ~1,000 concurrent served | $0.15 |
| Power, rack | Incremental | $0.30 |
| Storage, DB, API | Zero until ~2,000 concurrent | $0.00 |
| **Total marginal** | | **~$7.94** |

**~77 % of marginal cost is bandwidth.** Everything else is nearly free at the margin until you cross a hardware threshold.

Marginal cost per additional **subscriber** = $7.94 × peak concurrency ratio *R*:

| *R* | Marginal $/subscriber/month |
| --- | --- |
| 50 % | $3.97 |
| 25 % | $1.99 |
| **12.5 % (realistic)** | **$0.99** |
| 5 % | $0.40 |

**At a realistic 12.5 % peak concurrency ratio, each additional subscriber costs about $1/month in infrastructure.** That is a workable unit economic against BD streaming price points — the challenge is covering the fixed base, not the marginal user.

**Compare AWS marginal cost: ~$25/concurrent stream/month** (cloud document, §7.3) — **3.1× higher**, almost entirely egress.

### 10.5 Capacity expansion thresholds

What breaks first as you grow, and what it costs to fix:

| Concurrent streams | First constraint | Remedy | Incremental cost |
| --- | --- | --- | --- |
| 500 | — (design point) | — | — |
| 750 | Proxy bandwidth admission | +1 proxy node; upgrade domestic transit | $5,500 + ~$1,200/mo |
| 1,000 | Transcode GPU capacity | +1 transcode node (2× L4) | $13,500 + ~$100/mo |
| 1,500 | Storage read throughput (ARC/L2ARC miss rate rises) | +L2ARC NVMe; consider all-flash hot tier | $8,000 |
| 2,000 | API tier + database write load | +1 API node; evaluate read replicas | $5,000 + $12,000 |
| 3,000 | Rack power and ToR port capacity | Second rack, second ToR pair | ~$25,000 + ~$1,800/mo |
| 5,000+ | Single-site risk becomes unacceptable | Second site, active/active | Roughly duplicates the build |

**Bandwidth is the recurring cost that scales linearly; hardware scales in steps.** Plan bandwidth commitments quarterly and hardware annually.

---

## 11. Data Centre Selection Criteria

Bangladeshi facilities operating at or near Tier-3 standards include Aamra Networks, Summit Communications, Link3 Technologies, Exabyte, BDCOM, and the national Bangladesh Data Center Company (BDCCL) facility at Kaliakair. **Get written quotes from at least three.**

Evaluate on, in priority order:

1. **Power density per rack.** You need 8 kW (§9.1). Many BD facilities provision 3–5 kW as standard. This is the most likely disqualifier and the first question to ask.
2. **BDIX connectivity.** Direct BDIX presence or short, cheap transport to the exchange. This is the entire economic thesis — verify it, do not assume it.
3. **Upstream diversity.** At least two independent domestic transit providers, ideally on physically diverse paths.
4. **Mobile operator peering.** Ask specifically what peering exists with GP, Robi, and Banglalink (§9.3).
5. **Genuine power redundancy.** N+1 UPS, redundant generators, tested transfer. Bangladeshi grid reliability makes this non-negotiable, and "Tier 3" is often claimed rather than certified. **Ask for the certification, or an audit report.**
6. **Cooling capacity at your density.** 8 kW in one rack needs real airflow management — hot/cold aisle containment, not just raised floor.
7. **Remote hands availability and SLA.** With business-hours staffing (§9.4), 24/7 remote hands is what covers the gap.
8. **Physical security and access process.** How quickly can your engineer get in at 2 a.m.?
9. **Cross-connect pricing.** Per-connection fees add up across 18 servers plus upstreams.
10. **Expansion path.** Can they give you a second rack adjacent, and at what notice?

**Also evaluate a second site early**, even if you do not build it at launch. Single-site is acceptable risk for a launch-phase service; it stops being acceptable somewhere around meaningful subscriber revenue.

---

## 12. Risks and Catches

### 12.1 Bandwidth cost is the largest estimate uncertainty

The $1,600–4,900/month range in §9.3 is wide because BD pricing is not public and mobile-operator reach is the wildcard. If domestic transit to mobile operators turns out to cost near IIG rates, the bandwidth line could reach $6,000–7,000/month and the on-prem advantage over rented bare metal narrows substantially (though the advantage over AWS survives easily).

**Mitigation:** get firm quotes before committing capital. This is the single number most worth nailing down first, and it can be established with phone calls in a week.

### 12.2 Power density may constrain facility choice

Covered in §9.1 and §11. If no acceptable facility can deliver 8 kW in one rack, the build splits across two racks — adding cross-connects, a second ToR pair, and roughly $1,800/month.

### 12.3 Per-user stream limits do not hold across API servers

`SessionManager.ActiveCount(userID)` counts only sessions in that process (`internal/playback/session.go:1089`). With three API servers, a user can potentially open three times their plan limit by forcing sessions onto different servers.

For a commercial service with per-plan screen limits, **this is a revenue leak and must be fixed before launch.** Sticky sessions reduce the exposure but do not close it. The correct fix is moving admission counting to Redis or PostgreSQL — a contained change, and Redis is already a hard dependency on every node.

### 12.4 Silo is described in its own repository as very early WIP

`AGENTS.md` states plainly: "This repository is a VERY EARLY WIP." The playback protocol is at v3, the restart-resilience design document lists several deferred issues (owner-identity claim for multi-front-end integrated transcode; unrevocable 24-hour node-path tokens), and the API contract is under active v1 scope lock.

**Plan for meaningful ongoing engineering, not just operations.** Budget engineering capacity permanently, and expect to be tracking upstream changes. A "deploy it and run it" assumption is not supported by the current maturity of the codebase.

Specifically, the **24-hour unrevocable stream token** (`internal/playback/recipecard.go:168`) means a banned or unsubscribed user's in-flight stream cannot be cut server-side — enforcement happens at next play, bounded by the token TTL. For a paid service, consider shortening `MaxTokenTTL` (a code change) and understand the trade-off against restart resilience.

### 12.5 Single-site risk

One data centre, one rack, one storage pair. A facility-level failure — fire, flood, extended power failure, civil disruption — takes the service fully offline. Bangladesh's exposure to flooding and political disruption makes this a live concern rather than a theoretical one.

**Mitigations, in increasing cost order:** rehearsed restore-from-backup to a rented cloud environment (cheap, hours of RTO); warm standby of the API/DB tier in a second facility or cloud (moderate); full active/active second site (roughly doubles the build). At launch scale, a rehearsed cloud restore is the right level.

### 12.6 The stream-mix assumption drives everything

Scenario C (50 % transcode) would require roughly double the transcode hardware (+3 nodes, ~$40,000 CAPEX). Scenario A (10 % transcode) would need only one transcode node, saving ~$27,000.

**Instrument `play_method` distribution from day one of any pilot** — it is recorded per session in `playback_sessions_sync`. Do not order transcode hardware until you have measured it against your real client mix and your real library.

### 12.7 Subtitle burn-in can wreck GPU density

Bitmap subtitle burn-in forces `hwdownload → CPU filter → hwupload` (`internal/playback/transcode.go:809-810`), consuming CPU and reducing per-GPU density. If your library carries PGS subtitles that clients cannot render natively, measure this specifically — it can be the difference between 28 and 15 sessions per GPU.

**Mitigation:** convert bitmap subtitles to text formats (SRT/ASS) during ingest wherever possible. This is an ingest-pipeline change with an outsized infrastructure payoff.

### 12.8 Admission control is coarse

Health checks every 30 s, a 60-second rolling egress meter, 60–90 s reservation bridging. A synchronized wave of session starts — a premiere, a push notification — can overshoot a node's `MaxBandwidthKbps` before the meter reacts.

**Mitigations:** set caps conservatively (§7.3), stagger release-time push notifications, and monitor overshoot (§7.4).

### 12.9 Import duty and lead time

Duty estimates (§8.2) must be confirmed with a clearing agent. Separately, **enterprise hardware lead times to Bangladesh can run 8–16 weeks** including customs clearance. Order early, and consider whether a local reseller with stock is worth a price premium for schedule certainty.

### 12.10 Content licensing and regulatory posture

Operating a commercial streaming service in Bangladesh may involve BTRC licensing, content classification requirements, and evolving data-localization expectations. **Get local legal advice before committing capital.** On the whole this points toward in-country hosting, reinforcing the recommendation — but confirm rather than assume.

---

## 13. Build Plan

### Phase 0 — Validate assumptions (weeks 1–4, minimal spend)

The goal is to replace estimates with facts before spending capital.

1. **Get written quotes** from three Dhaka Tier-3 facilities: rack, 8 kW power, BDIX transport, domestic transit, mobile-operator peering (§11)
2. **Confirm import duty** with a clearing agent for your specific BOM (§8.2)
3. **Measure the stream mix** — deploy Silo on existing hardware, load a representative slice of the library, test against your real target devices, and record `play_method` distribution (§12.6)
4. **Benchmark one transcode node** — validate the GPU density estimate on your actual content, including subtitle burn-in (§12.7)
5. **Confirm APNIC resource path** — ASN and IPv4 assignment via sponsor or upstream (§8.4)
6. **Legal review** of licensing and data-residency obligations (§12.10)

**Decision gate: do the quotes and measurements support the model?** If bandwidth quotes come back near IIG rates, or the transcode share measures at Scenario C, revisit before ordering.

### Phase 1 — Procure and stage (weeks 5–16)

7. Order hardware; expect 8–16 weeks including clearance (§12.9)
8. Sign colocation; complete BDIX membership and transit contracts
9. Build Ansible playbooks, monitoring stack, and runbooks against VMs while hardware ships
10. **Fix the global stream-limit enforcement gap** (§12.3) — this is pre-launch blocking work
11. Build the delivery encode ladder from masters (§3) — also pre-launch blocking

### Phase 2 — Install and commission (weeks 17–20)

12. Rack, cable, power on, firmware/BIOS baseline
13. Build the storage pool; tune ZFS (§6.2); validate 1.5 GB/s+ sustained read
14. Deploy PostgreSQL + Patroni, Redis + Sentinel, MinIO
15. Deploy API, proxy, transcode tiers; register nodes with correct `MaxJobs` / `MaxBandwidthKbps` / groups
16. Ingest the 50 TB library; run a full scan; verify metadata
17. Configure geo-blocking, TLS, firewall rules

### Phase 3 — Validate (weeks 21–24)

18. **Load test in stages: 50 → 150 → 300 → 500 concurrent.** Validate against the model at each step, not just at the end
19. **Failure drills, all rehearsed and documented:** storage failover, database failover, Redis failover, transcode node loss under load, proxy node loss under load
20. Validate BDIX routing from real BD ISP connections — test from GP, Robi, Banglalink, and at least two broadband ISPs
21. Tune `MaxBandwidthKbps` and `MaxJobs` against observed behaviour
22. Soft launch to a limited user cohort; measure everything

### Phase 4 — Launch and iterate

23. Public launch with monitoring and alerting fully in place
24. Weekly review of the play-method distribution, ARC hit rate, node saturation, and bandwidth consumption against model
25. Quarterly capacity review against the expansion thresholds in §10.5

---

## 14. Verdict

On-premises in a Dhaka Tier-3 facility is the recommended platform for this workload.

**It wins decisively against AWS** — ~$560,000 versus ~$813,000 over three years, with 3.1× better marginal economics and dramatically better delivery to Bangladeshi users.

**It loses on pure cost to rented bare metal in Singapore** (~$252,000–324,000), and that comparison deserves to be taken seriously rather than argued away. On-prem's case rests on BDIX: for a Bangladesh-only consumer streaming service, being inside the domestic peering fabric is a product capability, not an infrastructure preference. Bangladeshi ISP packages treat domestic and international traffic very differently, and hosting abroad means competing against your own users' bandwidth constraints.

**The unit economics work at scale, not at 1,000 subscribers.** At $16.75/subscriber/month against 1,000 users, this infrastructure is not economically justified. At 5,000 subscribers it is $3.35, and marginal subscribers cost about $1/month. **The plan should be to build for 500 concurrent and then fill it** — the capital is largely spent whether you have 1,000 users or 5,000.

**Three things must happen before launch, independent of hosting:**

1. Fix global per-user stream-limit enforcement (§12.3)
2. Build a delivery encode ladder from masters (§3)
3. Measure the real stream mix against real devices (§12.6)

And one thing should be understood clearly: Silo is early-stage software by its own documentation. This deployment needs a permanent engineering budget, not just an operations budget.

---

## Sources

- [New bandwidth price from July 1 — New Age Bangladesh](https://www.newagebd.net/article/76750/new-bandwidth-price-from-july-1)
- [Bulk bandwidth price set for ISPs — New Age Bangladesh](https://www.newagebd.net/article/146155/bulk-bandwidth-price-set-for-isps)
- [Internet gateways propose 40% tariff cut — The Business Standard](https://www.tbsnews.net/bangladesh/internet-gateways-propose-40-tariff-cut-how-it-may-benefit-broadband-users-986811)
- [Uniform tariff for broadband internet — The Daily Star](https://www.thedailystar.net/business/telecom/news/uniform-tariff-broadband-internet-2151246)
- [BDIX — Bangladesh Internet Exchange, Data Center Map](https://www.datacentermap.com/ixp/bdix/)
- [Exabyte — Bangladesh IIG & IP Transit Provider](https://exabytebd.net/)
- [Summit Communications — IIG](https://www.summitcommunications.net/iig)
- [Bangladesh Submarine Cables PLC — Wikipedia](https://en.wikipedia.org/wiki/Bangladesh_Submarine_Cables_PLC)
