# MCP 子项目

本目录是 PDP 的 MCP Server 实现。产品定位、边界和开发顺序见上级目录的 `personaldev.md` 与 `AGENTS.md`。MCP 契约见本目录 `DESIGN.md`。

## 协作与教学约定

- 本子项目边做边学 MCP，讲解以教学为主。
- 一次只讲一个概念或一处改动，讲完停下等确认，不串联多个话题。
- 每段代码都要说明：为什么这样写、替代方案、涉及的 Go / MCP 机制。
- 先讲清再落地；涉及多个文件时逐个来。
- 控制篇幅，宁可多轮，也不要一次倾倒。

## 协作方式

- 当前会话负责总体设计、技术决定和 TODO 维护；具体编码由用户或用户另开的实现会话按步骤完成。
- 需要实现时，给出单步任务卡：目标、要写的文件、关键约束、验收方式、明确不做什么。
- 用户完成一步后再给下一步，不在同一轮里排多个实现任务。
- 实现完成后把 `当前进度` 与 `TODO` 同步更新到本文件。

## 已确定的技术决定

- 模块路径 `github.com/g726unknown/pdp`；只有一个 main 包 `cmd/pdp`，产出单个二进制、单进程。
- MCP SDK 用官方 `github.com/modelcontextprotocol/go-sdk` v1.8.0 的 `mcp` 包。
- 传输只做 stdio；HTTP 等需要真实需求再加。
- 日志用 `slog` 显式写 stderr。stdout 是 JSON-RPC 通道，任何输出到 stdout 的内容都是 bug。
- 业务包（`internal/recorder`、`internal/observer`、`internal/executor`、`internal/pathobserver` 等）不得 import `mcp`，用测试扫描 import 守住边界。
- `pathobserver` 与 `observer` 因**内容政策相反而分立**（前者永不读文件内容、后者的命令输出本身就是给 LLM 的证据），不合并。阶段三备份执行器将是又一个独立包：它必须 `Open`/`WriteFile`，进任何一个只读包都会拆牌坊。
- `pathobserver` 是备份范围判断的唯一证据来源，不外包给宿主 agent 的 shell/read：内容边界、敏感底线检查点、裸客户端可移植性都要求证据走 PDP 自己的通道。
- 分层：`mcpserver`（MCP 外壳）→ `executor`（流程与口径：何时探查、conflict 如何映射、warning 措辞）→ `observer`（只读事实，含命令注入接口）+ `recorder`（清单数据的唯一权威：文件原子写，以及字段级规则 `List.Upsert` / `List.MarkRemoved` 纯方法；事实由 executor 以普通 struct 递入，不 import `observer`）。清单落盘为 `PDP_HOME/list.json`。
- 不提供通用 shell 执行工具，只封装具体查询和操作。
- 状态目录由 `PDP_HOME` 决定，默认 `${XDG_DATA_HOME:-~/.local/share}/pdp`；测试一律指向临时目录。
- 工具输出统一信封 `status / summary / data / warnings / manual_steps`；业务结果放 Out 返回，`error` 只表示真实故障。
- 写操作使用 `revision` 乐观并发；不提供 `confirm: true` 之类的布尔确认参数。

## 当前进度

- [x] `go.mod` / `go.sum`，官方 SDK 依赖由 `go mod tidy` 生成
- [x] `cmd/pdp/main.go`：stderr 日志、signal ctx、空 server、stdio 启动
- [x] 手动握手验证：`initialize` 与 `tools/list` 正常，工具列表为空
- [x] `internal/recorder`：结构体 + `Load`（任务卡 #1 完成，2026-11-20）
- [x] `internal/recorder`：`Update` 原子写与 `base_revision`（任务卡 #2 完成，2026-11-21，13 测试全过）
- [x] `internal/recorder`：`List.Upsert` / `MarkRemoved` 纯方法（任务卡 #3 完成，2026-11-21，23 测试全过）
- [x] `internal/observer`：`Execer` + `pacman.go`（卡 #4 + 补丁 #4b 完成，2026-11-21，11 测试 + 真机探针通过；含 locale 教训）
- [x] `internal/observer`：`sys.go` + `testdata`（卡 #5 + 补丁 #5b 完成，2026-11-22，20 测试 + 真机探针通过；假设两层教训：locale + 输出排版）
- [x] `internal/executor`：骨架 + source 翻译 + 写路径用例（卡 #6 + 补丁 #6b 完成，2026-11-23，recorder 24 + observer 21 + executor 14 全过；`ValidType` 导出消灭镜像）
- [x] `internal/executor`：`CheckList` / `ShowList` 读路径（卡 #7 完成，2026-11-23，23 测试全过；含 CheckList 排序采纳）
- [x] `internal/mcpserver`：信封 + harness + `pdp_list_show`（卡 #8 完成，2026-11-23，5 测试全过；协议层首次全链贯通，guardExec 负向守卫）
- [x] `internal/mcpserver`：其余五工具 + 错误映射（卡 #9 完成，2026-11-23，16 测试全过；conflict 附当前 revision、not_found 映射、SDK 字母序定论）
- [x] 资源 + `main` 装配 + README + 真机 e2e（卡 #10 完成，2026-11-23；无状态全链探针 + 真实 agent 会话验收通过，ghostty 落盘）

**阶段一收卡（2026-11-23）**：六工具三资源、90 个测试、真实 agent 会话通过。当前会话亦挂载 pdp server，验收可经协议层回读。

阶段二拍板（2026-11-24）：备份范围判断全交 LLM（reason 必填存档）；**文件内容永不进模型上下文**；敏感硬底线极小集（可查看可修改）；`config` 为可选字段、`version` 保持 1。

阶段三拍板（2026-11-24，同日修订）：**单快照**——每次成功 sync 顶掉上一份（快照是存档判断的投影，不是历史账本）；底线**先报告再决断**——底线文件照常复制（不先决断跳过），非显式条目的命中在 `floor_included[]` 点名，保留或 exclude 后再 sync 由 LLM/用户定；`pdp_backup_sync`（含 dry_run）+ 资源 `pdp://backup/manifest`；symlink 按链接复制、目标内容须显式入 scope。统一原则：**备份忠实执行存档；存档没说清的，报告而不猜测；"确认"永远等于存档里的一个显式条目（带 reason）。**

- [x] 快照引擎（卡 #15 完成，2026-11-24，17 测试 + pathobserver `MatchFloor` 导出；单快照拍板采纳；五处执行语义自定全采纳——含墙重叠去重与深墙先走，"顺序依赖的语义都是坏语义"；包名 snapper：建议 archiver，用户知情保留并留消歧注释）
- [x] executor 接线 + `pdp_backup_sync` + 资源 manifest + 文案校准（卡 #16 **完成**，2026-11-26 收卡——首次真 sync 经协议层执行于真实数据：快照 20261126T081943Z 落盘，manifest 的 sha256 与源文件独立核算逐字一致；wechat 的 NoScopeYet 提醒在真机点亮）
- [x] 恢复改道：guide 恢复节 + instructions 三次校准 + 便携性测试（卡 #17 单测完成，2026-11-26，172 测试；"guide 告诉用户的那条命令真的能用"由 TestPDPHomePortable 钉死）
- [x] **恢复演练完成**（2026-11-26 收官）：真实会话通过；落盘侧核实——单快照替换在真机成立、ghostty 配置哈希与快照一致、fish 作为真实使用新增范围并如实投影（`explicit:false` 语义在真机数据兑现）。**至此 `personaldev.md` MVP 验收全部点亮**（2026-11-26）。
- [x] 能力可见性：README 重写 + guide 能力地图 + instructions 一句（卡 #18 **完成**，2026-11-26 行为验证一次通过：无提示直问"备份有变化吗"→ dry_run 权威通道 + wechat 主动提议定范围 + revision 6 vs 5 的审计级解释；README 构建命令实测坏已修，教训"文档命令验证靠执行"）

- [x] `internal/recorder`：`Scope` 结构 + `SetScope` 纯方法（卡 #11 完成，2026-11-24，recorder 38 测试；`include:false+paths` 拒绝、防御性拷贝采纳；decided_at 待补丁 #11b）
- [x] `internal/recorder`：`Scope.DecidedAt` + `include:false` 空路径校验补全（补丁卡 #11b 完成，2026-11-24，recorder 39 测试全过）
- [x] `internal/pathprobe` → `internal/pathobserver`：元数据探查 + 敏感底线标记（卡 #12 完成，2026-11-24，13 测试 + 真机探针；含包改名采纳、公钥误拦裁定见 #12b）
- [x] `internal/pathobserver`：敏感底线排除 `.pub` + `MTime` 指针化（补丁卡 #12b 完成，2026-11-24，17 测试 + 真机复探收敛为 [id_ed25519]）
- [x] `internal/executor`：`InspectPaths` / `SetScope` 接线（卡 #13 完成，2026-11-24，executor 33 测试；`ErrInvalidScope` 哨兵、SetScope guardExec 负向断言、"断言钉行为不钉措辞"经验）
- [x] `internal/mcpserver`：`pdp_path_inspect` / `pdp_scope_set` 上架 + 文案 + 真机 e2e（卡 #14 **完成**，2026-11-26 收卡——真实数据为证：ghostty 的 scope 于 11-24 由真实 agent 会话落档、带中文 reason 与 decided_at；随后经协议层回读核实）
- [x] `internal/mcpserver`：自主/询问分界引导（补丁卡 #14b 完成，2026-11-24，instructions 两句 + guide 中文增节；安装位制品经协议探针核实）

## 构建与验证

```bash
go build ./...
go vet ./...
go test ./...
```

手动握手（stdin 必须保持打开，否则会话在写回响应前就关闭）：

```bash
go build -o /tmp/pdp ./cmd/pdp
{
  printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"manual","version":"0"}}}'
  sleep 0.2
  printf '%s\n' '{"jsonrpc":"2.0","method":"notifications/initialized"}'
  sleep 0.2
  printf '%s\n' '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'
  sleep 0.5
} | /tmp/pdp
```

正常关闭（stdin EOF）时 `tools/list` 应返回工具列表，进程 exit 0。

无状态探针（2026-07-28 协议，推荐；旧版 `initialize` 握手仍被 SDK 兼容）：

```bash
go build -o /tmp/opencode/pdp ./cmd/pdp
PDP_HOME=$(mktemp -d /tmp/opencode/pdp-probe.XXXX)
M='{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},"io.modelcontextprotocol/clientInfo":{"name":"probe","version":"0"}}'
{
  printf '%s\n' "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"pdp_system_info\",\"arguments\":{},\"_meta\":$M}}"
  sleep 0.4
  printf '%s\n' "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"pdp_list_show\",\"arguments\":{},\"_meta\":$M}}"
  sleep 0.4
  printf '%s\n' "{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"resources/read\",\"params\":{\"uri\":\"pdp://guide\",\"_meta\":$M}}"
  sleep 0.6
} | PDP_HOME=$PDP_HOME /tmp/opencode/pdp 2>/dev/null
```

每次请求的 `_meta` 必须带 `protocolVersion` 与 `clientCapabilities`，缺了直接 `-32602`。`PDP_HOME` 指向临时目录，探针不碰真实数据。

### 假实现测试与真机探针

- stub 编码的是**假设**，且假设藏在多层里，真机各揭一层：locale（环境层，英文嗅探在中文 pacman 上失效）→ `LC_ALL=C` 修复；输出排版（格式层，版本横幅前有 ASCII logo，"取首行"把 logo 带进 Version）→ `pacmanVersionLine` 修复。完整链条见 2026-11-21/22 会话。
- observer 每卡验收 = 单测全绿 + 设计会话在真机跑一条只读探针（临时测试文件，跑完即删，不入库）。探针不是仪式，是这层假设唯一被系统性对照现实的机会。

## TODO

### 阶段一：已完成（2026-11-23）

六工具 + 三资源 + 装配 + 真机 e2e 全部落地并通过验收：`pdp_system_info`、`pdp_package_query`、`pdp_list_add`、`pdp_list_remove`、`pdp_list_check`、`pdp_list_show`，资源 `pdp://state/inventory`、`pdp://system/profile`、`pdp://guide`。清单落盘为 `PDP_HOME/list.json`（默认 `~/.local/share/pdp/`）。分层 `mcpserver → executor → observer + recorder`，90 个测试；无状态全链探针通过，真实 agent 会话（"把 ghostty 加进清单"）验收通过：查询 → 读 revision → 写前告知 → 写入，全部按 `Instructions` 契约执行。

### 后续阶段

- 阶段三（进行中）：`internal/snapper` 快照引擎（唯一可 `Open`/`WriteFile` 用户文件的包）→ `pdp_backup_sync`（含 dry_run）+ 资源 `pdp://backup/manifest`。拍板（2026-11-24，同日修订）：**单快照**，每次 sync 顶掉上一份（快照是投影不是账本）；底线**先报告再决断**（照常复制 + `floor_included[]` 点名，处置交 LLM/用户）；symlink 按链接复制，目标内容须显式入 scope
- `pdp_package_query` 扩展查询（`pacman -Si` / `-Qo`）
- **恢复（改道，2026-11-24）**：不建 PDP 恢复工具——guide 文档化标准流程，执行交给 agent 宿主 shell，宿主审批 UI 充当确认层；PDP 保证 `PDP_HOME` 便携（`~` 路径随用户名，注意事项入 guide）+ 只读工具支持；真实恢复摩擦大则评估补建 `pdp_restore_*`（门不焊死）

### 已知小瑕疵（不阻塞，随手可修）

- ~~`list_show` 的 summary 复数语法~~（卡 #14 已修：`inventory: N active, M removed`）。
- `capabilities` 里仍声明已弃用的 `logging:{}`（SDK 默认，待查关闭方式）。

## 待定问题

- `capabilities` 中 `logging:{}` 能否关闭。
- 备份目录结构与历史保留策略。
