# Deployment (Oracle Cloud Ubuntu VM)

Every push to `main` triggers [`.github/workflows/deploy.yml`](../.github/workflows/deploy.yml):

1. Runs `go test` (a failing test stops the deployment).
2. Detects the server's CPU (Oracle Ampere = `arm64`, AMD = `amd64`) and builds a matching binary.
3. Uploads it to `/opt/atmos/releases/<timestamp>-<commit>/` over SSH.
4. Points `/opt/atmos/current` at the new release and restarts the `atmos` systemd service.
5. Checks `/api/health`. If the new version doesn't come up, it **rolls back** to the previous release automatically.

Pull requests only run the tests and never deploy.

```
/opt/atmos/
  .env                  config and secrets (only on the server, never in git)
  current -> releases/20261112153000-a1b2c3d
  releases/             the last 5 releases, for rollback
```

---

## One-time setup

### 1. Create a deploy SSH key (on your local machine)

```bash
ssh-keygen -t ed25519 -f ~/.ssh/atmos_deploy -C "github-actions-atmos" -N ""
```

This key is used only by GitHub Actions. Keep using your normal key for your own SSH access.

### 2. Prepare the server

Replace `ubuntu@<SERVER_IP>` with your usual SSH login:

```bash
scp deploy/setup-server.sh deploy/atmos.service ubuntu@<SERVER_IP>:/tmp/
ssh ubuntu@<SERVER_IP> "sudo bash /tmp/setup-server.sh '$(cat ~/.ssh/atmos_deploy.pub)'"
```

Then fill in the real configuration (InfluxDB token, and so on):

```bash
ssh ubuntu@<SERVER_IP>
sudo nano /opt/atmos/.env
```

### 3. Add the GitHub secrets

In the repository, go to **Settings > Secrets and variables > Actions > New repository secret**:

| Secret | Value |
|---|---|
| `SSH_HOST` | Public IP of the VM |
| `SSH_USER` | `deploy` |
| `SSH_PRIVATE_KEY` | Output of `cat ~/.ssh/atmos_deploy` (the **private** key, including the BEGIN/END lines) |
| `SSH_KNOWN_HOSTS` | Output of `ssh-keyscan -t ed25519 <SERVER_IP>` |
| `SSH_PORT` | Optional, only if SSH doesn't use port 22 |

Optional **variable** (same page, "Variables" tab): `APP_PORT`, if you changed `PORT` in `.env` (default `8080`).

Before pasting the `ssh-keyscan` output, check that its fingerprint matches the server
(`ssh ubuntu@<SERVER_IP> ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`).

### 4. Deploy

```bash
git push origin main
```

Follow the run in the repository's **Actions** tab. To redeploy without a new commit, use
**Actions > Test and Deploy > Run workflow**.

---

## HTTPS with a domain (recommended)

The Go app listens on port 8080. Put Caddy in front of it to get automatic HTTPS on 80/443:

1. **Oracle Cloud Console**: go to *Networking > Virtual Cloud Networks > your VCN > Security Lists > Default*
   and add ingress rules for TCP **80** and **443** from `0.0.0.0/0`.
   (`setup-server.sh` already opened them in the VM's own iptables firewall.)
2. Point your domain's DNS **A record** at the server IP.
3. On the server:
   ```bash
   sudo apt install -y caddy        # or see https://caddyserver.com/docs/install
   sudo nano /etc/caddy/Caddyfile   # paste deploy/Caddyfile and set your domain
   sudo systemctl reload caddy
   ```

Don't open port 8080 publicly. Caddy reaches the app over localhost.

---

## Useful commands on the server

```bash
systemctl status atmos                 # is it running?
journalctl -u atmos -f                 # live logs
ls -lt /opt/atmos/releases             # available releases
curl localhost:8080/api/health         # health check
```

**Manual rollback** to an older release:

```bash
sudo -u deploy ln -sfn /opt/atmos/releases/<older-release> /opt/atmos/current
sudo systemctl restart atmos
```

## Notes

- InfluxDB (and Grafana, if you use it) must also run on the VM, or somewhere it can reach.
  Set `INFLUXDB_URL` in `/opt/atmos/.env` to match.
- The `deploy` user can only restart the `atmos` service via sudo, nothing else.
- To stop using a deploy key, delete its line from `/home/deploy/.ssh/authorized_keys` and remove the GitHub secret.
