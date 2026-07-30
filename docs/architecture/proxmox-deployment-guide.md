# Silo Distributed Deployment Guide — Proxmox LXC

**Hardware:** ThinkPad E580 · Core i5 · 16 GB RAM · 256 GB SSD · 1 TB HDD
**Platform:** Proxmox VE with 5 LXC containers (Ubuntu 26.04)
**Target:** Production-ready Silo with distributed transcode, S3 metadata storage, and Intel QSV hardware acceleration.

## Container Resource Allocation

| CT ID | Name           | vCPUs | RAM  | Swap   | SSD (root) | HDD (data) | Purpose                          |
| ----- | -------------- | ----- | ---- | ------ | ---------- | ---------- | -------------------------------- |
| 100   | **silo-api**   | 2     | 2 GB | 1 GB   | 15 GB      | —          | Silo API server + web frontend   |
| 101   | **silo-minio** | 1     | 1 GB | 512 MB | 10 GB      | 60 GB      | S3-compatible object storage     |
| 102   | **silo-db**    | 3     | 5 GB | 2 GB   | 60 GB      | —          | PostgreSQL 18 + pgvector + Redis |
| 103   | **silo-tc-1**  | 5     | 2 GB | 1 GB   | 10 GB      | 120 GB     | Transcode worker #1              |
| 104   | **silo-tc-2**  | 5     | 2 GB | 1 GB   | 10 GB      | 120 GB     | Transcode worker #2              |
| —     | **Proxmox OS** | —     | 2 GB | —      | 30 GB      | —          | Hypervisor overhead              |
| —     | **Free**       | 0     | 2 GB | —      | 121 GB     | 700 GB     | Headroom                         |

> **Swap** lives on the SSD (fast enough for emergency paging). It's a safety net, not a performance feature — PostgreSQL under memory pressure or ffmpeg on a heavy transcode are the most likely consumers. Total swap across all CTs: ~5.5 GB.

### Create all containers at once (Proxmox host)

```bash
# Download Ubuntu 26.04 template (first time only)
pveam update
pveam download local ubuntu-26.04-standard_26.04-1_amd64.tar.zst

# Create the CTs (all unprivileged with nesting for Docker)
pct create 100 local:vztmpl/ubuntu-26.04-standard_26.04-1_amd64.tar.zst \
  --hostname silo-api --cores 2 --memory 2048 --swap 1024 --rootfs local-lvm:15 \
  --net0 name=eth0,bridge=vmbr0,ip=dhcp --unprivileged 1 --features nesting=1

pct create 101 local:vztmpl/ubuntu-26.04-standard_26.04-1_amd64.tar.zst \
  --hostname silo-minio --cores 1 --memory 1024 --swap 512 --rootfs local-lvm:10 \
  --net0 name=eth0,bridge=vmbr0,ip=dhcp --unprivileged 1 --features nesting=1

pct create 102 local:vztmpl/ubuntu-26.04-standard_26.04-1_amd64.tar.zst \
  --hostname silo-db --cores 3 --memory 5120 --swap 2048 --rootfs local-lvm:60 \
  --net0 name=eth0,bridge=vmbr0,ip=dhcp --unprivileged 1 --features nesting=1

pct create 103 local:vztmpl/ubuntu-26.04-standard_26.04-1_amd64.tar.zst \
  --hostname silo-tc-1 --cores 5 --memory 2048 --swap 1024 --rootfs local-lvm:10 \
  --net0 name=eth0,bridge=vmbr0,ip=dhcp --unprivileged 1 --features nesting=1

pct create 104 local:vztmpl/ubuntu-26.04-standard_26.04-1_amd64.tar.zst \
  --hostname silo-tc-2 --cores 5 --memory 2048 --swap 1024 --rootfs local-lvm:10 \
  --net0 name=eth0,bridge=vmbr0,ip=dhcp --unprivileged 1 --features nesting=1
```

### Bind-mount storage (Proxmox host)

Some mounts are created via the Proxmox GUI during CT creation (storage-backed volumes from `hdd-thin`), others are directory bind-mounts from the host filesystem. The final config for each CT should look like:

| CT               | mp0                                             | mp1                            |
| ---------------- | ----------------------------------------------- | ------------------------------ |
| 100 (silo-api)   | `/mnt/media` (media files, ro)                  | —                              |
| 101 (silo-minio) | `/mnt/minio-data` (bucket, 60 GB from hdd-thin) | —                              |
| 102 (silo-db)    | —                                               | —                              |
| 103 (silo-tc-1)  | `/var/lib/silo-transcode` (120 GB from hdd-thin)    | `/mnt/media` (media files, ro) |
| 104 (silo-tc-2)  | `/var/lib/silo-transcode` (120 GB from hdd-thin)    | `/mnt/media` (media files, ro) |

#### Transcode temp (hdd-thin) — created via GUI during CT creation

For CT 103 and 104, add a mount point in the creation wizard:

- **Storage:** `hdd-thin`
- **Disk size:** `120` GiB
- **Path:** `/var/lib/silo-transcode`

Or via CLI after creation:

```bash
pct set 103 -mp0 hdd-thin:120,mp=/var/lib/silo-transcode
pct set 104 -mp0 hdd-thin:120,mp=/var/lib/silo-transcode
```

#### MinIO bucket (hdd-thin) — created via GUI

For CT 101, add during creation or after:

```bash
pct set 101 -mp0 hdd-thin:60,mp=/mnt/minio-data
```

#### Media files (directory bind-mount from host)

Your `.strm` files live on the Proxmox host at `/mnt/media`. Mount them read-only into the API and both TC containers:

```bash
# Transfer .strm files to the host first (from your Mac):
# scp -r /Users/atiab/Desktop/strm_library_sam/* root@<proxmox-ip>:/mnt/media/

# Mount into API (uses mp0 — no hdd-thin volumes on this CT)
pct set 100 -mp0 /mnt/media,/mnt/media,ro=1

# Mount into TC nodes (uses mp1 — mp0 is already taken by transcode temp)
pct set 103 -mp1 /mnt/media,/mnt/media,ro=1
pct set 104 -mp1 /mnt/media,/mnt/media,ro=1
```

If mp1 already exists as a placeholder, clear it first:

```bash
pct set 103 --delete mp1 && pct set 103 -mp1 /mnt/media,/mnt/media,ro=1
pct set 104 --delete mp1 && pct set 104 -mp1 /mnt/media,/mnt/media,ro=1
```

Verify all mounts:

```bash
pct config 103 | grep "^mp"
# mp0: hdd-thin:vm-103-disk-1,mp=/var/lib/silo-transcode,size=120G
# mp1: /mnt/media,/mnt/media,ro=1
```

### Typical folder structure

Your `.strm` library should look like this on disk:

```
/mnt/media/
├── Movies/
│   ├── Inception (2010)/
│   │   └── Inception.2010.1080p.BluRay.x264.strm
│   └── The Matrix (1999)/
│       └── The.Matrix.1999.1080p.strm
└── TV Shows/
    └── Breaking Bad (2008)/
        ├── Season 1/
        │   ├── Breaking.Bad.S01E01.1080p.strm
        │   └── Breaking.Bad.S01E02.1080p.strm
        └── Season 2/
            └── ...
```

Each `.strm` file contains exactly one HTTP/HTTPS URL pointing to the remote media file.

### Transfer `.strm` files from your computer to Proxmox

The files go on the **Proxmox host's HDD** at `/mnt/hdd/media/` — the same path you bind-mounted into the CTs earlier. No mount needed on your Mac.

```bash
# Step 1: On Proxmox host, create the destination directory
ssh root@<proxmox-ip> "mkdir -p /mnt/hdd/media"

# Step 2: Copy your .strm library from your Mac
# Replace the source path with wherever your .strm files live
scp -r /Users/atiab/Desktop/strm_library_sam/* root@<proxmox-ip>:/mnt/hdd/media/

# For large libraries, rsync is faster (resumes if interrupted):
rsync -avP /Users/atiab/Desktop/strm_library_sam/ root@<proxmox-ip>:/mnt/hdd/media/
```

After the transfer, verify the folder structure on Proxmox:

```bash
ssh root@<proxmox-ip> "ls /mnt/hdd/media/"
# Should show your TV Shows and Movies folders
```

The CTs will see the exact same tree at `/mnt/media` (via the bind-mount). No duplication — all three containers share the same files from the host.

> **`.strm` files are tiny** — each is ~200 bytes. Even 50,000 files is only ~10 MB. Transfer completes in seconds.

````

---

## Prerequisites (Every CT)

Start with a fresh Ubuntu 26.04 LXC template. Run on **every** container:

```bash
apt update && apt upgrade -y
apt install -y curl gnupg ca-certificates docker.io docker-compose-v2

# Enable and start Docker
systemctl enable --now docker
```

### Network fix (all CTs)

Ubuntu 26.04 LXC templates may ship without network config, leaving `eth0` down. Run this once per CT:

```bash
mkdir -p /etc/systemd/network
cat > /etc/systemd/network/eth0.network <<'EOF'
[Match]
Name=eth0

[Network]
DHCP=yes
EOF

systemctl enable --now systemd-networkd
```

Verify with `ip a show eth0` — should show `state UP` with a DHCP IP.

Alternatively, run this bootstrap script from the Proxmox host to set up all five CTs at once:

```bash
#!/bin/bash
for id in 100 101 102 103 104; do
  echo "=== Bootstrapping CT $id ==="
  pct exec $id -- bash -c '
    export DEBIAN_FRONTEND=noninteractive
    apt update && apt upgrade -y
    apt install -y curl gnupg ca-certificates docker.io docker-compose-v2
    systemctl enable --now docker

    mkdir -p /etc/systemd/network
    cat > /etc/systemd/network/eth0.network <<NET
[Match]
Name=eth0
[Network]
DHCP=yes
NET
    systemctl enable --now systemd-networkd
  ' &
done
wait
echo "=== All CTs ready ==="
````

> **Network assumption:** All CTs are on the same Proxmox bridge (`vmbr0`) and can reach each other by IP. Replace `<silo-db-ip>`, `<silo-api-ip>`, `<silo-minio-ip>`, etc. with actual addresses throughout this guide.

---

## 1. MinIO — S3 Object Storage (CT 101)

**Resources:** 1 vCPU · 1 GB RAM · 10 GB SSD · 60 GB HDD for bucket data

The HDD bind-mount was already set up at the top of this guide (`/mnt/hdd/minio` → `/mnt/minio-data`).

### 1.1 Docker Compose

Create `/opt/minio/docker-compose.yml`:

```yaml
services:
  minio:
    image: minio/minio:latest
    restart: unless-stopped
    environment:
      MINIO_ROOT_USER: silo-admin
      MINIO_ROOT_PASSWORD: r8Kp2mXv9qL5nW3yA7bF # generate a strong password
    command: server /data --console-address ":9001"
    ports:
      - "9000:9000" # S3 API (used by Silo)
      - "9001:9001" # Web console (your browser)
    volumes:
      - /mnt/minio-data:/data
```

Start it:

```bash
cd /opt/minio && docker compose up -d
```

### 1.2 Create the Silo bucket

Open the MinIO web console at `http://<silo-minio-ip>:9001` and log in with `silo-admin` / your password.

Create a bucket named **`silo`** (all lowercase). Set its access policy to **private**.

### 1.3 Create access keys for Silo

In the MinIO console, go to **Access Keys → Create Access Key**. Save the **Access Key** and **Secret Key** — you'll need them in Section 3 when configuring the Silo API server.

### 1.4 Verify

```bash
curl -s http://<silo-minio-ip>:9000/silo/
# Should return XML listing (empty bucket is fine)
```

---

## 2. PostgreSQL + pgvector + Redis (CT 102)

**Resources:** 3 vCPU · 5 GB RAM · 60 GB SSD

All services installed natively (no Docker on this CT).

### 2.1 Install PostgreSQL 18 + pgvector

Ubuntu 26.04 ships PostgreSQL 18 and pgvector in its default repos — no external PPA needed.

```bash
apt update
apt install -y postgresql postgresql-pgvector
```

### 2.2 Configure PostgreSQL

Edit `/etc/postgresql/18/main/postgresql.conf`:

```ini
listen_addresses = '*'
shared_buffers = 1GB
effective_cache_size = 3GB
```

Edit `/etc/postgresql/18/main/pg_hba.conf` — add at the end:

```
# Allow connections from other CTs on the Proxmox bridge
host    all             all             192.168.0.0/16          md5
host    all             all             10.0.0.0/8              md5
```

Restart PostgreSQL:

```bash
systemctl restart postgresql
```

### 2.3 Create the Silo database and enable pgvector

```bash
sudo -u postgres psql <<SQL
CREATE USER silo WITH PASSWORD 'silo' CREATEDB;
CREATE DATABASE silo OWNER silo;
\c silo
CREATE EXTENSION IF NOT EXISTS vector;
SQL
```

### 2.4 Install and configure Redis

```bash
apt install -y redis-server
```

Edit `/etc/redis/redis.conf`:

```ini
bind 0.0.0.0
protected-mode no
```

Restart Redis:

```bash
systemctl restart redis-server
```

### 2.5 Verify connectivity from another CT

```bash
# From any other CT:
apt install -y postgresql-client

# Test PostgreSQL
psql -h <silo-db-ip> -U silo -d silo -c "SELECT extname FROM pg_extension;"
# Should show: vector

# Test Redis
apt install -y redis-tools
redis-cli -h <silo-db-ip> ping
# Should return: PONG
```

---

## 3. Silo API + Web Frontend (CT 100)

**Resources:** 2 vCPU · 2 GB RAM · 15 GB SSD

This CT runs your **custom build** of Silo (includes the .strm ffprobe fixes, release-token regex cleaning, and subtitle path resolution).

### 3.1 Generate the master encryption key

```bash
openssl rand -base64 48
# Save this output — you'll reuse it for transcode nodes too
```

### 3.2 Clone the repo

```bash
# Clone your fork/branch with the customizations
cd /opt
git clone https://github.com/<your-org>/silo-server.git silo
cd silo
git checkout feat/strm-playback          # or your branch name
```

### 3.3 Build the custom Silo image

The build happens entirely inside Docker — no Go toolchain needed on the host. The `docker-compose.dev.yml` inline Dockerfile uses `FROM golang:1.26` and runs `go build` inside the container.

```bash
cd /opt/silo
docker compose -f docker-compose-proxmox.yml build silo --no-cache
```

This compiles the Go backend with all our customizations (.strm ffprobe, release-token regex, subtitle path fixes) into a local Docker image tagged `silo-api:latest`. The Go toolchain runs inside the Docker build — nothing to install on the host.

### 3.4 Environment file

Create `/opt/silo/.env`:

```env
SECRET_KEY=<the-base64-key-from-step-3.1>
DATABASE_URL=postgres://silo:silo@<silo-db-ip>:5432/silo?sslmode=disable
REDIS_URL=redis://<silo-db-ip>:6379
MEDIA_ROOT=/mnt/media
# SERVER_LOG_LEVEL=debug    # enable temporarily for troubleshooting
```

### 3.5 Start the API server

```bash
cd /opt/silo
docker compose -f docker-compose-proxmox.yml up -d
```

### 3.6 Verify

```bash
# Health check
curl -s http://localhost:80/api/v1/health
# Should return: {"status":"ok"}

# HW acceleration status
curl -s http://localhost:80/hw-capabilities | python3 -m json.tool
# Should show: "resolved": "qsv", "intel_detected": true
```

Open `http://<silo-api-ip>` in your browser — the UI serves on port 80. Complete the setup wizard:

- Point S3 at `http://<silo-minio-ip>:9000` with the access key from Section 1.3
- Add your media library (the `.strm` directory mounted at `/mnt/media`)
- Configure metadata providers (TMDB, TVDB)

### 3.6 Register transcode nodes (after Section 4)

In the Silo admin UI → **Settings → Nodes**, add:

- `http://<silo-tc1-ip>:8080` (label: `tc-1`)
- `http://<silo-tc2-ip>:8080` (label: `tc-2`)

Set **Local transcode fallback** to **Disabled** — this forces all transcoding to the dedicated nodes.

---

## 4. Transcode Nodes (CT 103 & CT 104)

**Resources each:** 5 vCPU · 2 GB RAM · 10 GB SSD · 120 GB HDD

These use the **stock DockerHub image** — no custom build needed. The HDD transcode storage and media mounts were already set up in the bind-mount section at the top of this guide.

> **Note:** Both transcode CTs write HLS segments to this volume. Each session gets a unique subdirectory (`/var/lib/silo-transcode/<session-uuid>/`), so there's no collision. Old sessions are cleaned up when playback ends.

> **Why `/var/lib/silo-transcode` and not `/tmp/silo-transcode`:** Ubuntu LXC containers mount `/tmp` as a separate tmpfs, which swallows any block device mount underneath it. Using a path outside `/tmp` avoids this collision.

### 4.1 Docker Compose (identical on both CTs)

Create `/opt/silo-tc/docker-compose.yml`:

```yaml
services:
  silo-transcode:
    image: ghcr.io/silo-server/silo-server:latest   # stock image
    restart: unless-stopped
    environment:
      MODE: transcode
      PORT: "8080"
      # MUST be the EXACT SAME SECRET_KEY as the API server
      SECRET_KEY: "<same-key-from-section-3.1>"
      DATABASE_URL: postgres://silo:silo@<silo-db-ip>:5432/silo?sslmode=disable
      REDIS_URL: redis://<silo-db-ip>:6379
      NODE_NAME: tc-1                  # tc-2 on the other CT
    ports:
      - "8080:8080"                    # transcode API (called by the API server)
    volumes:
      - /var/lib/silo-transcode:/var/lib/silo-transcode
      - /mnt/media:/mnt/media:ro        # .strm files (same path as API server)
      - /dev/dri:/dev/dri               # Intel iGPU for QSV
    devices:
      - /dev/dri:/dev/dri
```

Start it:

```bash
cd /opt/silo-tc && docker compose up -d
```

#### Set the transcode directory

In the Silo admin UI, go to **Settings → Playback** and set:

```
playback.transcode_dir = /var/lib/silo-transcode
```

This tells both the API server and transcode nodes where to write HLS segments. If you don't set this, the default `/tmp/silo-transcode` will be used — which won't work because `/tmp` is a tmpfs inside Ubuntu containers.

### 4.2 Verify

```bash
# Health check
curl -s http://localhost:8080/api/v1/health

# HW capabilities
curl -s http://localhost:8080/hw-capabilities | python3 -m json.tool
# Should show: "resolved": "qsv"
```

### 4.3 Repeat for the second transcode node

Do exactly the same on CT 103, but set `NODE_NAME: tc-2` in the compose file.

---

## 5. Post-Installation Checklist

- [ ] MinIO console accessible at `http://<silo-minio-ip>:9001`
- [ ] PostgreSQL reachable from API and transcode CTs: `psql -h <silo-db-ip> -U silo -d silo`
- [ ] Redis reachable: `redis-cli -h <silo-db-ip> ping` → `PONG`
- [ ] Silo Web UI loads at `http://<silo-api-ip>` (port 80)
- [ ] S3 configured in Silo setup wizard → test connection succeeds
- [ ] Media library added → scan completes
- [ ] Both transcode nodes appear in Admin → Nodes
- [ ] `playback.transcode_dir` set to `/var/lib/silo-transcode` in Admin → Settings → Playback
- [ ] HW acceleration reports `qsv` on all three Silo instances (API + both TC nodes)
- [ ] Playback test: start a stream → check Admin → Active Sessions shows `transcode_hw_accel: qsv`

---

## 6. Network Reference

| Service        | CT               | Port            | Accessed By           |
| -------------- | ---------------- | --------------- | --------------------- |
| PostgreSQL     | silo-db (102)    | 5432            | API, TC-1, TC-2       |
| Redis          | silo-db (102)    | 6379            | API, TC-1, TC-2       |
| MinIO S3 API   | silo-minio (101) | 9000            | API                   |
| MinIO Console  | silo-minio (101) | 9001            | Your browser          |
| Silo API + Web | silo-api (100)   | 80, 8096, 13378 | Browsers, client apps |
| Transcode API  | silo-tc-1 (103)  | 8080            | API server            |
| Transcode API  | silo-tc-2 (104)  | 8080            | API server            |

---

## 7. Maintenance

### Update the custom API image

```bash
# On the API CT: pull latest code and rebuild
cd /opt/silo
git pull origin feat/strm-playback      # or your branch
docker compose -f docker-compose-proxmox.yml build silo --no-cache
docker compose up -d
```

### Update stock transcode images

```bash
# On each transcode CT:
docker pull ghcr.io/silo-server/silo-server:latest
cd /opt/silo-tc && docker compose up -d
```

### Enable debug logging (troubleshooting)

Set `SERVER_LOG_LEVEL=debug` in `/opt/silo/.env` on the API CT, then restart:

```bash
cd /opt/silo && docker compose up -d
```

Provider search queries will now appear in the logs:

```bash
docker compose logs silo | grep "query_title"
```
