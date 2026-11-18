# Torrvia

[English](README.md) | 简体中文

Find what you want to watch.

面向 AI 的轻量影视资源检索服务。跨站搜索、比较资源，再推送到 qBittorrent。

- 并发搜索多个 PT 站点，跨站去重，按做种数与促销权重排序。
- 提供 Web 界面、REST API 和 MCP，支持流式搜索。
- 站点和下载器共用一份 `config.toml`，可在网页设置中修改。
- 查询 qBittorrent 的当前任务和下载进度，提交前检查已有资源。
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

先创建配置目录。升级旧版时，先停止旧容器，再将宿主机上的配置复制到新目录。以下命令会保留目标目录已有的配置和原有的 `./data` 数据。

```sh
docker compose stop torrvia
mkdir -p -m 700 config
if [ -f config.toml ]; then cp -np config.toml config/config.toml; fi
docker compose up -d --build
docker compose exec torrvia /torrvia -token
```

首次安装时，Torrvia 会在启动时生成 `./config/config.toml`。Compose 将整个 `./config/` 挂载到 `/config`，以便网页设置可以原子替换配置文件。数据目录 `./data` 挂载到 `/data`。用 `TORRVIA_PORT` 设置宿主机端口，用 `TORRVIA_VERSION` 设置构建版本。

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

MCP 提供 `search_torrents`、`get_torrent`、`list_sites`、`list_downloads` 和 `download_torrent` 五个工具。
片名识别、译名转换和资源取舍由调用方 AI 完成。
配套检索技能见 [skills/torrvia/SKILL.md](skills/torrvia/SKILL.md)。

## 下载状态和查重

`list_downloads` 读取 qBittorrent 的当前任务，包括仍保留在下载器中的已完成任务。返回任务哈希、名称、原始状态、0～1 的进度、字节数、下载和上传速度（字节/秒）、预计剩余秒数及保存路径。可选参数：

- `query`：按任务名搜索，忽略大小写和发布名中的标点；应使用原始发布名，不自动转换中文别名。
- `filter`：`all`（默认）、`downloading`、`completed`、`active`、`inactive`、`stalled` 或 `errored`。
- `hash`：使用下载器的任务哈希查询单个任务，与 PT 站点资源 ID 不同。
- `limit` / `offset`：按添加时间从新到旧分页；默认每页 20 条，最多 100 条。返回 `total` 和 `has_more`。

对应的 REST 接口为 `GET /api/downloads?q=Fights+Break+Sphere&filter=completed&limit=20&offset=0`，沿用现有访问令牌认证。

`download_torrent` 和 `POST /api/download` 提交前会检查全部当前任务，按磁力链接中的信息哈希，或规范化后的发布名与体积（相差不超过 8%）匹配。已有任务时返回 `ok: true`、`already_exists: true`、匹配方式 `match` 及任务状态 `existing`，跳过提交。名称和体积匹配属于启发式判断；如果任务已改名且没有已知磁力哈希，可能无法识别。查询失败会报错并停止提交。

查询范围是仍保留在 qBittorrent 的任务，不是永久下载历史或本地媒体目录扫描。已从下载器删除的任务不会出现。进度针对选中的文件；即使达到 100%，也应检查是否存在错误或文件丢失。新任务提交成功只代表下载器已接收，不代表下载完成。

升级服务后需重启，并刷新或重新连接 MCP 客户端，才能发现新工具。

## 开发

```sh
make test
make vet
```
