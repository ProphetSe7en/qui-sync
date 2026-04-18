# qui-sync

> **⚠️ EARLY DEVELOPMENT — NOT READY FOR USE**
>
> This application is under active development and **not yet suitable for production use**. Features are incomplete, APIs may change without notice, and data loss is possible. Do not use this with your primary Qui setup without backups.
>
> **For testing only.** If you're interested in trying it out, please reach out first — feedback is welcome, but expect rough edges.

A web UI for managing [Qui](https://github.com/autobrr/qui) automation rules as code.

## What it does

- **Export:** Pull automations from your Qui instances into a versioned git repo. Choose which rules to include, set their priority, and exclude private ones.
- **Sync:** Subscribe to someone else's export repo and apply their rules to your own Qui instance. Your local trackers, enabled state, and intervals are preserved — only the rule logic is updated.
- **Auto-sync:** Optionally keep rules in sync automatically on a schedule. Per-rule opt-in — you choose which rules auto-update and which stay manual.

## Status

| Feature | Status |
|---------|--------|
| Export tab | Working — tested in production |
| Settings tab | Working |
| Sync tab | Code complete — first user testing in progress |
| Auto-sync | Built, not yet tested |
| Push to GHCR | Not yet — local Docker image only |
| Documentation | Minimal — this README + inline help in the UI |

## Running (for testers only)

```bash
docker build -t qui-sync:dev .
docker run -d --name qui-sync \
  -p 6070:6070 \
  -e PUID=99 -e PGID=100 \
  -v /path/to/appdata/qui-sync:/config \
  -v /path/to/qui-automations:/data \
  qui-sync:dev
```

Open `http://<your-ip>:6070` in a browser.

You need a running Qui instance with an API key. Place `config.yml` and `api-key` in the config volume — see `config.example.yml` for the format.

## License

Not yet decided. All rights reserved until a license is chosen.
