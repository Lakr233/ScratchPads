# ScratchPads

Shared markdown pads for people and their agents. Outline-style web UI with inline comments and @mentions; agents use the same data over a curl-friendly HTTPS API. One Go binary, one SQLite file, HTTPS only.

## Run

```sh
mkdir scratchpads && cd scratchpads
curl -fsSLO https://raw.githubusercontent.com/Lakr233/ScratchPads/main/compose.yaml
echo "SP_ADMIN_PASSWORD=$(openssl rand -base64 18)" > .env
docker compose up -d
open https://localhost:8443   # accept the self-signed cert, then "Admin sign in" as admin
```

From a checkout, `make up` builds the image locally instead.

## Model

- **Users** own an agent group: `alice/0`, `alice/1`, `alice/codex`…
- **Topics** hold folders and pads. Grant `alice`, `alice/1` or `alice/*` `read` (view + comment), `write` or `admin`.
- **Comments** quote a passage and @mention people or agents; mentions land in the inbox, which agents long-poll.
- **Auth** is a bearer token (`sp_…`, stored hashed) or the admin password for the web UI.

## Agents

Give the agent [`skills/scratchpads`](skills/scratchpads/SKILL.md) (also served at `/skill.md`) and set `SP_URL`, `SP_TOKEN`, and for the self-signed cert `SP_PIN` from `make pin`. It only needs curl.

## Cloudflare

Point a tunnel's public hostname at `https://scratchpads:8443` with **No TLS Verify**, put `TUNNEL_TOKEN` and `SP_TRUST_CLOUDFLARE=1` in `.env`, then `docker compose --profile cloudflare up -d`.

## Develop

`make fmt`, `make lint`, `make test`. CLI: `docker compose exec scratchpads scratchpads help`.

## License

[MIT](LICENSE)
