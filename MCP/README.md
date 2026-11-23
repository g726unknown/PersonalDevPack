# PDP MCP Server

PDP（Personal DevPack）是一个 MCP server：帮 agent 维护本机 Linux 开发环境的**软件清单**——记录想保留哪些软件、核对清单与系统是否一致、查看与移除条目。目标环境是 Arch Linux（pacman）。产品背景、边界与阶段规划见 [`../personaldev.md`](../personaldev.md)。

## 构建

```bash
go build -o pdp ./cmd/pdp
```

产物是单文件二进制 `pdp`，无参数、无 CLI——它只作为 stdio MCP server 运行。

## 在 agent 中配置

PDP 走标准 stdio 传输，任何 MCP 客户端都能接。以 opencode 为例（配置文件的 `mcp` 段）：

```json
{
  "mcp": {
    "pdp": {
      "type": "local",
      "command": ["/绝对路径/pdp"],
      "enabled": true
    }
  }
}
```

Claude CLI：

```bash
claude mcp add pdp -- /绝对路径/pdp
```

通用：以 `pdp` 为 stdio 子进程启动即可，无需环境变量（可选设置 `PDP_HOME`）。

## PDP_HOME 与数据位置

状态目录按优先级解析：`PDP_HOME` > `$XDG_DATA_HOME/pdp` > `~/.local/share/pdp`。清单落在其中的 `list.json`——纯文本 JSON，随时可手动查看与检查。

## 安全边界

- PDP **只读写自己的 `list.json`**：不安装、不卸载、不升级任何系统软件，不修改任何配置文件。
- 写操作（`pdp_list_add` / `pdp_list_remove`）需要 `base_revision`（先 `pdp_list_show` 读一次清单），防止并发覆盖。
- 删除是软删除（写 `removed_at` 标记），历史可查。
- 本阶段**不含备份与恢复**；agent 不会替你做这两件事，也不要声称会。

## 开发

```bash
go test ./... -count=1 && go vet ./... && gofmt -l . && go build ./...
```

分层与约定见 [`AGENTS.md`](AGENTS.md)，MCP 契约见 [`DESIGN.md`](DESIGN.md)。
