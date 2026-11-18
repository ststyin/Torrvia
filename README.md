# Torrvia

English | [简体中文](README.zh-CN.md)

Find what you want to watch.

A lightweight, AI-oriented media search service. Search private trackers, compare releases, and send your selection to qBittorrent.

- Search multiple trackers concurrently, deduplicate releases, and rank results by seeders and promotion weight.
- Use the Web UI, REST API, or MCP endpoint, with streaming search results.
- Manage sites and the downloader in one `config.toml`, editable through Web settings.
- Query live qBittorrent tasks and their progress; check for existing releases before submitting downloads.
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

The MCP tools are `search_torrents`, `get_torrent`, `list_sites`, `list_downloads`, and `download_torrent`. The calling AI handles title recognition, translation, and release selection. The companion workflow is in [skills/torrvia/SKILL.md](skills/torrvia/SKILL.md).

## Download status and duplicate checks

`list_downloads` reads current qBittorrent tasks, including completed downloads. It returns task hashes, names, raw states, progress from 0 to 1, byte sizes, speeds in bytes/s, ETA in seconds, and save paths. Optional inputs:

- `query`: search the task name, ignoring case and release-name punctuation. Use original release names; localized aliases are not translated.
- `filter`: `all` (default), `downloading`, `completed`, `active`, `inactive`, `stalled`, or `errored`.
- `hash`: look up a specific task using its downloader hash, rather than a tracker resource ID.
- `limit` / `offset`: paginate matches, newest first; default limit 20, maximum 100. The response includes `total` and `has_more`.

The equivalent authenticated REST endpoint is `GET /api/downloads?q=Fights+Break+Sphere&filter=completed&limit=20&offset=0`.

Before submitting, `download_torrent` and `POST /api/download` check all current tasks for a matching magnet infohash or normalized release name and size (within 8%). An existing match returns `ok: true`, `already_exists: true`, `match`, and `existing` with its live status; no task is submitted. Name-and-size matching is heuristic and can miss renamed releases without a known magnet hash. A failed check returns an error and stops submission.

These queries cover tasks still in qBittorrent, not a permanent download history or a scan of local media files. A task removed from qBittorrent will no longer appear. Progress describes selected files; inspect `state` for errors or missing files even at 100%. A successful new submission confirms acceptance, not completion.

After upgrading the service, restart it and refresh or reconnect your MCP client to discover the new tool.

## Development

```sh
make test
make vet
```
