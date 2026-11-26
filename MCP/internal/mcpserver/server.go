// Package mcpserver 是 MCP 外壳：薄 handler、统一信封、工具注册。
// 本包不出现 os/exec、文件遍历、系统探查——一切能力经 executor
// 组合 observer 与 recorder 完成，handler 只做转参、调用、包信封。
package mcpserver

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/g726unknown/pdp/internal/executor"
)

// Config 是 mcpserver 的装配注入。
type Config struct {
	// StateDir 是 main 解析 PDP_HOME 得到的状态目录；
	// 装配事实不是观察事实，observer 给不了——
	// system_info 的 paths.state_dir 来自这里。
	StateDir string
}

// api 持有 executor 服务与装配配置，全部 handler 挂在它上面。
type api struct {
	svc *executor.Service
	cfg Config
}

// Register 把所有能力挂到 srv 上。注册按 DESIGN 第 5 节的域分组
// （system → package → list），纯粹为了代码组织——
// tools/list 的实际顺序由 SDK 的 featureSet 按工具名字母序输出，
// 注册顺序不影响线上顺序。顺序确定性（客户端缓存的依据）
// 由 SDK 的排序保证：同一组工具任何时刻列出都是同一顺序。
func Register(s *mcp.Server, svc *executor.Service, cfg Config) {
	a := &api{svc: svc, cfg: cfg}
	a.registerSystemTools(s)
	a.registerPackageTools(s)
	a.registerListTools(s)
	a.registerScopeTools(s)
	a.registerBackupTools(s)
	a.registerResources(s)
}
