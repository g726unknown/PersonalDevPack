package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/g726unknown/pdp/internal/executor"
	"github.com/g726unknown/pdp/internal/recorder"
)

// registerListTools 注册 list 域的工具，
// 域内顺序固定：add → remove → check → show。
func (a *api) registerListTools(s *mcp.Server) {
	mcp.AddTool(s,
		&mcp.Tool{
			Name:        "pdp_list_add",
			Description: "Record one package in the PDP list (list.json). Probes pacman for version/source, then upserts the entry; the list's revision is incremented. Requires base_revision from your last read of the list; stale values are rejected as conflict. Re-adding a soft-removed package restores it.",
			Annotations: &mcp.ToolAnnotations{IdempotentHint: true},
		},
		a.listAdd,
	)
	mcp.AddTool(s,
		&mcp.Tool{
			Name:        "pdp_list_remove",
			Description: "Soft-remove one package from the PDP list (writes removed_at; entries are never physically deleted). Requires base_revision from your last read of the list.",
			Annotations: &mcp.ToolAnnotations{IdempotentHint: true},
		},
		a.listRemove,
	)
	mcp.AddTool(s,
		&mcp.Tool{
			Name:        "pdp_list_check",
			Description: "Compare the PDP list against the local system for every non-removed entry. Read-only, reports facts only (recorded vs actual version and source); it does not judge whether a difference is drift — you decide with context. Skips soft-removed entries. Use this for \"is my list up to date?\" questions.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		},
		a.listCheck,
	)
	mcp.AddTool(s,
		&mcp.Tool{
			Name:        "pdp_list_show",
			Description: "Show the recorded PDP software inventory (list.json) without touching the system. Entries are sorted by name and exclude soft-removed packages; removed_count reports how many were soft-removed.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		},
		a.listShow,
	)
}

// listShowIn：list_show 无参数。空结构体保证输入 schema 是 object。
type listShowIn struct{}

// listShowEntry 是条目的线上形态；时间字段由 encoding/json
// 自动输出 RFC3339。
type listShowEntry struct {
	Name       string          `json:"name"`
	Source     string          `json:"source"`
	Version    string          `json:"version"`
	Type       string          `json:"type"`
	RecordedAt time.Time       `json:"recorded_at"`
	CheckedAt  time.Time       `json:"checked_at"`
	Config     *recorder.Scope `json:"config,omitempty"` // nil = 尚未讨论过
}

// listShowOut 是 pdp_list_show 的输出：信封平铺 + 业务字段。
type listShowOut struct {
	envelope
	Revision     int             `json:"revision"`
	Total        int             `json:"total"`
	ByType       map[string]int  `json:"by_type"`
	Entries      []listShowEntry `json:"entries"`
	RemovedCount int             `json:"removed_count"`
}

// handler 只做三件事，不许更多：转参 → 调 svc.ShowList → 包信封。
// 对 list_show 而言所有 error 都是真实故障（盘坏了）——
// 没有业务 status 分支；错误即 return，SDK 会置 IsError 并把
// 错误文本放进 Content，LLM 能看到并自行纠正。
func (a *api) listShow(ctx context.Context, _ *mcp.CallToolRequest, _ listShowIn) (*mcp.CallToolResult, listShowOut, error) {
	res, err := a.svc.ShowList(ctx)
	if err != nil {
		return nil, listShowOut{}, err
	}

	out := listShowOut{
		Revision:     res.Revision,
		Total:        res.Total,
		ByType:       res.ByType,
		Entries:      make([]listShowEntry, 0, len(res.Entries)),
		RemovedCount: res.RemovedCount,
	}
	for _, e := range res.Entries {
		out.Entries = append(out.Entries, listShowEntry{
			Name:       e.Name,
			Source:     e.Source,
			Version:    e.Version,
			Type:       e.Type,
			RecordedAt: e.RecordedAt,
			CheckedAt:  e.CheckedAt,
			Config:     e.Config,
		})
	}
	out.Status = StatusOK
	// 数字前移绕开单复数（瑕疵清单第一条）：英文单复数在 1 上翻车，
	// "inventory: N active" 对任何 N 都不翻车。
	out.Summary = fmt.Sprintf("inventory: %d active, %d removed", res.Total, res.RemovedCount)
	return nil, out, nil
}

// ---- pdp_list_add ----

type listAddIn struct {
	Name         string `json:"name"             jsonschema:"pacman package name, e.g. neovim"`
	Type         string `json:"type"             jsonschema:"classification: devtool | runtime | service | other"`
	Source       string `json:"source,omitempty" jsonschema:"optional override: pacman | aur | manual | unknown; leave empty for auto-detection"`
	BaseRevision int    `json:"base_revision"    jsonschema:"the revision you last read (pdp_list_show); stale values are rejected"`
}

type listAddOut struct {
	envelope
	Action   string        `json:"action"` // added | updated | unchanged | restored
	Revision int           `json:"revision"`
	Package  listShowEntry `json:"package"`
}

// 四条 status 路径在这个 handler 交汇：
// ok（四种 action）/ conflict（附当前 revision）/ rejected / IsError。
func (a *api) listAdd(ctx context.Context, _ *mcp.CallToolRequest, in listAddIn) (*mcp.CallToolResult, listAddOut, error) {
	res, err := a.svc.AddPackage(ctx, executor.AddInput{
		Name:         in.Name,
		Type:         in.Type,
		Source:       in.Source,
		BaseRevision: in.BaseRevision,
	})
	if err != nil {
		env, mapped := mapErr(err)
		if !mapped {
			// 探查失败或盘故障：真实故障 → IsError。
			return nil, listAddOut{}, err
		}
		out := listAddOut{envelope: env}
		var ce *recorder.ConflictError
		if errors.As(err, &ce) {
			// conflict 附盘上当前 revision：LLM 拿到就能直接当新的
			// base_revision 用，省一次 list_show。
			out.Revision = ce.Current
		}
		return nil, out, nil
	}

	out := listAddOut{
		Action:   string(res.Action),
		Revision: res.Revision,
		Package: listShowEntry{
			Name:       res.Package.Name,
			Source:     res.Package.Source,
			Version:    res.Package.Version,
			Type:       res.Package.Type,
			RecordedAt: res.Package.RecordedAt,
			CheckedAt:  res.Package.CheckedAt,
		},
	}
	if res.Warning != "" {
		out.Warnings = []string{res.Warning}
	}
	out.Status = StatusOK
	out.Summary = fmt.Sprintf("%s %s (revision %d)", in.Name, res.Action, res.Revision)
	return nil, out, nil
}

// ---- pdp_list_remove ----

type listRemoveIn struct {
	Name         string `json:"name"          jsonschema:"pacman package name to remove from the list"`
	BaseRevision int    `json:"base_revision" jsonschema:"the revision you last read (pdp_list_show); stale values are rejected"`
}

type listRemoveOut struct {
	envelope
	Action   string `json:"action"` // removed | already_removed | not_present
	Revision int    `json:"revision"`
}

// not_present 是 handler 层的映射：executor 返回的是正常结果（无 error）。
// 这是信封 not_found 的唯一来源。
func (a *api) listRemove(ctx context.Context, _ *mcp.CallToolRequest, in listRemoveIn) (*mcp.CallToolResult, listRemoveOut, error) {
	res, err := a.svc.RemovePackage(ctx, executor.RemoveInput{
		Name:         in.Name,
		BaseRevision: in.BaseRevision,
	})
	if err != nil {
		env, mapped := mapErr(err)
		if !mapped {
			return nil, listRemoveOut{}, err
		}
		out := listRemoveOut{envelope: env}
		var ce *recorder.ConflictError
		if errors.As(err, &ce) {
			out.Revision = ce.Current
		}
		return nil, out, nil
	}

	out := listRemoveOut{
		Action:   string(res.Action),
		Revision: res.Revision,
	}
	if res.Action == recorder.ActionNotPresent {
		out.Status = StatusNotFound
		out.Summary = fmt.Sprintf("%s is not in the list", in.Name)
	} else {
		out.Status = StatusOK
		out.Summary = fmt.Sprintf("%s %s (revision %d)", in.Name, res.Action, res.Revision)
	}
	return nil, out, nil
}

// ---- pdp_list_check ----

type listCheckIn struct{}

type listCheckEntry struct {
	Name            string `json:"name"`
	RecordedVersion string `json:"recorded_version"`
	Installed       bool   `json:"installed"`
	ActualVersion   string `json:"actual_version"`
	RecordedSource  string `json:"recorded_source"`
	ActualSource    string `json:"actual_source"`
}

type listCheckOut struct {
	envelope
	Revision int              `json:"revision"`
	Entries  []listCheckEntry `json:"entries"`
}

// 只报事实，无判定字段——漂移与否由 LLM 结合上下文决定（DESIGN 铁则）。
// status 恒 ok；探查与 Load 错误是真实故障 → IsError。
func (a *api) listCheck(ctx context.Context, _ *mcp.CallToolRequest, _ listCheckIn) (*mcp.CallToolResult, listCheckOut, error) {
	res, err := a.svc.CheckList(ctx)
	if err != nil {
		return nil, listCheckOut{}, err
	}

	installed := 0
	out := listCheckOut{
		Revision: res.Revision,
		Entries:  make([]listCheckEntry, 0, len(res.Entries)),
	}
	for _, e := range res.Entries {
		if e.Installed {
			installed++
		}
		out.Entries = append(out.Entries, listCheckEntry{
			Name:            e.Name,
			RecordedVersion: e.RecordedVersion,
			Installed:       e.Installed,
			ActualVersion:   e.ActualVersion,
			RecordedSource:  e.RecordedSource,
			ActualSource:    e.ActualSource,
		})
	}
	out.Status = StatusOK
	out.Summary = fmt.Sprintf("%d entries checked, %d installed", len(res.Entries), installed)
	return nil, out, nil
}
