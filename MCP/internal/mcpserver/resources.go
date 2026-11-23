package mcpserver

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerResources 挂三份只读资源（DESIGN 第 6 节）。
func (a *api) registerResources(s *mcp.Server) {
	s.AddResource(&mcp.Resource{
		URI:         "pdp://state/inventory",
		Name:        "inventory",
		Title:       "PDP 软件清单",
		MIMEType:    "application/json",
		Description: "清单全量 JSON，含软删条目",
	}, a.resourceInventory)
	s.AddResource(&mcp.Resource{
		URI:         "pdp://system/profile",
		Name:        "profile",
		Title:       "系统概览",
		MIMEType:    "application/json",
		Description: "pdp_system_info 同源的系统事实（JSON 形态）",
	}, a.resourceProfile)
	s.AddResource(&mcp.Resource{
		URI:         "pdp://guide",
		Name:        "guide",
		Title:       "PDP 使用指南",
		MIMEType:    "text/markdown",
		Description: "四段使用说明：是什么、标准流程、写操作边界、数据位置",
	}, a.resourceGuide)
}

// resourceInventory 返回全量清单。
// 走 Load 而不是 ShowList：资源要全量（含软删条目），
// ShowList 故意丢掉软删——这条差异就是本资源与 pdp_list_show
// 并存的理由（executor.Inventory 的注释互指）。
func (a *api) resourceInventory(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	l, err := a.svc.Inventory(ctx)
	if err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return nil, err
	}
	return resourceText(req, "application/json", string(b)), nil
}

// profileJSON 是 pdp://system/profile 的线上形态：
// 与 system_info 工具同一份映射（systemFacts 共用），不带信封字段。
type profileJSON struct {
	OS             sysOS             `json:"os"`
	PackageManager sysPackageManager `json:"package_manager"`
	User           sysUser           `json:"user"`
	Paths          sysPaths          `json:"paths"`
	Capabilities   []string          `json:"capabilities"`
}

func (a *api) resourceProfile(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	out, err := a.systemFacts(ctx)
	if err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(profileJSON{
		OS:             out.OS,
		PackageManager: out.PackageManager,
		User:           out.User,
		Paths:          out.Paths,
		Capabilities:   out.Capabilities,
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	return resourceText(req, "application/json", string(b)), nil
}

func (a *api) resourceGuide(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	return resourceText(req, "text/markdown", guideMarkdown), nil
}

// resourceText 组装单条文本资源内容，回显请求的 URI。
func resourceText(req *mcp.ReadResourceRequest, mime, text string) *mcp.ReadResourceResult {
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
		URI:      req.Params.URI,
		MIMEType: mime,
		Text:     text,
	}}}
}
