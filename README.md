# Personal DevPack（PDP）

PDP 是一个 MCP server，帮 agent 维护你的 Linux 开发环境：**记录你想保留哪些软件、判断配置备份范围、把判断快照成真实备份**。目标环境是 Arch Linux（pacman）；接入任何标准 MCP 客户端（opencode、Claude CLI 等）。产品背景、边界与阶段规划见 [`personaldev.md`](personaldev.md)。

## PDP 能做什么（五步故事）

1. **记录软件**：对 agent 说"把 X 记进清单"——`pdp_list_add` 探查版本与来源，落盘可查。
2. **核对**：说"清单还准吗"——`pdp_list_check` 逐项对比记录与系统，只报事实。
3. **定备份范围**：说"备份 X 的配置"——agent 用 `pdp_path_inspect` 取证（只读元数据），与你确认后 `pdp_scope_set` 存档；**每个决定带理由与时间戳**，躺在清单里可审查。
4. **快照**：说"跑一次备份"——`pdp_backup_sync` 把范围复制到 `PDP_HOME/backups/`（单快照、原子替换、SHA-256 账本；`dry_run` 预览变化）。
5. **恢复**：文件丢失或换机器——按 guide 的恢复流程用宿主 shell 执行（`cp -a` 回放 + sha256 验证），PDP 提供账本与核对。

## 工具目录（九个，一行一个"什么时候用它"）

| 工具 | 什么时候用它 |
|---|---|
| `pdp_system_info` | 了解这台机器（发行版、pacman 可用性） |
| `pdp_package_query` | 某包装没装、什么版本、pacman 来的还是 AUR 来的 |
| `pdp_list_add` | "这个软件我要保留"——记录进清单 |
| `pdp_list_remove` | 不再跟踪某软件（软删除，历史可查） |
| `pdp_list_check` | "我的清单还准吗"——逐项核对，只报事实 |
| `pdp_list_show` | 看清单现状，含每个软件的备份范围存档 |
| `pdp_path_inspect` | 定范围前取证：目录里有什么、有没有敏感文件——只读元数据，**永不读内容** |
| `pdp_scope_set` | 存档与用户达成的范围判断（每条路径必带理由） |
| `pdp_backup_sync` | 快照落盘；`dry_run` 同时回答"上次备份之后有东西变过吗" |

## 资源（四份）

| URI | 内容 |
|---|---|
| `pdp://state/inventory` | 清单全量 JSON（含软删条目） |
| `pdp://system/profile` | 系统概览（与 `pdp_system_info` 同源） |
| `pdp://backup/manifest` | 当前快照的账本：每文件路径、SHA-256、来源 revision |
| `pdp://guide` | agent 使用说明与恢复流程 |

## 数据位置与形态

状态目录按优先级解析：`PDP_HOME` > `$XDG_DATA_HOME/pdp` > `~/.local/share/pdp`。全部状态是**纯文本**：`list.json` 是人可读的判断账本；`backups/<时间戳>/` 内是**镜像目录树**——文件管理器打开即可与源逐一对照；`manifest.json` 记录每个文件的哈希。**整个 `PDP_HOME` 整目录拷贝即可在新机器上用**（路径以 `~` 记录，同名用户零调整）。

## 安全边界

- PDP 只写两处自己的领地：`list.json` 与 `PDP_HOME/backups/`。**永不写机器侧任何文件**，不安装、不卸载、不升级任何软件。
- **文件内容永不进入模型上下文**：探查工具只读元数据；备份复制是程序字节管道，进上下文的只有路径与哈希。宿主 agent 自带能力不受此约束。
- 敏感底线（SSH 私钥、`.pem` 等）在元数据层标记：备份时**照常复制但在 `floor_included[]` 点名**，保留或排除由你决断——"永不静默"，而非"永不放行"。
- 删除是软删除（`removed_at` 标记，历史可查）；备份是单快照（每次替换上一份），不是版本历史。

## 在 agent 中配置

任何 MCP 客户端都能接（stdio 传输）。opencode 配置示例：

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

## 构建

```bash
cd MCP && go build -o pdp ./cmd/pdp
```

产物是单文件二进制（`MCP/pdp`），无参数、无 CLI——只作为 stdio MCP server 运行。（Go 的模块住在 `MCP/go.mod`，所以构建必须先进入该目录或用 `go -C MCP build ...`。）

## 开发

```bash
cd MCP && go test ./... -count=1 && go vet ./... && gofmt -l . && go build ./...
```

分层与协作约定见 [`MCP/AGENTS.md`](MCP/AGENTS.md)，MCP 契约见 [`MCP/DESIGN.md`](MCP/DESIGN.md)。
