# Public deployment guide

[简体中文](public-deployment.md)

This guide assumes a fresh Linux VM, a dedicated hostname, and Docker Compose. Commands target Ubuntu or Debian; adapt package and firewall commands for other distributions.

## 1. Security boundary

The Manager creates and controls browser containers through `/var/run/docker.sock`. Compromise of the Manager is therefore equivalent to host root access.

- Use a dedicated VM and do not colocate databases, CI runners, internal proxies, or other sensitive workloads.
- Never expose the Docker TCP API or share its socket with unrelated containers.
- Limit use to trusted coworkers. This release is not a strong isolation boundary for mutually hostile tenants.
- Use a cloud security group first, then a host firewall and the Docker `DOCKER-USER` chain.
- Encrypt disks and backups, restrict snapshot access, and monitor free disk space.

The defaults allow each instance up to 2 CPUs, 3 GiB of memory, and 1 GiB of shared memory. Size the host and user quotas to avoid overcommit.

## 2. Ports

| Direction | Protocol / port | Purpose | Recommended source |
| --- | --- | --- | --- |
| Inbound | TCP 22 | SSH administration | Fixed administrator IP or VPN only |
| Inbound | TCP 80 | ACME validation and HTTPS redirect | Internet |
| Inbound | TCP 443 | HTTPS and WebSocket | Office/VPN CIDR where possible |
| Inbound | Dynamic UDP range | WebRTC media | Office/VPN CIDR where possible |

Session noVNC, input, audio, and file-service TCP ports are not published on the host. Do not expose them separately.

WebRTC ports are selected from the Linux ephemeral range. After starting the Manager, inspect the actual range:

```bash
cd deploy
docker compose --env-file ../.env exec manager \
  sh -c 'cat /proc/sys/net/ipv4/ip_local_port_range'
```

Allow that range in the cloud firewall and `DOCKER-USER`. If UDP is blocked, the UI can fall back to noVNC, but initial connection waits for WebRTC to time out and audio/video quality is reduced.

## 3. DNS

Create an `A` record such as `browser.example.com` pointing to the server's public IPv4 address. Create an `AAAA` record only after IPv6 listening and firewall rules are configured. Start with DNS-only mode if your provider offers an HTTP/CDN proxy: WebRTC UDP still connects directly to `RB_WEBRTC_ICE_IP`.

```bash
dig +short browser.example.com A
dig +short browser.example.com AAAA
```

## 4. Install Docker and prepare the host

Use Docker's official repository rather than an unknown installation script: <https://docs.docker.com/engine/install/ubuntu/>

```bash
docker version
docker compose version
sudo install -d -m 0750 -o "$USER" -g "$USER" /opt/remotebrowser
sudo install -d -m 0700 -o "$USER" -g "$USER" /srv/remotebrowser/data
git clone https://github.com/robertji666/RemoteBrowser.git /opt/remotebrowser/app
cd /opt/remotebrowser/app
cp .env.example .env
chmod 600 .env
```

Never commit `.env`, data directories, databases, profiles, downloads, or backups.

## 5. Production configuration

Set at least:

```dotenv
RB_ADMIN_PASSWORD=a-unique-password-generated-by-a-password-manager
RB_ADMIN_EMAIL=admin@example.com
RB_SESSION_HOST_DATA_DIR=/srv/remotebrowser/data
RB_SITE_ADDRESS=browser.example.com
RB_PUBLIC_URL=https://browser.example.com
RB_WEBRTC_ICE_IP=203.0.113.10
RB_DEFAULT_INSTANCE_QUOTA=2
```

`RB_SITE_ADDRESS` is the hostname without a path. `RB_PUBLIC_URL` is the complete user-facing HTTPS URL. `RB_WEBRTC_ICE_IP` is a client-reachable public IP, not a hostname or container address. `RB_COOKIE_SECRET` may be left empty so the application creates and persists one; if supplied, use at least 32 random bytes and preserve it across restarts. Leave every `RB_SMTP_*` variable empty when email is disabled.

## 6. Free automatic HTTPS

The deployment uses Caddy. Once DNS is correct, ports 80 and 443 are reachable, and `RB_SITE_ADDRESS` is a hostname, Caddy obtains and renews a publicly trusted ACME certificate and redirects HTTP to HTTPS. Preserve the `caddy_data` volume across upgrades.

See [Caddy automatic HTTPS](https://caddyserver.com/docs/automatic-https) and [Let's Encrypt challenge types](https://letsencrypt.org/docs/challenge-types/). There is normally no need to run Certbot alongside Caddy. Never commit a private key.

## 7. Firewalls

At the cloud provider, allow SSH only from an administrator IP or VPN, TCP 80, TCP 443, and the inspected UDP ephemeral range. Deny all other inbound traffic. Restrict 443 and UDP to office/VPN CIDRs when practical.

Ubuntu UFW example (keep the current SSH session open while applying it):

```bash
sudo ufw default deny incoming
sudo ufw default allow outgoing
sudo ufw allow from YOUR_ADMIN_PUBLIC_IP to any port 22 proto tcp
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw enable
sudo ufw status verbose
```

Docker-published ports can bypass UFW's normal path. Docker documents using `DOCKER-USER` for additional filtering: <https://docs.docker.com/engine/network/packet-filtering-firewalls/>

Find the public interface with `ip route show default`, substitute the real interface and inspected UDP range, and then apply a persistent equivalent of:

```bash
EXT_IF=eth0
UDP_MIN=32768
UDP_MAX=60999

sudo iptables -I DOCKER-USER 1 -i "$EXT_IF" \
  -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
sudo iptables -I DOCKER-USER 2 -i "$EXT_IF" -p tcp \
  -m multiport --dports 80,443 -j ACCEPT
sudo iptables -I DOCKER-USER 3 -i "$EXT_IF" -p udp \
  --dport "$UDP_MIN:$UDP_MAX" -j ACCEPT
sudo iptables -I DOCKER-USER 4 -i "$EXT_IF" -j DROP
sudo iptables -S DOCKER-USER
```

Do not copy placeholder values. Add `-s COMPANY_CIDR` to 443 or UDP rules if access is source-restricted. The final DROP affects other published Docker services, another reason to use a dedicated VM. Configure equivalent IPv6 rules whenever IPv6 is enabled.

## 8. Build and start

```bash
cd /opt/remotebrowser/app
docker build --pull -t remotebrowser/session:latest docker/session
cd deploy
docker compose --env-file ../.env config --quiet
docker compose --env-file ../.env pull caddy
docker compose --env-file ../.env up -d --build
docker compose --env-file ../.env ps
docker compose --env-file ../.env logs --tail=100 caddy manager
```

Verify the certificate and response:

```bash
curl --fail --show-error https://browser.example.com/healthz
```

If certificate issuance fails, inspect DNS, inbound 80/443, firewalls, the Caddy log, and `RB_SITE_ADDRESS`. Do not disable TLS verification as a workaround.

## 9. Acceptance and operations

Before inviting coworkers:

1. Create a normal user and exercise initial password change and sign-in.
2. Create an instance and verify WebRTC, noVNC fallback, repeated audio switching, Chinese input, clipboard, upload, and download.
3. Restart the Manager and host, then verify users, instances, profiles, and downloads persist.
4. Disable or reset a user and confirm old sessions and remote credentials stop working.
5. Use two users to verify instance and file isolation.
6. Scan the public IP from an unauthorized network and verify only intended ports are reachable.

Monitor disk, inodes, CPU, memory, container restarts, and TLS health. This release has no per-user disk quota. Regularly patch the OS, Docker, dependencies, Caddy, Chromium, and base images. Back up the full data directory with SQLite online backup or while the Manager is stopped, and test restoration on an isolated host. A new Session image does not replace existing instance containers; schedule recreation while preserving profile and download mounts.

Before publishing to GitHub, scan the full Git history for secrets, enable GitHub private vulnerability reporting and available Dependabot/secret-scanning features, protect the default branch, and require CI. See [SECURITY.md](../SECURITY.md) for private reporting instructions.
