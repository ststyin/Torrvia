# Torrvia

[English](README.md) | 简体中文

Find what you want to watch.

面向 AI 的轻量影视资源检索服务。跨站搜索、比较资源，再推送到 qBittorrent。

- 并发搜索多个 PT 站点，跨站去重，按做种数与促销权重排序。
- 提供 Web 界面、REST API 和 MCP，支持流式搜索。
- 站点和下载器共用一份 `config.toml`，可在网页设置中修改。
- 单个 Go 二进制，自托管运行。

## 本地运行

需要 Go 1.27.4 或更新版本。

```sh
make build
./torrvia
```

首次启动会生成 `config.toml` 和访问令牌，默认网页地址为 `http://localhost:8080`。
打开网页，填入启动时打印的令牌，在设置中配置站点和下载器。
默认提供演示站点，可先体验搜索流程。

```sh
./torrvia -config /path/to/config.toml
./torrvia -check
./torrvia -token
```

也可用 `TORRVIA_CONFIG` 指定配置文件。配置中包含站点 Cookie 和下载器密码，应保存在本机。

## Docker

先在宿主机启动一次程序生成配置，再运行容器：

```sh
make build
./torrvia -check
docker compose up -d --build
```

配置文件和数据目录分别挂载到 `/config/config.toml` 和 `/data`。
用 `TORRVIA_PORT` 设置宿主机端口，用 `TORRVIA_VERSION` 设置构建版本。

## MCP 接入

Claude Code：

```sh
claude mcp add --transport http torrvia http://localhost:8080/mcp \
  --header "Authorization: Bearer <访问令牌>"
```

Codex（`~/.codex/config.toml`）：

```toml
[mcp_servers.torrvia]
url = "http://localhost:8080/mcp"
http_headers = { Authorization = "Bearer <访问令牌>" }
```

MCP 提供 `search_torrents`、`get_torrent`、`list_sites` 和 `download_torrent` 四个工具。
片名识别、译名转换和资源取舍由调用方 AI 完成。
配套检索技能见 [skills/find-movie/SKILL.md](skills/find-movie/SKILL.md)。

## 开发

```sh
make test
make vet
```
