# Silo Production Deployment — Recommendation

## Decision Memo: Cloud vs On-Premises for a 500-Concurrent Bangladesh Streaming Service

**Date:** 2026-07-31
**Supporting analyses:** `production-feasibility-cloud.md`, `production-feasibility-onprem.md`
**Decision required:** hosting platform for a commercial streaming service — 50 TB owned library, 1,000 initial subscribers, 500 concurrent streams, Bangladesh-only delivery.

---

## 1. Recommendation

**Build on owned hardware in a Dhaka Tier-3 colocation facility. Use cloud for disaster recovery and burst transcode only.**

Start with the **lean build (~$149,000)**, not the full build, and expand into the full topology as subscribers arrive.

| | Recommended |
| --- | --- |
| Primary platform | Owned hardware, Dhaka Tier-3 colocation |
| Initial capital | **~$149,000** (lean build) → ~$248,000 (full build, phased) |
| Monthly operating cost | **~$7,500** (lean) → ~$9,850 (full) |
| 3-year TCO | **~$560,000** (full build) |
| Cloud role | DR restore target; burst transcode at peak; cold archive of masters |
| Latency to users | 5–20 ms over BDIX |

---

## 2. Why — The Three Reasons That Decide It

### 2.1 BDIX is a product capability, not a cost line

This is the reason that would stand even if the cost comparison were neutral.

Bangladeshi consumer ISP packages routinely differentiate sharply between **domestic BDIX-peered traffic and international traffic**, frequently delivering several times the throughput on domestic content. An entire Bangladeshi hosting industry exists specifically to sell BDIX-peered hosting for this reason.

A Bangladesh-only streaming service hosted in Mumbai or Singapore is, to every one of its users, **international content**. Your 5 Mbps stream competes for the customer's international bandwidth allocation — which on a mid-tier broadband package may be substantially constrained, and on mobile is subject to the operator's transit economics.

The practical consequences are worse than the latency number suggests: higher rebuffer rates and more ABR downshifts than any bandwidth model predicts, disproportionately affecting your most price-sensitive subscribers, with no ability to negotiate peering because you have no BD presence.

**You cannot buy your way out of this from outside the country.**

### 2.2 The two things this workload needs most are the two things cloud prices worst

| Resource | Requirement | Cloud | On-prem |
| --- | --- | --- | --- |
| **Egress** | ~235 TB/month | ~$15,700/mo (AWS Mumbai) | ~$3,000/mo (BDIX + domestic transit) |
| **Shared POSIX storage** | 50 TB @ 625 MB/s | ~$5,400/mo (self-managed ZFS on EBS) | ~$150 per usable TB, **once** |

Together these are **~$21,100/month on AWS** — more than the entire compute footprint, and neither benefits from reservation discounts. Egress is 47 % of the on-demand AWS bill.

The storage comparison is the starker one: fully loaded (chassis, redundancy, replica node included) the hardware is ~$150 per usable TB one-time, against ~$1,980 per usable TB over three years on EBS `st1`. **The storage tier pays for itself in roughly four months and then keeps working for five years.**

### 2.3 Marginal economics are 3× better on-prem, and marginal economics are what determine whether the business works

| | Marginal cost per concurrent stream/month | Marginal cost per subscriber/month @ 12.5 % peak ratio |
| --- | --- | --- |
| AWS Mumbai (committed) | ~$25 | $3.13 |
| **On-prem, Dhaka** | **~$8** | **$0.99** |

Roughly 77 % of the on-prem marginal cost is bandwidth; everything else is nearly free until you cross a hardware step.

Against typical Bangladeshi streaming price points of BDT 200–400/month ($1.64–3.28), **an AWS marginal cost of $3.13 per subscriber consumes most or all of the subscription.** At $0.99, there is a business.

---

## 3. The Honest Counter-Argument

**Rented bare metal in Singapore is cheaper than owning hardware in Dhaka.** This deserves to be stated plainly rather than argued away.

| Option | 3-year TCO | Capital required |
| --- | --- | --- |
| Rented bare metal, Singapore | ~$252,000–324,000 | $0 |
| **On-prem, Dhaka** | **~$560,000** | **~$149,000–248,000** |
| AWS Mumbai, committed | ~$813,000 | $0 |

On-prem costs roughly **$240,000–300,000 more over three years** than rented bare metal, and requires substantial capital up front.

**The case for paying that premium:**

1. **BDIX** (§2.1) — the decisive factor, and unavailable at any price from Singapore
2. **Residual value** — hardware retains ~25 % at year three, narrowing the real gap to ~$200,000–260,000
3. **No provider risk** — no exposure to pricing changes, capacity constraints, or a provider exiting the region
4. **Free over-provisioning** — spare CPU, RAM, and disk cost nothing recurring; on rented hardware every upgrade is permanent margin
5. **Regulatory direction** — Bangladesh's posture on data localization is tightening, not loosening

**The case against, and when to take it seriously:**

If capital is genuinely unavailable, **rented bare metal in Singapore is a legitimate launch platform** — far better than AWS on every axis. Launch there, measure real subscriber behaviour, and migrate to Dhaka colocation once revenue supports the capital. Silo's architecture makes this migration tractable: proxy and transcode nodes are stateless or token-pinned, and the only genuinely hard part is moving 50 TB of media.

**A third option worth pricing before deciding either way:** several Bangladeshi providers offer dedicated servers and colocation with BDIX peering. If any can supply **GPU-equipped dedicated servers in-country**, that combination — BDIX delivery with no capital outlay — would likely beat both alternatives outright. This is a week of phone calls and should be done before committing.

---

## 4. What the Codebase Says About How to Deploy It

Nine findings from the Silo source shape the deployment regardless of platform. Full detail and code references are in the cloud document (§2).

| # | Finding | Consequence |
| --- | --- | --- |
| 1 | Media is addressed by **absolute POSIX path** in the signed stream token; no object-storage path exists for media | 50 TB must be mounted at an identical path on every API, proxy, and transcode node. This is the constraint that makes cloud expensive |
| 2 | Clients connect **directly to proxy nodes**; Silo's own planner load-balances streams | Each proxy needs a public IP, DNS name, and TLS cert. **Never put a load balancer in front of the proxy tier** — it bypasses Silo's capacity admission |
| 3 | API session state is a **per-process in-memory map** | Sticky sessions are mandatory on the API load balancer |
| 4 | Per-user stream limits are counted **per API process** | **Revenue leak.** Must be fixed before launch (§5.1) |
| 5 | API servers transcode locally by default; multi-front-end causes split-brain | Set `playback.local_transcode_fallback = false` |
| 6 | Nodes are **static URL rows in PostgreSQL**, health-checked every 30 s | **Do not use Kubernetes for streaming tiers.** Run them as pets with stable DNS |
| 7 | **No source-media cache** | Storage must sustain full aggregate read bandwidth (~625 MB/s) |
| 8 | HLS segments accumulate for the whole session | 1–2 TB NVMe scratch per transcode node, not 120 GB |
| 9 | Silo self-tunes PostgreSQL via `ALTER SYSTEM` | Self-managed PostgreSQL works better than RDS — a genuine on-prem advantage |

Finding 6 is worth emphasizing because it inverts the usual instinct: **this workload wants less orchestration, not more.** Silo already implements capacity-aware scheduling with per-node job caps, bandwidth admission, and co-location groups. Kubernetes would replace that with round-robin and break node registration in the process.

---

## 5. Three Things That Must Happen Before Launch

These are blocking, and none depends on the hosting decision.

### 5.1 Fix global per-user stream-limit enforcement

`SessionManager.ActiveCount(userID)` counts only sessions held by that process (`internal/playback/session.go:1089`). With three API servers, a user can open up to three times their plan limit by spreading sessions across servers.

For a service whose pricing has a "how many screens" dimension, this is direct revenue leakage and an anti-abuse hole. Sticky sessions reduce exposure but do not close it.

**Fix:** move admission counting to Redis or PostgreSQL. Redis is already a hard dependency on every node, so this is contained work. **Effort: medium. Priority: blocking.**

### 5.2 Build a delivery encode ladder from your masters

Direct play delivers the **source file's** bitrate, not a ladder rung. Direct-playing production masters at 15–25 Mbps triples to quintuples every bandwidth number in both analyses — and bandwidth is 77 % of marginal cost.

Normalizing the library to H.264 High@L4.1 + AAC in MP4, with pre-built 1080p/720p/480p renditions, does two things at once: it collapses egress *and* it moves the stream mix toward Scenario A, halving transcode hardware.

**This is the single highest-leverage action available**, and as a production house you fully control it. **Effort: medium. Priority: blocking.**

### 5.3 Measure the real stream mix before ordering transcode hardware

All sizing assumes Scenario B (50 % direct play, 25 % remux, 25 % transcode). Scenario C would need roughly double the transcode hardware (+$40,000); Scenario A roughly half (−$27,000).

Silo records `play_method` per session in `playback_sessions_sync`. Deploy on existing hardware, load a representative slice of the library, test against your actual target devices, and measure.

**Effort: low. Priority: blocking — this determines a $67,000 swing in the hardware order.**

---

## 6. Also Worth Doing

| Item | Why | Effort |
| --- | --- | --- |
| Convert bitmap (PGS) subtitles to text during ingest | Burn-in forces a GPU→CPU→GPU round trip that can halve GPU density | Low |
| Shorten `MaxTokenTTL` from 24 h | A banned user's in-flight stream cannot currently be cut server-side | Low (understand the restart-resilience trade-off) |
| Evaluate URL-based media resolution (extend the `.strm` mechanism) | Would unlock object storage for media — large cloud saving, and simplifies on-prem too | Medium-High |
| Set conservative `MaxBandwidthKbps` and stagger release notifications | Admission control uses a coarse 60 s rolling window and can overshoot on synchronized session starts | Trivial |
| Instrument ARC/L2ARC hit ratio and per-node saturation from day one | These are the leading indicators for every capacity threshold | Low |

---

## 7. Recommended Path

### Phase 0 — Validate before spending (weeks 1–4, minimal cost)

The estimates with the widest error bars are all resolvable with phone calls and a test deployment. **Do not order hardware before these are answered.**

1. **Bandwidth quotes** from three Dhaka facilities — BDIX transport, domestic transit, and specifically what peering exists with Grameenphone, Robi, and Banglalink. *This is the largest uncertainty in the entire model.*
2. **Power density** — confirm a facility can deliver 8 kW in one rack. Many BD facilities provision 3–5 kW as standard; this is the most likely disqualifier.
3. **Import duty** — confirm with a clearing agent against your specific bill of materials.
4. **Stream mix measurement** (§5.3).
5. **GPU benchmark** — validate transcode density on your real content, including subtitle burn-in.
6. **Price BD dedicated servers with GPU** (§3) — if this exists, it may beat the whole plan.
7. **Legal review** — BTRC licensing, content classification, data-residency obligations.

**Decision gate:** if bandwidth quotes come back near IIG rates, or the transcode share measures at Scenario C, revisit the sizing before ordering.

### Phase 1 — Launch lean (~$149,000 capital)

Build the lean topology: 1 storage + cold spare, 2 transcode, 2 proxy, 2 API, 1 database + replica, co-located Redis, and — do not skip this — 2 load balancers.

This meets 500 concurrent with no failure headroom. That is an acceptable trade at launch, when subscriber count is low and the cost of a degraded hour is small. It also defers roughly $100,000 of capital until subscriber revenue justifies it.

Run Phase 0's blocking fixes (§5) in parallel with procurement.

### Phase 2 — Fill the capacity

The infrastructure is sized by peak concurrency and paid for regardless of subscriber count. At 1,000 subscribers the fully-loaded cost is **$16.75/subscriber/month**; at 5,000 it is **$3.35**.

**The fastest route to working unit economics is subscriber growth, not infrastructure reduction.** Marginal subscribers cost about $1/month at a realistic peak concurrency ratio.

### Phase 3 — Harden as revenue supports it

Add nodes against the expansion thresholds (on-prem document, §10.5), in this order:

1. Third proxy node and third API node — removes the launch-phase failure exposure
2. Second storage node with live ZFS replication — removes the largest single point of failure
3. Third transcode node — capacity headroom
4. Second site or a rehearsed cloud DR restore target — removes single-site risk

---

## 8. What Would Change This Recommendation

| If... | Then... |
| --- | --- |
| Bandwidth quotes come back at $6,000–7,000/month (mobile transit priced near IIG) | On-prem still beats AWS comfortably, but the gap to rented bare metal widens. Re-examine, and push harder on direct mobile-operator peering |
| No Dhaka facility can supply 8 kW/rack at acceptable cost | Split across two racks (+~$1,800/mo), or reconsider a BD provider's dedicated servers |
| A BD provider offers GPU-equipped dedicated servers with BDIX | **Take it.** BDIX delivery with no capital outlay likely beats this plan outright |
| Capital is genuinely unavailable | Launch on rented bare metal in Singapore; migrate to Dhaka once revenue supports the capital. Accept the delivery-quality penalty as a temporary cost |
| The measured stream mix is Scenario A (10 % transcode) | Drop to one transcode node; save ~$27,000 and revisit whether the lean build needs expanding at all |
| The measured stream mix is Scenario C (50 % transcode) | Add ~$40,000 of transcode hardware — and treat §5.2 (encode ladder) as urgent rather than merely blocking |
| Subscriber growth stalls below ~2,000 | The economics do not work on any platform at 500-concurrent capacity. The problem is demand, not hosting |

---

## 9. Summary

| | AWS Mumbai | Bare metal, Singapore | **On-prem, Dhaka** |
| --- | --- | --- | --- |
| 3-year TCO | ~$813,000 | ~$252,000–324,000 | **~$560,000** |
| Capital required | $0 | $0 | ~$149,000–248,000 |
| Marginal $/concurrent stream/mo | ~$25 | ~$10 **(est.)** | **~$8** |
| Latency from Dhaka | 40–55 ms | 60–80 ms | **5–20 ms** |
| Routing to BD users | International | International | **Domestic / BDIX** |
| Operational burden | Low | Medium | High |
| **Verdict** | Reject | Viable fallback | **Recommended** |

**Build in Dhaka.** The cost advantage over AWS is decisive; the delivery-quality advantage over any offshore option is what makes the product competitive in its actual market.

**But treat Phase 0 as genuinely gating.** The bandwidth estimate has the widest error bars in this analysis and can be resolved in a week of phone calls. Resolve it before spending capital.

**And be clear-eyed about the two structural facts:** the infrastructure is only economically sensible at 2,500+ subscribers, and Silo is early-stage software by its own documentation — this deployment needs a permanent engineering budget, not just an operations budget.
