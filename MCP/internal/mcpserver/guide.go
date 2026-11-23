package mcpserver

// guideMarkdown 是资源 pdp://guide 的内容：
// 给 agent 的四段使用说明，只描述已实现的能力。
const guideMarkdown = `# PDP 使用指南

## PDP 是什么

PDP 帮你维护本机开发环境的软件清单（list.json）：记录想保留哪些软件、
核对清单与系统是否一致、查看与移除条目。本阶段只做清单，不含备份与恢复。

## 标准流程

1. 调 pdp_system_info 了解环境（发行版、pacman 是否可用）。
2. 用户提到想保留的软件时，调 pdp_list_add 记录
   （需要先 pdp_list_show 读一次清单拿 base_revision）。
3. 怀疑清单与系统脱节时，调 pdp_list_check 比对事实。
4. 用户不再需要某软件时，调 pdp_list_remove 软删除。

## 写操作边界

pdp_list_add / pdp_list_remove 只修改 PDP 自己的 list.json，
不安装、不卸载、不修改任何系统状态。执行前向用户说明将写入什么。

## 数据位置

清单落在 PDP_HOME/list.json（默认 ~/.local/share/pdp/list.json），
纯文本 JSON，可随时手动查看与检查。
`
