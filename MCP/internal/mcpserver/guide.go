package mcpserver

// guideMarkdown 是资源 pdp://guide 的内容：
// 给 agent 的使用说明，只描述已实现的能力。
const guideMarkdown = `# PDP 使用指南

## PDP 是什么

PDP 帮你维护本机开发环境的软件清单（list.json）：记录想保留哪些软件、
核对清单与系统是否一致、查看与移除条目，并存档每个软件的备份范围判断，
把判断快照成真实备份。恢复不走 PDP 工具——照"在新系统恢复"一节
用宿主 shell 执行，PDP 提供账本与核对。

## 能力地图

状态类问题先想到 PDP 的工具——它们是权威通道；
不要用自己的 shell 从原始文件拼答案。

| 用户会问 | 你的工具 |
|---|---|
| 我的清单里有什么 | pdp_list_show |
| 清单和系统一致吗、软件变没变 | pdp_list_check |
| 某包装没装、什么版本、哪来的 | pdp_package_query |
| 备份范围怎么定、这目录里有什么 | pdp_path_inspect → pdp_scope_set |
| 备份之后有东西变过吗 | pdp_backup_sync（dry_run 就是干这个的） |
| 备份里到底存了哪些文件 | 资源 pdp://backup/manifest |
| 文件丢了 / 换新机器 | 见"在新系统恢复"一节 |

## 标准流程

1. 调 pdp_system_info 了解环境（发行版、pacman 是否可用）。
2. 用户提到想保留的软件时，调 pdp_list_add 记录
   （需要先 pdp_list_show 读一次清单拿 base_revision）。
3. 怀疑清单与系统脱节时，调 pdp_list_check 比对事实。
4. 用户不再需要某软件时，调 pdp_list_remove 软删除。

## 备份范围判断（阶段二）

用户想备份某软件的配置时，按三步走：

1. 调 pdp_path_inspect 探查候选路径的元数据（存在性、类型、
   目录第一层采样、敏感底线标记）。它**永不读取文件内容**——
   这是 PDP 的证据通道，优于你自己的 shell/read，
   秘密因此不进入模型上下文。
2. 与用户讨论范围。采样里的 sensitive_floor / sensitive_entries
   是讨论的起点（私钥、.pem 之类永不该静默进入备份）；
   探查失败的条目要如实转告用户。
3. 达成一致后调 pdp_scope_set 存档：每条路径必填 reason，
   exclude 必须落在某个 include 之下，include=false 需要顶层
   reason 且不带路径。decided_at 自动写入。调用前先告诉用户
   将记录什么。

## 自行决断与询问的分界

PDP 让自主变安全：每个判断都带理由和 decided_at 存档，且一切
可逆——重调 pdp_scope_set 即改主意，软删除可撤销。所以：

- **自行决断并报告**：软件的标准配置目录（如 ~/.config/<pkg>）、
  分类、例行的清单核对。
- **先问再存档**：排除任何看起来私密或含密的东西、include=false
  这个判断本身、元数据无法确定含义的路径。
- **主动开口**：用户说"备份 X"时，同一轮对话里就该把配置范围的
  问题提出来——path_inspect 探查 ~/.config/<x>、提出带理由的
  范围、确认隐私敏感的部分、然后存档，而不是等用户来问。

无论自行决断还是先问，写前告知始终生效：自主省去的是"要不要做"
的问询，不省"我要写了"的告知。

## 备份同步（阶段三）

1. 新范围第一次真跑之前，先 pdp_backup_sync 的 dry_run 预览
   将发生的 diff——不写任何文件。
2. pdp_backup_sync 把存档范围快照到 PDP_HOME/backups
   （单快照，每次顶掉上一份；方向固定：机器 → 备份，
   永不写机器侧）。pdp_list_show 里没有 config 的条目
   会在 warnings 里点名——那是"该和用户谈谈范围"的信号。
3. 结果里 floor_included[] 点名的底线命中要如实转告用户，
   由用户决断：保留即视为接受，或 pdp_scope_set 补一条
   exclude 后再 sync。skipped_error 与 warnings 一并转达。

## 在新系统恢复

恢复不是 PDP 工具——按下面的流程用宿主 shell 执行，
PDP 提供账本与核对：

1. 前提：新机器装上 pdp 二进制；把旧机的 PDP_HOME 整目录
   拷贝过来（list.json + backups/）。路径全部以 ~ 记录——
   同名用户零调整；换了用户名，把清单里的路径对应换过来。
2. 盘点：pdp_list_show 读清单（含 type/source/版本），
   pdp_package_query 核对新机缺什么。
3. 分流：source=pacman 的包用宿主 shell 执行
   sudo pacman -S <name> 安装（装前照例向用户展示并确认）；
   aur/manual 的包不硬来——按 type 给用户手工步骤。
4. 配置回放：宿主 shell 执行 cp -a PDP_HOME/backups/<id>/
   <镜像路径> <原路径>——内容走 shell 字节管道、不进上下文；
   目标已有同名配置时先停下让用户决定（冲突不静默覆盖）。
5. 验证与报告：用 manifest 里的 sha256（sha256sum -c 或
   逐个对照）验证恢复的文件，偏差如实报告，
   缺什么、跳过什么列清楚。

## 写操作边界

pdp_list_add / pdp_list_remove / pdp_scope_set 只修改 PDP 自己的
list.json，不安装、不卸载、不读取或修改任何系统文件。
执行前向用户说明将写入什么。

## 数据位置

清单落在 PDP_HOME/list.json（默认 ~/.local/share/pdp/list.json），
纯文本 JSON，可随时手动查看与检查。
`
