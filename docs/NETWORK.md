# SCARLIX OS v12 — Network Topology

## LAN

| Device | IP | Role |
|--------|-----|------|
| Router | 192.168.1.1 | WAN, DHCP, DNS |
| SCARLIX PC (Main) | 192.168.1.100 | AI brain, Buzz, ScarliHQ |
| HP Agent PC | 192.168.1.101 | Coding, Coolify, Nextcloud, Gitea |
| Admin PC | 192.168.1.10 | SSH, browser |
| Family devices | 192.168.1.20-30 | User profiles |

## Headscale VPN (MagicDNS)

| Name | IP | Role |
|------|-----|------|
| scarlix-main | 100.64.0.1 | Main server |
| scarlix-hp | 100.64.0.2 | HP Agent |
| scarlix-admin | 100.64.0.3 | Admin PC |
| scarlix-mob | 100.64.0.4 | Mobile (Termux/OGAM) |
| scarlix-hugo | 100.64.0.5 | Son's laptop |
| scarlix-xox | 100.64.0.6 | Daughter's tablet |
| scarlix-mon | 100.64.0.7 | Wife's laptop |

## Domains (Caddy + Authelia)

| Subdomain | Backend | Port |
|-----------|---------|------|
| scarlix.example.com | ScarliHQ 2D Dashboard | 8090 |
| buzz.scarlix.example.com | Buzz workspace | 3000 |
| hermes.scarlix.example.com | Hermes Agent | 7999 |
| git.scarlix.example.com | Gitea | 3000 |
| coolify.scarlix.example.com | Coolify PaaS | 8000 |
| files.scarlix.example.com | Nextcloud | 8080 |
| photos.scarlix.example.com | Photoprism | 2342 |
| ai.scarlix.example.com | LiteLLM (OpenAI API) | 4000 |
| auth.scarlix.example.com | Authelia SSO | 9091 |
| monitoring.scarlix.example.com | Grafana | 3000 |

## Port Reference

### Main (192.168.1.100 / 100.64.0.1)
| Port | Service |
|------|---------|
| 8090 | ScarliHQ HTTP |
| 8091 | ScarliHQ WS/MCP |
| 3000 | Buzz Relay |
| 7999 | Hermes Agent |
| 30000 | SGLang (GPU 0) |
| 11435 | Ollama (GPU 1) |
| 11438 | llama.cpp (CPU) |
| 4000 | LiteLLM Gateway |
| 8002 | Whisper STT |
| 8003 | Piper TTS |
| 80/443 | Caddy Proxy |
| 9091 | Authelia SSO |
| 8080 | Headscale VPN |

### HP Agent (192.168.1.101 / 100.64.0.2)
| Port | Service |
|------|---------|
| 8001 | OpenCode |
| 3000 | Gitea |
| 8000 | Coolify |
| 8080 | Nextcloud |
| 2342 | Photoprism |
| 5678 | n8n |
