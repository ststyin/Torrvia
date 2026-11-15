# Torrvia

English | [简体中文](README.zh-CN.md)

Find what you want to watch.

A lightweight, AI-oriented media search service. Search private trackers, compare releases, and send your selection to qBittorrent.

- Search multiple trackers concurrently, deduplicate releases, and rank results by seeders and promotion weight.
- Use the Web UI, REST API, or MCP endpoint, with streaming search results.
- Manage sites and the downloader in one `config.toml`, editable through Web settings.
- Self-host a single Go binary.

## Run locally

Requires Go 1.27.4 or later.

```sh
make build
./torrvia
```

The first run creates `config.toml` and an access token. Open `http://localhost:8080`, enter the token printed at startup, and configure your sites and downloader in Settings. A demo tracker is included so you can try searching before connecting real sites.

```sh
./torrvia -config /path/to/config.toml
./torrvia -check
./torrvia -token
```

You can also select the configuration file with `TORRVIA_CONFIG`. The file contains tracker cookies and downloader credentials; keep it on your machine.

## Docker

Generate the configuration on the host before starting the container:

```sh
make build
./torrvia -check
docker compose up -d --build
```

The configuration and data directory are mounted at `/config/config.toml` and `/data`. Set `TORRVIA_PORT` to change the host port or `TORRVIA_VERSION` to change the build version.

## MCP setup

Claude Code:

```sh
claude mcp add --transport http torrvia http://localhost:8080/mcp \
  --header "Authorization: Bearer <access-token>"
```

Codex (`~/.codex/config.toml`):

```toml
[mcp_servers.torrvia]
url = "http://localhost:8080/mcp"
http_headers = { Authorization = "Bearer <access-token>" }
```

The four MCP tools are `search_torrents`, `get_torrent`, `list_sites`, and `download_torrent`. The calling AI handles title recognition, translation, and release selection. The companion workflow is in [skills/find-movie/SKILL.md](skills/find-movie/SKILL.md).

## Development

```sh
make test
make vet
```
