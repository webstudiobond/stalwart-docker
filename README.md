# Hardened Containerized Stalwart Mail Server

[![CI](https://github.com/webstudiobond/stalwart-docker/actions/workflows/ci.yml/badge.svg)](https://github.com/webstudiobond/stalwart-docker/actions/workflows/ci.yml)
[![Build](https://github.com/webstudiobond/stalwart-docker/actions/workflows/build.yml/badge.svg)](https://github.com/webstudiobond/stalwart-docker/actions/workflows/build.yml)
[![GitHub last commit](https://img.shields.io/github/last-commit/webstudiobond/stalwart-docker)](https://github.com/webstudiobond/stalwart-docker/commits/main)
[![GitHub issues](https://img.shields.io/github/issues/webstudiobond/stalwart-docker)](https://github.com/webstudiobond/stalwart-docker/issues)
[![GitHub repo size](https://img.shields.io/github/repo-size/webstudiobond/stalwart-docker)](https://github.com/webstudiobond/stalwart-docker)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

Containerized deployment architecture for [Stalwart Mail Server](https://stalw.art) using [PostgreSQL](https://www.postgresql.org/) for persistent storage, [Valkey](https://valkey.io/) for in-memory caching, and [Angie](https://en.angie.software/) as the perimeter reverse proxy.

## Architecture Highlights

* **Scratch Runtime:** Built `FROM scratch` with CA certificates and no shell, package manager, or system utilities. The `server` image adds `stalwart` along with two static Go binaries: `init` (loads optional recovery secrets and bridges loopback Valkey connections to a UNIX domain socket before starting the server) and `probe` (queries `/healthz/live` for container healthchecks). The `cli` image adds only `stalwart-cli`.
* **Socket-Only Database & Cache IPC:** Both PostgreSQL and Valkey run with networking completely disabled. Each service communicates over its own isolated, in-memory `tmpfs` UNIX domain socket restricted to the unprivileged container user, preventing cross-service socket access.
* **Least-Privilege Execution:** Services run as an unprivileged host user with read-only root filesystems, privilege escalation disabled, and all Linux capabilities dropped—except `NET_BIND_SERVICE` on the mail server for ports `25`, `465`, and `993`.
* **File-Backed Secret Injection:** Database passwords, recovery accounts, DKIM private keys, and DNS API tokens are mounted as read-only Docker Secrets (`/run/secrets/*`), keeping plaintext secrets out of `.env` files and Compose manifests.
* **Locked HTTP Perimeter:** Edge Angie exposes only client discovery and MTA-STS endpoints, returning `404` for `/admin` and `/account` by default. WebAdmin is accessed over an SSH tunnel (`127.0.0.1:18080:8080`) or a private VPN interface (`<VPN_IP>:18080:8080`).
* **On-Demand CLI:** `stalwart-cli` is packaged into a separate scratch image under `profiles: [tools]` and shares the mail server's network namespace on demand, keeping administrative tooling out of the runtime container.

---

<details>
<summary><strong>Directory Structure</strong></summary>

Production directory layout on the host (`/home/stalwart`):

```text
├── docker-compose.yaml                # Production deployment manifest
├── .env                               # Host infrastructure variables (UID, GID, HOST_IP, image versions)
├── config/                            # Service configuration
│   ├── stalwart/
│   │   └── config.json                # Bootstrap-generated store connection config
│   └── valkey/
│       └── valkey.conf                # In-memory Valkey configuration
├── secrets/                           # Docker secrets directory
│   ├── db_name.txt                    # PostgreSQL database name
│   ├── db_user.txt                    # PostgreSQL username
│   ├── db_password.txt                # PostgreSQL password
│   ├── stalwart_recovery_user.txt     # Recovery administrator username
│   ├── stalwart_recovery_password.txt # Recovery administrator password
│   ├── cf_api_token.txt               # Optional: DNS provider API token for ACME DNS-01
│   ├── dkim_ed25519.key               # Optional: Ed25519 DKIM private key
│   └── dkim_rsa.key                   # Optional: RSA-2048 DKIM private key
├── postgres/                          # Persistent PostgreSQL data directory
├── data/                              # Stalwart blob storage
└── logs/                              # Stalwart log directory (consumed by host CrowdSec)
```

</details>

---

<details>
<summary><strong>Deployment &amp; Setup</strong></summary>

### Prerequisites

* **Docker Engine & Compose:** Ensure Docker Engine and the Docker Compose plugin are installed on the host. Follow the official installation guide for [Ubuntu](https://docs.docker.com/engine/install/ubuntu/#install-using-the-repository).
* **External Gateway Network:** The stack communicates with the host's Edge reverse proxy over a shared Docker network named `frontend_gateway` (declared as `external: true`). This network is provisioned by the **[angie-docker-compose](https://github.com/webstudiobond/angie-docker-compose)** stack. If deploying standalone, create it manually:

```bash
docker network create --subnet=172.20.0.0/16 frontend_gateway
```

### 1. Create a Dedicated System User

Choose a dedicated, unprivileged numeric ID unique to this service on your host and verify that it is not already in use by another user or group:

```bash
APP_UID=<CHOSEN_UID>

getent passwd ${APP_UID}
getent group ${APP_UID}
```

> **Important:** Both commands must return empty output. Each containerized service must have its own dedicated unprivileged user and group to enforce strict host DAC privilege separation between different stacks.

Create an unprivileged system account with a matching user-group (`-U`) and disabled login shell:

```bash
sudo useradd -m -d /home/stalwart -s /usr/sbin/nologin -u ${APP_UID} -U stalwart
```

### 2. Create Directory Structure

```bash
sudo mkdir -p /home/stalwart/{config/{stalwart,valkey},data,logs,postgres,secrets}
sudo chown root:stalwart /home/stalwart/secrets
sudo chmod 0750 /home/stalwart/secrets
sudo chown stalwart:stalwart /home/stalwart/{config/valkey,data,logs,postgres}
sudo chmod 0700 /home/stalwart/{config/stalwart,data,logs,postgres}
```

> **Log Access for Non-Root Services (Optional):** By default, `logs/` is restricted to `0700`. If an external unprivileged service (such as CrowdSec running under a non-root account) requires access to parse application logs, grant read permissions to the directory and log files:

```bash
sudo chmod 0755 /home/stalwart/logs
sudo chmod 0644 /home/stalwart/logs/*
```

### 3. Generate Secrets

Install the required tools:

```bash
sudo apt update && sudo apt install -y pwgen openssl
```

**Database and recovery administrator credentials:**

Initialize secret files with strict DAC permissions before writing:

```bash
sudo touch /home/stalwart/secrets/{db_name,db_user,db_password,stalwart_recovery_user,stalwart_recovery_password}.txt
sudo chown root:stalwart /home/stalwart/secrets/{db_name,db_user,db_password,stalwart_recovery_user,stalwart_recovery_password}.txt
sudo chmod 0440 /home/stalwart/secrets/{db_name,db_user,db_password,stalwart_recovery_user,stalwart_recovery_password}.txt

printf "db_%s" "$(pwgen -s -A 6 1)" | sudo tee /home/stalwart/secrets/db_name.txt > /dev/null
printf "u_%s" "$(pwgen -s -A 6 1)" | sudo tee /home/stalwart/secrets/db_user.txt > /dev/null
pwgen -s 64 1 | tr -d '\n' | sudo tee /home/stalwart/secrets/db_password.txt > /dev/null
printf "r_%s" "$(pwgen -s -A 6 1)" | sudo tee /home/stalwart/secrets/stalwart_recovery_user.txt > /dev/null
pwgen -s 64 1 | tr -d '\n' | sudo tee /home/stalwart/secrets/stalwart_recovery_password.txt > /dev/null
```

> NOTE: `stalwart_recovery_user.txt` and `stalwart_recovery_password.txt` are required for initial bootstrap. Although the [official Stalwart documentation](https://stalw.art/docs/configuration/recovery-mode/#recovery-administrator) advises disabling the recovery account in production, leaving it enabled in this stack is safe if desired:
> * The credentials are high-entropy random strings stored in `0440` host files owned by `root:stalwart`, mounted read-only via Docker Secrets, and loaded at startup by the `init` binary without appearing in `docker-compose.yaml` or `.env`.
> * Public access to `/admin` and `/account` is blocked (`404`) by default in Edge Angie (the WebUI location block is commented out). WebAdmin is only reachable over an SSH tunnel (`127.0.0.1:18080:8080`) or a private VPN interface (`<VPN_IP>:18080:8080`).
>
> Both recovery secrets are commented out by default in `docker-compose.yaml` and can be enabled permanently or on demand.

**Optional secrets (DKIM keys & ACME DNS API token):**

Stalwart can generate DKIM keys and manage ACME certificates directly in its database. Alternatively, you can provision keys and API tokens on the host as read-only Docker Secrets (`/run/secrets/*`)—creating as many domain keys as needed—and select **Secret read from file** in WebAdmin:

```bash
sudo openssl genpkey -algorithm ed25519 -out /home/stalwart/secrets/dkim_ed25519.key
sudo openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out /home/stalwart/secrets/dkim_rsa.key
sudo chown root:stalwart /home/stalwart/secrets/dkim_*.key
sudo chmod 0440 /home/stalwart/secrets/dkim_*.key
```

```bash
sudo touch /home/stalwart/secrets/cf_api_token.txt
sudo chown root:stalwart /home/stalwart/secrets/cf_api_token.txt
sudo chmod 0440 /home/stalwart/secrets/cf_api_token.txt
sudo nano /home/stalwart/secrets/cf_api_token.txt
```

### 4. Download Configuration and Compose Manifest

```bash
REPO="https://raw.githubusercontent.com/webstudiobond/stalwart-docker/main"

sudo -u stalwart curl -fsSL ${REPO}/config/valkey/valkey.conf -o /home/stalwart/config/valkey/valkey.conf
sudo curl -fsSL ${REPO}/docker-compose.yaml -o /home/stalwart/docker-compose.yaml
sudo curl -fsSL ${REPO}/examples/.env.example -o /home/stalwart/.env
sudo chown root:root /home/stalwart/{docker-compose.yaml,.env}
sudo chmod 0600 /home/stalwart/{docker-compose.yaml,.env}
```

### 5. Configure Environment

```bash
sed -i "s/^APP_UID=.*/APP_UID=${APP_UID}/" /home/stalwart/.env
sed -i "s/^APP_GID=.*/APP_GID=${APP_UID}/" /home/stalwart/.env
```

Identify your host's public IP address:

```bash
ip --br a
```

Update `/home/stalwart/.env`:
* Set `HOST_IP` to the host's dedicated public IP address so Stalwart binds its public mail ports (`25`, `465`, `993`) strictly to that network interface. Never use `0.0.0.0`.
* **IPv6 Support (Optional):** If IPv6 connectivity is required, define `HOST_IP6` in `/home/stalwart/.env` with your host's dedicated public IPv6 address, and add the corresponding bracketed port mappings (e.g., `"[${HOST_IP6}]:25:25"`, `"[${HOST_IP6}]:465:465"`, `"[${HOST_IP6}]:993:993"`) under `ports:` in `docker-compose.yaml`.

> **Security Note:** `HOST_IP` intentionally defaults to `127.0.0.1` in `.env.example` for initial staging so automated scanners and malicious bots cannot probe or attack the mail server while the administrator completes initial configuration. You may keep `127.0.0.1` during bootstrap, but you must replace it with the host's dedicated public IP once setup is finalized to receive external mail.

```bash
sudo nano /home/stalwart/.env
```

</details>

---

<details>
<summary><strong>Initial Configuration (Bootstrap)</strong></summary>

Stalwart stores its operational configuration, accounts, and domains inside PostgreSQL. Initial setup is performed once via a temporary bootstrap container with outbound network access so Stalwart can download its WebUI asset bundle and write the store connection config to `/home/stalwart/config/stalwart/config.json`.

### 1. Start Database & Cache Services

```bash
sudo docker compose -f /home/stalwart/docker-compose.yaml up -d postgres valkey
sudo docker compose -f /home/stalwart/docker-compose.yaml ps
```

### 2. Launch Temporary Bootstrap Container

Pull the server image and run a temporary container bound to `127.0.0.1:18080`:

```bash
sudo docker compose -f /home/stalwart/docker-compose.yaml pull stalwart

APP_UID="$(id -u stalwart)"
APP_GID="$(id -g stalwart)"

sudo docker run --rm -d \
  --name stalwart_bootstrap \
  --network bridge \
  --user "${APP_UID}:${APP_GID}" \
  --tmpfs /tmp:mode=1777,noexec,nosuid,nodev,size=64m \
  --tmpfs /run:mode=1777,noexec,nosuid,nodev,size=16m \
  -v stalwart_postgres_socket:/var/run/postgresql \
  -v stalwart_valkey_socket:/var/run/valkey \
  -v "/home/stalwart/config/stalwart:/etc/stalwart:rw" \
  -v "/home/stalwart/data:/var/lib/stalwart:rw" \
  -v "/home/stalwart/logs:/var/log/stalwart:rw" \
  -v "/home/stalwart/secrets/db_name.txt:/run/secrets/db_name:ro" \
  -v "/home/stalwart/secrets/db_user.txt:/run/secrets/db_user:ro" \
  -v "/home/stalwart/secrets/db_password.txt:/run/secrets/db_password:ro" \
  -v "/home/stalwart/secrets/stalwart_recovery_user.txt:/run/secrets/stalwart_recovery_user:ro" \
  -v "/home/stalwart/secrets/stalwart_recovery_password.txt:/run/secrets/stalwart_recovery_password:ro" \
  -p 127.0.0.1:18080:8080 \
  ghcr.io/webstudiobond/stalwart-docker:latest

sudo docker logs -f stalwart_bootstrap
```

### 3. Complete Web Setup Wizard

1. **Access Setup Wizard:** Forward port `18080` over SSH from your workstation (`ssh -N -L 18080:127.0.0.1:18080 user@server`) and navigate to `http://127.0.0.1:18080/admin`.
2. **Authenticate:** Sign in using the credentials from `secrets/stalwart_recovery_user.txt` and `secrets/stalwart_recovery_password.txt`.
3. **Server Identity:** Set **Server Hostname** to your public mail FQDN (e.g., `mail.example.com`) and **Default Email Domain** to your primary domain (e.g., `example.com`). Disable automatic TLS certificate retrieval (configure DNS-01 ACME or external certificates after setup). Enable email signing key generation to auto-generate DKIM keys in the database, or disable if providing external key files via Docker Secrets.
4. **Data Store:** Select `PostgreSQL`. Set **Host** to `/var/run/postgresql`, **Port** to `5432` (used in the UNIX socket filename `.s.PGSQL.5432`), **Database** to the value from `secrets/db_name.txt`, and **Username** to the value from `secrets/db_user.txt`. Select **Secret read from file** for **Password** with path `/run/secrets/db_password`. Disable TLS and allow invalid certs. Set **Max Connections** to `10` (`Fast` recycling).
5. **Attachment & File Storage:** Select `FileSystem`, set path to `/var/lib/stalwart`, and choose **Nested Depth** `2`.
6. **Full-Text Search Index:** Keep the default (`PostgreSQL`).
7. **Cache & Temporary Data:** Select standalone `Redis/Valkey`, set URL to `redis://127.0.0.1:6379`, and keep default timeouts and connection limits.
8. **Account Directory:** Select `Internal`.
9. **Log Destination:** Select `File`, set path to `/var/log/stalwart`, prefix `stalwart.log`, daily rotation, ANSI colors disabled, tracer enabled, and level `Info`. Set **Filtering Mode** to `Exclude the specified events` and exclude noisy background events (`Metric store`, `Metrics collected`, `Metrics pushed`, `Task queued for processing`, `Task scheduled for future execution`, `Search index updated`).
10. **Automatic DNS Management:** Select manual (or configure DNS provider integration later).
11. **Administrator Account:** The wizard automatically generates a default `admin@<domain>` mailbox and displays its password once—record it if you plan to keep the account. Because `admin@<domain>` is a predictable target for brute-force and credential-stuffing attacks, log back in with your `stalwart_recovery_user` credentials after setup, delete the auto-generated `admin@<domain>` account, and create accounts with non-predictable identifiers instead.
12. **Terminate Bootstrap Container:** Stop log following (`Ctrl+C`) and terminate the temporary container:

```bash
sudo docker stop stalwart_bootstrap
```

### 4. Lock Configuration

The bootstrap container must be stopped before locking its writable configuration mount. Make the generated file readable, but not writable, by the service account:

```bash
sudo chown root:stalwart /home/stalwart/config/stalwart/config.json
sudo chmod 0440 /home/stalwart/config/stalwart/config.json
```

</details>

---

<details>
<summary><strong>Operation &amp; Maintenance</strong></summary>

### Starting the Stack

Start all production services in the background:

```bash
sudo docker compose -f /home/stalwart/docker-compose.yaml up -d
```

### Verifying Service Health

Inspect container status, healthcheck states, and resource usage:

```bash
sudo docker compose -f /home/stalwart/docker-compose.yaml ps
sudo docker stats --no-stream stalwart-mail stalwart-db stalwart-cache
```

### Inspecting Runtime Logs

Follow real-time application and database logs:

```bash
# Follow all stack logs
sudo docker compose -f /home/stalwart/docker-compose.yaml logs -f

# Follow mail server logs only
sudo docker compose -f /home/stalwart/docker-compose.yaml logs -f stalwart
```

### Updating the Stack

Pull updated container images and recreate services with minimal downtime:

```bash
sudo docker compose -f /home/stalwart/docker-compose.yaml pull
sudo docker compose -f /home/stalwart/docker-compose.yaml up -d
```

### Stopping & Tearing Down

Gracefully stop running containers without removing them:

```bash
sudo docker compose -f /home/stalwart/docker-compose.yaml stop
```

Stop and remove containers and internal networks (persistent data in `data/`, `postgres/`, and `logs/` is preserved):

```bash
sudo docker compose -f /home/stalwart/docker-compose.yaml down
```

</details>

---

<details>
<summary><strong>Post-Bootstrap Configuration &amp; Recommendations</strong></summary>

### 1. Event Logging & CrowdSec Integration

Stalwart includes a built-in auto-ban and rate-limiting engine. Keeping it active alongside host-level CrowdSec provides defense in depth: CrowdSec tails `/home/stalwart/logs/stalwart.log*` and escalates protocol errors, authentication failures, and Stalwart's own block events into host firewall bans.

To ensure all security-relevant events are written at `Info` level for CrowdSec scenarios, configure **Custom event levels** in WebAdmin (**Settings › Telemetry › Event Levels** → `/account/Settings/x:EventTracingLevel`) with level `Info` for:
* `POP3 error occurred`
* `IMAP error occurred`
* `TLS handshake error`
* `Concurrent request limit reached`
* `Command not implemented`
* `Syntax error`
* `Invalid command`
* `HTTP error occurred`
* `Authentication failed`

### 2. Allowed IP Addresses

In WebAdmin (**Settings › Security › Allowed IPs**), optionally whitelist internal infrastructure addresses to ensure healthcheck probes and host-forwarded administrative sessions are never throttled or blocked by internal rate limiters:

* **`127.0.0.1` (`healthcheck`):** Container loopback used by `/usr/local/bin/probe` (`http://127.0.0.1:8080/healthz/live`).
* **`172.20.0.1` (`frontend_gateway`):** Host bridge gateway IP seen by Stalwart when accessing the published WebAdmin port (`18080:8080`) from the host via SSH port forwarding or a VPN interface.

### 3. DKIM & DKIM2 Signing (`RFC 6376` / `draft-ietf-dkim-dkim2-spec`)

Provisioning keys via Docker Secrets keeps private keys out of the PostgreSQL database (`0440` host files owned by `root:stalwart` mounted on read-only `tmpfs`) and allows publishing strict DNS records (`t=s`) rather than Stalwart's permissive defaults. Because DKIM2 reuses standard `v=DKIM1` `_domainkey` DNS TXT records and has cryptographic domain separation from DKIM1, the same key pairs and selectors can safely sign both protocols simultaneously.

1. **Mount secret files:** Uncomment `dkim_ed25519_key` and `dkim_rsa_key` (and any additional domain keys) under both the `stalwart` service `secrets:` section and the root `secrets:` block in `docker-compose.yaml`, then recreate the container (`sudo docker compose -f /home/stalwart/docker-compose.yaml up -d`).
2. **Extract public keys & publish strict DNS TXT records:**

   ```bash
   # Extract base64 Ed25519 public key
   sudo openssl pkey -in /home/stalwart/secrets/dkim_ed25519.key -pubout -outform DER | tail -c 32 | openssl base64 -A && echo

   # Extract base64 RSA-2048 public key
   sudo openssl rsa -in /home/stalwart/secrets/dkim_rsa.key -pubout -outform DER 2>/dev/null | openssl base64 -A && echo
   ```

   Publish the two TXT records in DNS (`t=s` enforces strict domain alignment in both DKIM1 and DKIM2 by forbidding subdomain signing; `h=sha256; s=email` harden DKIM1 and are ignored by DKIM2 verifiers):

   ```text
   mail-ed25519._domainkey.example.com. IN TXT "v=DKIM1; k=ed25519; h=sha256; s=email; t=s; p=<ED25519_PUBLIC_KEY>"
   mail-rsa._domainkey.example.com.     IN TXT "v=DKIM1; k=rsa; h=sha256; s=email; t=s; p=<RSA_PUBLIC_KEY>"
   ```

   > **Warning:** Strict constraints such as `t=s`, `h=sha256`, and `s=email` (as well as DKIM2 `donotmodify` / `donotexplode` flags below) may not fit every mail topology—for example, `t=s` invalidates signatures if a parent-domain key signs messages for subdomains or uses subdomain `i=` identities. Always review [RFC 6376 § 3.6.1](https://datatracker.ietf.org/doc/html/rfc6376#section-3.6.1) and test verification across your own mail flows before deploying strict parameters in production.

3. **Configure DKIM1 signatures (`RFC 6376` / `RFC 8463`):**
   In WebAdmin (**Management › Domains › DKIM Signatures** → `/account/Management/x:DkimSignature/new`), create dual DKIM1 signatures:
   * **`DKIM1 (Ed25519 SHA-256)`:** Selector `mail-ed25519`, private key set to **Secret read from file** → `/run/secrets/dkim_ed25519_key`.
   * **`DKIM1 (RSA SHA-256)`:** Selector `mail-rsa`, private key set to **Secret read from file** → `/run/secrets/dkim_rsa_key`.
   * Per [RFC 6376 § 5.4.1 (Recommended Signature Content)](https://datatracker.ietf.org/doc/html/rfc6376#section-5.4.1), extend the signed header list on both DKIM1 signatures beyond Stalwart's defaults:

     **Default headers:**
     ```text
     Date
     From
     Message-ID
     Subject
     To
     ```

     **Additional headers to include:**
     ```text
     Cc
     Reply-To
     In-Reply-To
     References
     MIME-Version
     Content-Type
     Content-Transfer-Encoding
     Resent-Date
     Resent-From
     Resent-To
     Resent-Cc
     ```

4. **Configure DKIM2 signatures (`draft-ietf-dkim-dkim2-spec`):**
   In the same **DKIM Signatures** section, create two additional signatures reusing the same selectors and secret files:
   * **`DKIM2 (Ed25519 SHA-256)`:** Selector `mail-ed25519`, private key set to **Secret read from file** → `/run/secrets/dkim_ed25519_key`.
   * **`DKIM2 (RSA SHA-256)`:** Selector `mail-rsa`, private key set to **Secret read from file** → `/run/secrets/dkim_rsa_key`.
   * DKIM2 hashes all non-trace message headers automatically by specification (no manual header list is required). Under **Signing Flags**, enable all three strict delivery constraints:
     * `Request that the message not be modified in transit` (`donotmodify`)
     * `Request that the message not be delivered to more than one recipient` (`donotexplode`)
     * `Request feedback about how the message is handled during delivery` (`feedback`)

### 4. DNS-01 ACME Automation via Docker Secrets

To obtain TLS certificates for mail protocols (`25`, `465`, `993`) via DNS-01 without exposing API credentials in plaintext—for example, if you use Cloudflare as your DNS provider (for other DNS providers, rename or add the corresponding secret files in `docker-compose.yaml`):

1. Uncomment `cf_api_token` in `docker-compose.yaml` and run `sudo docker compose -f /home/stalwart/docker-compose.yaml up -d`.
2. In WebAdmin (**Settings › Server › TLS › ACME Providers**), select **Secret read from file** for the provider token and specify `/run/secrets/cf_api_token`.

### 5. Optional Ports & Private WebAdmin Access

In `docker-compose.yaml`, optional listeners are commented out by default:

* **`${HOST_IP}:587:587` (STARTTLS Submission):** Disabled in favor of Implicit TLS on port `465`. Uncomment only if legacy clients require port `587`.
* **`127.0.0.1:18080:8080` (Private WebAdmin):** Exposes the HTTP management interface on loopback for SSH port forwarding (`ssh -N -L 18080:127.0.0.1:18080 user@server`) when public WebUI access is blocked at the Edge proxy. For direct access over a VPN (e.g., WireGuard), replace `127.0.0.1` with the host's internal VPN interface IP (e.g., `10.0.0.1:18080:8080`).
* **`127.0.0.1:4190:4190` (ManageSieve):** Bound to localhost by default; replace `127.0.0.1` with a private VPN IP or uncomment as needed if desktop mail clients require direct ManageSieve access.

</details>

---

<details>
<summary><strong>External Angie</strong></summary>

## Edge Reverse Proxy (External Angie)

The `stalwart` container joins the external `frontend_gateway` network (`172.20.0.0/16`) without publishing HTTP ports on the host. An external Edge Angie instance terminates HTTPS/TLS, manages HTTP ACME certificates, and restricts public HTTP exposure strictly to required discovery endpoints.

Production-hardened virtual host templates for Stalwart are maintained in the **[angie-docker-compose](https://github.com/webstudiobond/angie-docker-compose)** repository under `data/conf.d/domains/`:

* **[`stalwart.conf`](https://github.com/webstudiobond/angie-docker-compose/blob/main/data/conf.d/domains/stalwart.conf)** — Primary mail host (`mail.example.com`). Exposes client autodiscovery (`/autodiscover/*`, `/.well-known/user-agent-configuration.json`) and JMAP discovery (`/.well-known/jmap`). Public access to the WebUI (`/admin`, `/account`) is locked (`404`) by default, with an optional commented location block if public WebUI access is required.
* **[`autoconfig.conf`](https://github.com/webstudiobond/angie-docker-compose/blob/main/data/conf.d/domains/autoconfig.conf)** — Mozilla Autoconfig virtual host (`autoconfig.example.com`) for Thunderbird and K-9 Mail (`/mail/config-v1.1.xml`, `/.well-known/autoconfig/mail/config-v1.1.xml`, `/.well-known/mail-v1.xml`, `/.well-known/user-agent-configuration.json`).
* **[`autodiscover.conf`](https://github.com/webstudiobond/angie-docker-compose/blob/main/data/conf.d/domains/autodiscover.conf)** — Microsoft Autodiscover virtual host (`autodiscover.example.com`) for Outlook (`/autodiscover/autodiscover.xml`, `/autodiscover/autodiscover.json`, `/.well-known/user-agent-configuration.json`).
* **[`mta-sts.conf`](https://github.com/webstudiobond/angie-docker-compose/blob/main/data/conf.d/domains/mta-sts.conf)** — SMTP MTA-STS policy virtual host (`mta-sts.example`, RFC 8461) exposing `/.well-known/mta-sts.txt`.

Download the required templates into your Edge Angie domains directory:

```bash
DATA_ANGIE="/home/angie/data"
ANGIE_REPO="https://raw.githubusercontent.com/webstudiobond/angie-docker-compose/main/data/conf.d/domains"

for conf in stalwart autoconfig autodiscover mta-sts; do
  curl -fsSL "${ANGIE_REPO}/${conf}.conf" -o "${DATA_ANGIE}/conf.d/domains/${conf}.conf"
done
```

Open each file to customize domain names, upstream definitions, and ACME client identifiers for your environment before reloading Angie:

```bash
nano "${DATA_ANGIE}/conf.d/domains/stalwart.conf"
nano "${DATA_ANGIE}/conf.d/domains/autoconfig.conf"
nano "${DATA_ANGIE}/conf.d/domains/autodiscover.conf"
nano "${DATA_ANGIE}/conf.d/domains/mta-sts.conf"
```

</details>

---

<details>
<summary><strong>Permissions &amp; Ownership</strong></summary>

### Host Permissions & Privilege Separation

The stack enforces strict host-level privilege separation across three ownership tiers so that even in the event of a container compromise (`stalwart`), the process cannot modify deployment manifests, service configurations, or secret files:

* `root:root` (`0600`): `docker-compose.yaml` and `.env` (accessible exclusively to host root).
* `root:stalwart` (`0750`/`0440`): `/home/stalwart` base directory (`0750`), `config/` directories (`0750`), `secrets/` directory (`0750`), configuration files (`config.json`, `valkey.conf` at `0440`), and secret files (`*.txt`, `*.key` at `0440`; read-only to containers, immutable to runtime processes).
* `stalwart:stalwart` (`0700`/`0600`): `data/` (blob storage), `postgres/` (database cluster), and `logs/` (application logs). Managed with strict DAC: directories `0700`, files `0600`.

| Host path | Owner | Mode | Reason |
| --- | --- | --- | --- |
| `/home/stalwart` | `root:stalwart` | `0750` | Allow service traversal without access for other users |
| `docker-compose.yaml`, `.env` | `root:root` | `0600` | Host-only manifests and environment settings |
| `config/` directories | `root:stalwart` | `0750` | Prevent service-account modification |
| `config/stalwart/config.json`, `config/valkey/valkey.conf` | `root:stalwart` | `0440` | Read-only service configuration |
| `secrets/` directory | `root:stalwart` | `0750` | Traverse-only access for the service group |
| `secrets/*` | `root:stalwart` | `0440` | Read-only to the service group |
| `data/`, `postgres/` | `stalwart:stalwart` | `0700` | Private writable runtime storage |
| `logs/` directory | `stalwart:stalwart` | `0700` | Writable by Stalwart, readable by host root CrowdSec |
| `logs/` files | `stalwart:stalwart` | `0600` | Writable by Stalwart, readable by host root CrowdSec |

### One-Shot Permission Reset (Running Stack)

Use this self-contained block to apply or restore canonical ownership and permissions on an existing or migrated installation without stopping the stack:

```bash
sudo chown root:stalwart /home/stalwart
sudo chmod 0750 /home/stalwart

sudo chown root:root /home/stalwart/{docker-compose.yaml,.env}
sudo chmod 0600 /home/stalwart/{docker-compose.yaml,.env}

sudo chown -R root:stalwart /home/stalwart/{config,secrets}
sudo find /home/stalwart/{config,secrets} -type d -exec chmod 0750 {} +
sudo find /home/stalwart/{config,secrets} -type f -exec chmod 0440 {} +

sudo chown -R stalwart:stalwart /home/stalwart/{data,logs,postgres}
sudo find /home/stalwart/{data,logs,postgres} -type d -exec chmod 0700 {} +
sudo find /home/stalwart/{data,logs,postgres} -type f -exec chmod 0600 {} +
```

> **Log Access for Non-Root Services (Optional):** By default, `logs/` is restricted to `0700`. If an external unprivileged service (such as CrowdSec running under a non-root account) requires access to parse application logs, grant read permissions to the directory and log files:

```bash
sudo chmod 0755 /home/stalwart/logs
sudo chmod 0644 /home/stalwart/logs/*
```

</details>

---

<details>
<summary><strong>Client Configuration (Apple Mail Profile)</strong></summary>

## Apple Mail Provisioning (`email.mobileconfig`)

Because Apple Mail on macOS and iOS does not support standard IMAP/SMTP Autoconfig or Autodiscover, use [`examples/email.mobileconfig.example`](examples/email.mobileconfig.example) to provision IMAP (`993`, Implicit TLS) and SMTP (`465`, Implicit TLS) automatically.

By default, the `<key>IncomingPassword</key>` block in the template is commented out so macOS/iOS prompts the user for the password interactively during installation and stores it directly in the Apple Keychain. You may uncomment this block to embed the password inside the profile; however, because `.mobileconfig` files store embedded passwords in plaintext, any profile containing credentials must be transferred strictly over encrypted channels and deleted immediately after installation.

Generate a customized `.mobileconfig` profile (including fresh `PayloadUUID` values via `uuidgen`):

```bash
EMAIL="user@example.com"
MAIL_HOST="mail.example.com"
ORG_DOMAIN="example.com"
UUID_ACCOUNT="$(uuidgen)"
UUID_PROFILE="$(uuidgen)"

curl -fsSL https://raw.githubusercontent.com/webstudiobond/stalwart-docker/main/examples/email.mobileconfig.example \
  -o "${EMAIL}.mobileconfig"

sed -i \
  -e "s|info@example.org|${EMAIL}|g" \
  -e "s|mail.example.com|${MAIL_HOST}|g" \
  -e "s|example.org|${ORG_DOMAIN}|g" \
  -e "s|5E0CFDD5-49CD-45CF-AE90-F0E3140A581C|${UUID_ACCOUNT}|g" \
  -e "s|817768DA-0EE0-4436-87B5-B8ACC2FBCAD6|${UUID_PROFILE}|g" \
  "${EMAIL}.mobileconfig"
```

> TIP:
> * **Always generate unique `PayloadUUID` values:** You must replace both `PayloadUUID` placeholders with freshly generated UUIDs (`uuidgen`) for every profile you create—never reuse the static template UUIDs.
> * By default, the example profile includes only IMAP/SMTP mail settings (`com.apple.mail.managed`). Refer to [Apple's Profile-Specific Payload Keys documentation](https://developer.apple.com/documentation/devicemanagement/profile-specific-payload-keys) if you wish to extend `PayloadContent` with [CalDAV (`com.apple.caldav.account`)](https://developer.apple.com/documentation/devicemanagement/caldav), [CardDAV (`com.apple.carddav.account`)](https://developer.apple.com/documentation/devicemanagement/carddav), or other payloads supported by Stalwart (and ensure the corresponding routes are enabled in Edge Angie).

</details>

---

<details>
<summary><strong>Stalwart CLI</strong></summary>

`stalwart-cli` runs as an on-demand container under `profiles: [tools]` and shares the `stalwart` container's network namespace (`network_mode: "service:stalwart"`).

Authenticate interactively with `--user <ADMIN_USER>` (either a database administrator account or the recovery administrator username when `stalwart_recovery_user` / `stalwart_recovery_password` are enabled in `docker-compose.yaml`); `stalwart-cli` prompts for the password interactively via TTY so credentials are never exposed in shell history, process arguments, or container environment inspection.

Note that `Domain`, `Account`, and `QueueMessage` below are literal JMAP schema object types (not placeholders—`query Domain` lists all configured domains on the server). Because `/home/stalwart/docker-compose.yaml` and `.env` are restricted to `0600 root:root`, standalone commands require `sudo`:

```bash
# List all available configuration object types (or inspect a specific schema, e.g. 'describe Account')
sudo docker compose -f /home/stalwart/docker-compose.yaml run --rm --no-deps stalwart-cli --user <ADMIN_USER> describe

# List all configured mail domains
sudo docker compose -f /home/stalwart/docker-compose.yaml run --rm --no-deps stalwart-cli --user <ADMIN_USER> query Domain

# List all accounts
sudo docker compose -f /home/stalwart/docker-compose.yaml run --rm --no-deps stalwart-cli --user <ADMIN_USER> query Account

# Inspect the outbound mail queue
sudo docker compose -f /home/stalwart/docker-compose.yaml run --rm --no-deps stalwart-cli --user <ADMIN_USER> query QueueMessage
```

> TIP: When the `stalwart` container is already running, `--no-deps` skips dependency checks and executes commands immediately.

### Shell Function for Interactive Sessions

Because `sudo` cannot be embedded inside `.bashrc` functions, this helper is designed for use exclusively under `root` (`sudo -i`). Add the helper function to `/root/.bashrc`:

```bash
stalwart_cli() {
    docker compose -f /home/stalwart/docker-compose.yaml run --rm --no-deps stalwart-cli "$@"
}
```

```bash
source ~/.bashrc
stalwart_cli --user <ADMIN_USER> describe
stalwart_cli --user <ADMIN_USER> query Domain
stalwart_cli --user <ADMIN_USER> query Account
stalwart_cli --user <ADMIN_USER> query QueueMessage
```

</details>

---

<details>
<summary><strong>Development</strong></summary>

For local image building, static analysis, unit testing, and CI/CD pipeline documentation, see **[DEVELOPMENT.md](DEVELOPMENT.md)**.

</details>

---

<details>
<summary><strong>License &amp; Attribution</strong></summary>

## License & Attribution

This repository and deployment architecture are licensed under the [MIT License](LICENSE). This project is an independent containerized deployment architecture and is not affiliated with, endorsed, or sponsored by Stalwart Labs LLC.

**Stalwart License & Ownership:** Stalwart Mail Server is open-source software licensed under the [GNU Affero General Public License v3.0 (AGPLv3)](https://github.com/stalwartlabs/stalwart/blob/main/LICENSE) and developed by [Stalwart Labs LLC](https://stalw.art).

</details>
