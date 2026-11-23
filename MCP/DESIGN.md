# PDP MCP 设计

本文记录当前阶段的 MCP 契约。实现可以调整，但改动这里要先改文档。只记契约（工具、字段、语义），不记实现机制（锁、落盘顺序、错误分类）——机制活在代码注释与测试里，写进本文只会腐化。

## 1. 范围

第一版只回答一个问题：**这台机器上我打算保留哪些软件，清单是否和系统一致。**

包含 6 个工具，不做备份和恢复。

## 2. 协议与身份

| 项 | 决定 |
|---|---|
| 协议 | 2026-07-28（无状态：无 `initialize`，版本在每条请求 `_meta` 中）；SDK v1.8.0 同时兼容旧版 |
| 传输 | stdio |
| capabilities | tools + resources；不声明 sampling、elicitation、roots（规范中已弃用/取代） |
| 身份 | `pdp` / `Personal DevPack` |
| 日志 | 进程日志写 stderr，不使用已弃用的 MCP logging 通知 |

待办：SDK 默认仍在 `capabilities` 中声明 `logging:{}`，需要确认能否关闭。

## 3. 通用约定

- 命名 `pdp_<domain>_<verb>`，domain 目前有 `system`、`package`、`list`。
- 副作用分级：
  - **L0 只读**：直接执行。
  - **L1 只改 PDP 自己的 JSON**：直接执行，由 LLM 事先向用户说明。
  - **L2 改系统**（安装软件、覆盖配置）：必须 `plan` → 用户确认 → `apply`。本阶段没有 L2 工具。
- 写操作单个对象；只读查询可以批量。
- 所有工具返回统一信封：

  ```json
  {"status":"ok|partial|conflict|rejected|not_found","summary":"","data":{},"warnings":[],"manual_steps":[]}
  ```

- Go `error` 只表示真实故障（磁盘错误、JSON 损坏、命令崩溃）。业务结果一律放信封。
- 写工具必须带 `base_revision`；不匹配返回 `conflict` 并附当前 revision。
- `tools/list` 顺序由 SDK 按名字母序输出（featureSet 的 sorted keys），确定性由此保证；注册顺序只是代码组织，不进线上。

## 4. 数据模型

`list.json`（`PDP_HOME/list.json`，即清单本身）：

```json
{
  "version": 1,
  "revision": 7,
  "updated_at": "2026-11-17T09:30:00Z",
  "packages": {
    "neovim": {
      "name": "neovim",
      "source": "pacman",
      "version": "0.11.2",
      "type": "devtool",
      "recorded_at": "2026-11-10T08:00:00Z",
      "checked_at": "2026-11-17T09:30:00Z"
    }
  }
}
```

| 字段 | 含义 | 谁来填 |
|---|---|---|
| `name` | 软件名（pacman 包名） | 调用方 |
| `source` | `pacman` / `aur` / `manual` / `unknown` | PDP 探查；调用方可覆盖 |
| `version` | 最近一次核对到的版本，非实时数据；未安装时为空字符串 | PDP 探查 |
| `type` | `devtool` / `runtime` / `service` / `other` | LLM 判断 |
| `recorded_at` | 首次写入后永不重置，`restored` 同样保持——保留"最初意图"的凭证 | PDP |
| `checked_at` | 最近一次核对并写回的时间 | PDP |
| `removed_at` | 软删除标记；存在即视为已移除 | PDP |

`type` 是**语义分类**，系统无法推导：`devtool` 编辑器与命令行工具、`runtime` 语言工具链、`service` 常驻服务与数据库、`other` 兜底。它决定清单分组，也决定恢复阶段是否需要提示"启用/启动服务"。

来源判断：`pacman -Q` 命中即已安装；同时命中 `pacman -Qm`（foreign）为 `aur`，否则 `pacman`；未安装时默认 `unknown`，调用方可声明 `manual`。`version` 与 `source` 只来自探查，不接受模型编造。

`checked_at` 仅在 `added` / `updated` / `restored` 时刷新（`unchanged` 一个字段都不动）。`list_check` 是只读操作，不写任何字段，避免只读调用导致 `revision` 跳动。

## 5. 工具

### `pdp_system_info`（L0）

- 入参：无
- 出参：`os{id,name,version,kernel,arch}`（`id` 为 os-release 的 ID 如 `arch`，`name` 为 PRETTY_NAME，`version` 为 VERSION_ID、滚动发行版为空是事实）、`package_manager{name,version,available}`、`user{name,home}`、`paths{state_dir}`、`capabilities[]`（v1 只报告 `pacman` 可用性：有则 `["pacman"]`，无则 `[]`）

### `pdp_package_query`（L0，批量）

- 入参：`names[]`
- 行为：只查系统，不读写清单
- 出参：`entries[]{name, installed, version, source, foreign}` 覆盖每个请求的名字；`missing[]` 是其中未安装名字的便利清单，信息不重复
- 同一份逻辑供 `list_add` 内部复用；`source` 翻译（foreign→aur）属于 executor，observer 只报事实

### `pdp_list_add`（L1，单个）

- 入参：`name`（必填）、`type`（必填，枚举）、`source`（可选覆盖）、`base_revision`
- 行为：探查系统 → `List.Upsert`；`recorded_at` 首次写入后永不重置；`checked_at` 仅在 `added` / `updated` / `restored` 时刷新，`unchanged` 不发生任何字段变更；`type` / `version` / `source` 以调用方传入为准（想保持原值就传原值）；曾移除的条目再次 upsert 为 `restored`
- 出参：`action: added|updated|unchanged|restored`、包的最新记录、`revision`
- 未安装：仍然记录，`version` 为空并给 warning
- 失败：`conflict`（revision 不符）、`rejected`（空名、非法类型）

### `pdp_list_remove`（L1，单个）

- 入参：`name`、`base_revision`
- 行为：软删除（写 `removed_at`），不物理删除
- 出参：`action: removed|already_removed|not_present`、`revision`
- `not_present` 时信封 `status` 为 `not_found`

### `pdp_list_check`（L0）

- 入参：无（检查所有未移除的条目）
- 行为：只读，不写清单，只报告事实
- 出参：`revision`、`entries[]{name, recorded_version, installed, actual_version, recorded_source, actual_source}`
- **不做判定**：是否算漂移由 LLM 结合上下文决定（"记录时就没装"与"装了又被删"在事实层面相同）
- 不扫描"系统有但清单没有"的软件

### `pdp_list_show`（L0）

- 入参：无
- 行为：只读本地状态，不碰系统
- 出参：`revision`、`total`、`by_type{}`、`entries[]`、`removed_count`

## 6. 资源

| URI | 内容 | 兜底工具 |
|---|---|---|
| `pdp://state/inventory` | 清单全量 JSON | `pdp_list_show` |
| `pdp://system/profile` | 系统概览 | `pdp_system_info` |
| `pdp://guide` | 简短使用说明 | 无 |

v1 不做资源模板和订阅。

## 7. 明确不做

通用 shell 执行、任意文件读写、整机软件扫描、备份、恢复、`scope_set`、`path_inspect`、`path_peek`。

## 8. 待定

- `capabilities` 中 `logging:{}` 能否关闭。
- pacman 不可用的机器上 `list_add` / `list_check` 的行为（v1 假定目标环境有 pacman，探查失败如实报错）。
- 备份目录结构与历史保留策略。
