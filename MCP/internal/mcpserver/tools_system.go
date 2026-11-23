package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerSystemTools 注册 system 域的工具。
func (a *api) registerSystemTools(s *mcp.Server) {
	mcp.AddTool(s,
		&mcp.Tool{
			Name:        "pdp_system_info",
			Description: "Report facts about this machine: OS (id, name, version, kernel, arch), package manager (pacman availability and version), current user and home, PDP state directory, and PDP capabilities. Read-only; does not touch the package list.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		},
		a.systemInfo,
	)
}

type systemInfoIn struct{}

type sysOS struct {
	ID      string `json:"id"`      // os-release 的 ID，如 "arch"
	Name    string `json:"name"`    // PRETTY_NAME
	Version string `json:"version"` // VERSION_ID；滚动发行版为空是事实
	Kernel  string `json:"kernel"`
	Arch    string `json:"arch"`
}

type sysPackageManager struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Available bool   `json:"available"`
}

type sysUser struct {
	Name string `json:"name"`
	Home string `json:"home"`
}

type sysPaths struct {
	StateDir string `json:"state_dir"` // 来自 Config：装配事实，不是观察事实
}

type systemInfoOut struct {
	envelope
	OS             sysOS             `json:"os"`
	PackageManager sysPackageManager `json:"package_manager"`
	User           sysUser           `json:"user"`
	Paths          sysPaths          `json:"paths"`
	Capabilities   []string          `json:"capabilities"` // v1 只报告 pacman 可用性
}

// systemFacts 组装系统事实的线上形态——system_info 工具与
// pdp://system/profile 资源共用同一份映射，翻译只有一处。
func (a *api) systemFacts(ctx context.Context) (systemInfoOut, error) {
	// := 接住 observer.SystemInfo，字段访问不需要 import observer——
	// “外壳只认识执行者”在类型系统上的兑现方式。
	info, err := a.svc.SystemInfo(ctx)
	if err != nil {
		return systemInfoOut{}, err
	}

	caps := []string{} // capabilities 恒非 nil：空是"无可用后端"的事实
	if info.Pacman.Available {
		caps = append(caps, "pacman")
	}

	return systemInfoOut{
		OS: sysOS{
			ID:      info.OSID,
			Name:    info.OSPrettyName,
			Version: info.OSVersionID,
			Kernel:  info.Kernel,
			Arch:    info.Arch,
		},
		PackageManager: sysPackageManager{
			Name:      "pacman",
			Version:   info.Pacman.Version,
			Available: info.Pacman.Available,
		},
		User:         sysUser{Name: info.UserName, Home: info.Home},
		Paths:        sysPaths{StateDir: a.cfg.StateDir},
		Capabilities: caps,
	}, nil
}

// handler 三件事：转参 → 调 systemFacts → 包信封。
// 所有 error 都是真实故障（观察链路坏了）；status 恒 ok。
func (a *api) systemInfo(ctx context.Context, _ *mcp.CallToolRequest, _ systemInfoIn) (*mcp.CallToolResult, systemInfoOut, error) {
	out, err := a.systemFacts(ctx)
	if err != nil {
		return nil, systemInfoOut{}, err
	}

	pacmanWord := "pacman unavailable"
	if out.PackageManager.Available {
		pacmanWord = "pacman available"
	}
	out.Status = StatusOK
	out.Summary = fmt.Sprintf("%s (%s), %s", out.OS.Name, out.OS.ID, pacmanWord)
	return nil, out, nil
}
