# PDP MCP 设计

本文记录当前阶段的 MCP 契约。实现可以调整，但改动这里要先改文档。只记契约（工具、字段、语义），不记实现机制（锁、落盘顺序、错误分类）——机制活在代码注释与测试里，写进本文只会腐化。

## 1. 范围

第一版回答：**这台机器上我打算保留哪些软件，清单是否和系统一致。**（阶段一，已交付）

第二版追加：**每个软件要不要备份配置、备份哪些路径、排除哪些隐私——判断全部交给 LLM 与用户对话完成，PDP 只提供证据（元数据）并存档判断结果（带理由）。**（阶段二）

硬边界（阶段二拍板，永久生效）：**文件内容永远不进入模型上下文**——工具结果、日志、清单文件，哪里都不去。不存在读取文件内容的工具；备份阶段的内容复制只经过程序字节管道。用户对 LLM/提供商不信任的威胁模型在此被承认。

边界属于 PDP 自己的通道：PDP 的任何工具、日志与存档永不携带文件内容；宿主 agent 自带能力（shell、文件读取）不受此约束——PDP 不承诺阻止模型经宿主工具接触内容，只承诺自己永不主动暴露。

第三版追加：**把存档的判断变成真备份**——时间戳快照、哈希 diff、单向同步、失败不伤旧账（阶段三）。

包含 9 个工具（8 旧 + `pdp_backup_sync`）与 4 份资源（增 `pdp://backup/manifest`）；**恢复不以工具形态提供**（改道决策，见 §7）。

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
      "checked_at": "2026-11-17T09:30:00Z",
      "config": {
        "include": true,
        "reason": "user confirmed config is worth keeping",
        "decided_at": "2026-11-24T09:00:00Z",
        "paths": [
          { "path": "~/.config/nvim",        "action": "include", "reason": "main config, user confirmed" },
          { "path": "~/.config/nvim/.env",   "action": "exclude", "reason": "may contain tokens, user chose to drop" }
        ]
      }
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
| `config` | 备份范围判断的存档（见下），**可选字段；缺失 = 尚未讨论过**（区别于 `include:false` 的"明确不备份"） | LLM 与用户对话的结果，`pdp_scope_set` 写入 |

`config`（`Scope`）语义：

- `include`：`true` = 备份 `paths` 中 include 的内容；`false` = 明确不备份，**此时顶层 `reason` 必填且 `paths` 必须为空**——存档存的是决定，不是讨论过程。
- `decided_at`：判断达成的时间，`SetScope` 成功时由 PDP 写入并刷新。可审查性 = 为什么（`reason`）+ 什么时候（`decided_at`）。
- `paths[]`：`action` ∈ `include | exclude`；**每条 `reason` 必填**；`exclude` 必须落在某个 `include` 路径之下（洞必须在墙里）；路径必须绝对或以 `~` 开头。
- 预设的缺席由可审查性补偿：没有内置规则库，安全感来自"每个决定都带理由躺在清单里"。
- 格式兼容：`config` 为新增可选字段，**`version` 保持 1**。升版本保留给不兼容变更（改名、删字段、改语义），本次不触发；既有清单原地有效。

敏感硬底线（用户可查看、可修改；永不静默进入备份）：`~/.ssh/id_*`、`*.pem`、`~/.netrc`、`~/.pgpass`、`~/.aws/credentials`。它在**对话层生效，不在执行层拒绝**：`pdp_path_inspect` 标记命中（`sensitive_floor: true` + warning），不读取内容，不阻止 LLM 表达观点；备份**不做任何过滤决断**，忠实执行存档范围；底线文件照常复制，但非显式条目的命中在 `pdp_backup_sync` 结果的 `floor_included[]` 里**点名**——先报告、再决断：保留即视为接受，或 `scope_set` 补一条 exclude 后下次 sync 将其移除。已知代价如实记录在案：处置前该文件会留在本地快照（最多存活到第 2 次轮转），备份目录在用户自己的 `$PDP_HOME` 下、不出本机。"永不静默"细化为"**永不无报告**"：报告骑在结果里，决断权在 LLM 与用户。

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
- 出参：`revision`、`total`、`by_type{}`、`entries[]`（含 `config` 存档，未配置时字段缺省）、`removed_count`

### `pdp_path_inspect`（L0，批量，阶段二）

- 入参：`paths[]`（绝对路径或 `~` 开头）、`max_entries?`（条目名采样上限，默认 50）
- 行为：**只读元数据，永不读文件内容**。存在性、类型、大小、mtime、权限、目录**第一层**条目名采样（不递归；超出给 `truncated: true` 与总数）；symlink 不穿透——报告链接本身与解析目标，不列目标内容；单条路径探查失败计入该条目，不拖垮整批
- 出参：`entries[]{path, exists, kind, size, mtime, mode, symlink_target, symlink_broken, entries_sample, entries_total, truncated, sensitive_floor, sensitive_entries[], error}`
- 敏感底线：路径自身命中 → `sensitive_floor: true`；采样中的子文件名命中 → 列入 `sensitive_entries[]`；两者都由信封转成 warning。不读取内容，不阻止表达意见
- 用途：LLM 验证配置位置推测、判断隐私风险的唯一证据来源
- 失败：`rejected`（相对路径、空路径，executor 预检）

### `pdp_scope_set`（L1，单个，阶段二）

- 入参：`name`、`scope{include, reason, paths[]{path, action, reason}}`、`base_revision`
- 行为：`List.SetScope` 纯方法写入；校验：`reason` 逐条必填、`include:false` 时顶层 `reason` 必填且 `paths` 必须为空、`exclude` 必须落在 `include` 之下、路径必须绝对或 `~` 开头；`decided_at` 由 PDP 写入，调用方不填；条目不存在 → `not_found`；条目已软删 → `rejected`（先恢复再谈范围）
- 出参：`action: set`、包的最新 `config`、`revision`
- 语义：这是"LLM 与用户对话结果"的存档点——PDP 不判断内容，只存判断

### `pdp_backup_sync`（L1，阶段三）

- 入参：`dry_run`（默认 `false`；`true` 只展示将发生的 diff，不写任何文件）
- 行为：读取清单中全部 `config.include:true` 条目 → 墙内减洞 → 复制到 `PDP_HOME/backups/<UTC 时间戳>/`；快照**完整构建成功后**才替换旧快照（单快照：每次成功 sync 顶掉上一份——快照是存档判断的投影，不是历史账本；要历史让 LLM 维护多份清单或多 `PDP_HOME`），失败或中断丢弃整个新目录、现有快照不受影响
- `manifest.json`：人可读 JSON——每个文件的路径、SHA-256、大小、mtime、symlink 目标，以及来源清单的 `revision`
- diff：机器 ↔ 上一次快照逐文件比对，区分 added / modified / deleted
- symlink：按链接本身复制（存目标字符串，永不跟随目标内容）；目标内容若也要备份，目标路径须在 scope 里显式 include（正常流程，先 `pdp_path_inspect`）
- 底线文件：见 §4——**照常复制、不先决断**；非显式条目的命中进 `floor_included[]` 点名报告，保留或 exclude 后再 sync 由 LLM/用户定
- 出参：`snapshot_id`（dry_run 为空）、`diff{added, modified, deleted}`、`floor_included[]`（本次复制的、非显式条目的底线命中）、`skipped_error[]`（读不了的文件如实点名；非空时信封 `status` 为 `partial`——有想做的没做完）、`warnings` / `manual_steps`
- 范围外提示：清单里 `config` 为 nil（从未讨论过）的未移除条目不进备份，逐个给 warning 提醒 LLM 主动提起；`include:false` 是明确决定，不打扰
- 同步方向永久固定：机器 → 备份；PDP 永不写机器侧

## 6. 资源

| URI | 内容 | 兜底工具 |
|---|---|---|
| `pdp://state/inventory` | 清单全量 JSON | `pdp_list_show` |
| `pdp://system/profile` | 系统概览 | `pdp_system_info` |
| `pdp://backup/manifest` | 当前快照的 manifest JSON | 无（diff 走 `pdp_backup_sync` 的 dry_run） |
| `pdp://guide` | 简短使用说明 | 无 |

v1 不做资源模板和订阅。

## 7. 明确不做

通用 shell 执行、整机软件扫描、**恢复工具**（改道决策 2026-11-24：恢复流程由 guide 文档化，执行交给 agent 的宿主 shell——`cp`/`pacman -S` 走宿主字节管道、内容不进上下文，宿主的命令审批 UI 就是 L2 确认层；PDP 提供便携真相源与只读工具。若真实恢复摩擦大，`pdp_restore_*` 可补建，门不焊死）、**任何把文件内容送进模型上下文的工具**（永久边界，见 §1 硬边界；备份复制是程序字节管道——路径与哈希进上下文，内容不进）。

## 8. 待定

- `capabilities` 中 `logging:{}` 能否关闭。
- pacman 不可用的机器上 `list_add` / `list_check` 的行为（v1 假定目标环境有 pacman，探查失败如实报错）。
