package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerPackageTools 注册 package 域的工具。
func (a *api) registerPackageTools(s *mcp.Server) {
	mcp.AddTool(s,
		&mcp.Tool{
			Name:        "pdp_package_query",
			Description: "Query the local system (pacman -Q / -Qm) for the given package names. Read-only: does not read or write the PDP list. Returns one entry per requested name plus a convenience list of names that are not installed.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		},
		a.packageQuery,
	)
}

type packageQueryIn struct {
	Names []string `json:"names" jsonschema:"package names to query, e.g. [\"neovim\", \"htop\"]"`
}

type packageQueryEntry struct {
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
	Version   string `json:"version"`
	Source    string `json:"source"`
	Foreign   bool   `json:"foreign"`
}

type packageQueryOut struct {
	envelope
	// Entries 覆盖每个请求的名字；rejected 路径也保持非 nil
	// 空 slice——空形态约定见 executor。
	Entries []packageQueryEntry `json:"entries"`
	Missing []string            `json:"missing,omitempty"` // 未安装名字的便利清单
}

// handler 三件事：转参 → 调 svc.QueryPackages → 包信封。
// rejected（名单含空串）是业务结果；探查错误是真实故障 → IsError。
func (a *api) packageQuery(ctx context.Context, _ *mcp.CallToolRequest, in packageQueryIn) (*mcp.CallToolResult, packageQueryOut, error) {
	entries, missing, err := a.svc.QueryPackages(ctx, in.Names)
	if err != nil {
		if env, mapped := mapErr(err); mapped {
			return nil, packageQueryOut{envelope: env, Entries: []packageQueryEntry{}}, nil
		}
		return nil, packageQueryOut{}, err
	}

	installed := 0
	out := packageQueryOut{
		Entries: make([]packageQueryEntry, 0, len(entries)),
		Missing: missing,
	}
	for _, e := range entries {
		if e.Installed {
			installed++
		}
		out.Entries = append(out.Entries, packageQueryEntry{
			Name:      e.Name,
			Installed: e.Installed,
			Version:   e.Version,
			Source:    e.Source,
			Foreign:   e.Foreign,
		})
	}
	out.Status = StatusOK
	out.Summary = fmt.Sprintf("%d of %d queried packages installed", installed, len(entries))
	return nil, out, nil
}
