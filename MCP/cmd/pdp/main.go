// pdp 是 PDP（Personal DevPack）的 MCP server：
// 维护本机开发环境的软件清单，经 stdio 提供给现有 agent。
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/g726unknown/pdp/internal/executor"
	"github.com/g726unknown/pdp/internal/mcpserver"
	"github.com/g726unknown/pdp/internal/observer"
	"github.com/g726unknown/pdp/internal/pathobserver"
	"github.com/g726unknown/pdp/internal/recorder"
)

const version = "0.1.0-dev"

// instructions 是 initialize 握手时给客户端的服务说明（DESIGN §1 的口径）。
// 只描述已实现的能力——没上线的承诺写进这里就是撒谎。
const instructions = `PDP maintains the software inventory of this Linux machine (list.json): record which packages to keep, check the list against the system, inspect filesystem metadata, and archive backup-scope decisions.
Write operations (pdp_list_add, pdp_list_remove, pdp_scope_set) only modify PDP's own list.json; tell the user what will be written before calling them. PDP never installs, uninstalls, or upgrades anything on the system.
pdp_path_inspect is the evidence channel for backup-scope research: metadata only, never file contents — prefer it over your own shell/read so secrets stay out of the model context.
pdp_scope_set archives the backup-scope decision you reached with the user; every path needs a reason, and decided_at is stamped automatically.
Decide alone vs. ask by reversibility: entries are reversible and every judgment is archived with a reason, so routine calls (standard config paths like ~/.config/<pkg>, classifications) you may decide and report; ask before archiving anything that touches privacy (exclusions, include=false) or when intent is unclear. When the user asks to back up a package, raise the config-scope question yourself in the same conversation (path_inspect → propose → scope_set) instead of waiting to be asked.
Backup execution and restoration are NOT implemented — only scope decisions are archived; do not claim backup/restore capabilities.
Read resources pdp://guide for the standard flow and pdp://state/inventory for the raw list.`

// stateDir 解析状态目录：
// PDP_HOME > ${XDG_DATA_HOME}/pdp > ~/.local/share/pdp。
// 优先级与语义见 AGENTS.md 技术决定。
//
// home 取 getenv("HOME") 而不是 os.UserHomeDir()：后者内部也读环境，
// 但不接受注入——为它单独破坏测试性不值得（参数化注入，老规矩）。
func stateDir(getenv func(string) string) (string, error) {
	if h := getenv("PDP_HOME"); h != "" {
		return h, nil
	}
	if x := getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "pdp"), nil
	}
	if h := getenv("HOME"); h != "" {
		return filepath.Join(h, ".local", "share", "pdp"), nil
	}
	return "", errors.New("cannot determine state dir: set PDP_HOME")
}

func main() {
	// stdout 属于 JSON-RPC 通道，日志必须走 stderr。
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// main 只负责报告退出状态；真正的装配在 run 里，便于测试与复用。
	if err := run(os.Getenv, logger); err != nil {
		logger.Error("server exited", "err", err)
		os.Exit(1)
	}
}

// run 的装配顺序固定：解析状态目录 → 三层组装 → 注册 → stdio 运行。
func run(getenv func(string) string, logger *slog.Logger) error {
	// Ctrl-C 和 SIGTERM 都会取消 ctx，进而让 server 关闭会话并返回。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dir, err := stateDir(getenv)
	if err != nil {
		return err
	}

	rec := recorder.Open(dir)
	// home 与 stateDir 同一注入哲学：装配点读环境、注入各层。
	// HOME 缺席时 ~ 探查按条目报错，不影响其他能力。
	svc := executor.New(observer.New(observer.OSExecer{}), rec, pathobserver.New(getenv("HOME")))
	srv := mcp.NewServer(
		&mcp.Implementation{
			Name:    "pdp",
			Title:   "Personal DevPack",
			Version: version,
		},
		&mcp.ServerOptions{Logger: logger, Instructions: instructions},
	)
	mcpserver.Register(srv, svc, mcpserver.Config{StateDir: dir})

	logger.Info("pdp mcp server starting",
		"version", version, "transport", "stdio", "state_dir", dir)

	// Run 会阻塞到 ctx 取消或对端断开（stdin EOF）。
	err = srv.Run(ctx, &mcp.StdioTransport{})
	if errors.Is(err, context.Canceled) {
		// Ctrl-C / SIGTERM 是正常关闭，不是 ERROR——
		// 阶段一 TODO 里这条欠的账在此结清。
		return nil
	}
	return err
}
