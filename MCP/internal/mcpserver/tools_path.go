package mcpserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/g726unknown/pdp/internal/executor"
	"github.com/g726unknown/pdp/internal/pathobserver"
	"github.com/g726unknown/pdp/internal/recorder"
)

// registerScopeTools 注册阶段二的两个工具：
// 证据（path_inspect，L0）与存档（scope_set，L1）。
func (a *api) registerScopeTools(s *mcp.Server) {
	mcp.AddTool(s,
		&mcp.Tool{
			Name: "pdp_path_inspect",
			Description: "Read-only filesystem metadata for backup-scope research: existence, kind, size, mtime, mode, first-level directory entries (no recursion), symlink targets (never traversed), sensitive-floor markers. " +
				"NEVER reads file contents — this is PDP's evidence channel; prefer it over your own shell/read for scope research, so secrets stay out of the model context.",
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		},
		a.pathInspect,
	)
	mcp.AddTool(s,
		&mcp.Tool{
			Name: "pdp_scope_set",
			Description: "Archive the backup-scope decision for one package — the result of your conversation with the user. " +
				"Every path entry carries a REQUIRED reason; an exclude must sit under an include; include=false requires a top-level reason and no paths. decided_at is written automatically. " +
				"Tell the user what will be recorded before calling.",
		},
		a.scopeSet,
	)
}

// ---- pdp_path_inspect ----

type pathInspectIn struct {
	Paths      []string `json:"paths"        jsonschema:"absolute paths or ~/..., e.g. [\"~/.config/ghostty\"]"`
	MaxEntries int      `json:"max_entries,omitempty" jsonschema:"optional cap for first-level entry sampling; default 50"`
}

type pathInspectOut struct {
	envelope
	// Entries 直接透传 pathobserver.Facts——Facts 的 json tag
	// 本来就是按线上形态写的（precedent：SystemInfo 直通）。
	Entries []pathobserver.Facts `json:"entries"`
}

// handler 三件事：转参 → 调 svc.InspectPaths → 包信封。
// 注意这个工具没有 IsError 路径：探查本身不可能整体失败——
// 单条失败是 Facts.Error 字段里的事实（权限、断链皆是），
// 唯一的 error 是 rejected（预检拒绝，业务结果）。
// executor 的 warnings 进信封 warnings：LLM 靠这些文字决定
// 要不要就此询问用户。
func (a *api) pathInspect(ctx context.Context, _ *mcp.CallToolRequest, in pathInspectIn) (*mcp.CallToolResult, pathInspectOut, error) {
	res, err := a.svc.InspectPaths(ctx, executor.InspectInput{
		Paths:      in.Paths,
		MaxEntries: in.MaxEntries,
	})
	if err != nil {
		if env, mapped := mapErr(err); mapped {
			return nil, pathInspectOut{envelope: env, Entries: []pathobserver.Facts{}}, nil
		}
		return nil, pathInspectOut{}, err
	}

	out := pathInspectOut{Entries: res.Entries, Warnings: res.Warnings}
	out.Status = StatusOK
	out.Summary = fmt.Sprintf("inspected %d paths, %d warnings", len(res.Entries), len(res.Warnings))
	return nil, out, nil
}

// ---- pdp_scope_set ----

// scopePathIn / scopeIn 是判断的线上形态。不直接用 recorder.Scope：
// 线上不该出现 decided_at——它由 SetScope 在锁内用当前时间写入，
// 调用方填零值留位（toRecorderScope 原样放过）。
type scopePathIn struct {
	Path   string `json:"path"   jsonschema:"absolute or ~/ prefixed"`
	Action string `json:"action" jsonschema:"include | exclude"`
	Reason string `json:"reason" jsonschema:"required — every decision is auditable"`
}

type scopeIn struct {
	Include bool          `json:"include"`
	Reason  string        `json:"reason,omitempty" jsonschema:"required when include=false"`
	Paths   []scopePathIn `json:"paths,omitempty"`
}

type scopeSetIn struct {
	Name         string  `json:"name"`
	Scope        scopeIn `json:"scope"`
	BaseRevision int     `json:"base_revision" jsonschema:"the revision you last read; stale values are rejected"`
}

type scopeSetOut struct {
	envelope
	Action   recorder.Action `json:"action"` // set | not_present
	Revision int             `json:"revision"`
	Scope    recorder.Scope  `json:"scope"` // 值拷贝回显（executor 保证）
}

// toRecorderScope 把线上形态映射为 recorder.Scope。
// DecidedAt 留零值——SetScope 在锁内用 now 写它，调用方无权伪造。
func toRecorderScope(in scopeIn) recorder.Scope {
	paths := make([]recorder.ScopePath, 0, len(in.Paths))
	for _, p := range in.Paths {
		paths = append(paths, recorder.ScopePath{
			Path:   p.Path,
			Action: p.Action,
			Reason: p.Reason,
		})
	}
	return recorder.Scope{
		Include: in.Include,
		Reason:  in.Reason,
		Paths:   paths,
	}
}

// handler 三件事：转参 → 调 svc.SetScope → 包信封。
// 四条 status 路径：ok / conflict（附盘上当前 revision，照抄 list_add）/
// rejected（ErrInvalidInput 或 ErrInvalidScope）/ not_found（not_present
// 是正常返回，映射照抄 list_remove）。
// 不设 IdempotentHint：重调会刷新 decided_at 并落盘 revision——
// 这不是幂等，是覆盖写。
func (a *api) scopeSet(ctx context.Context, _ *mcp.CallToolRequest, in scopeSetIn) (*mcp.CallToolResult, scopeSetOut, error) {
	res, err := a.svc.SetScope(ctx, executor.ScopeInput{
		Name:         in.Name,
		Scope:        toRecorderScope(in.Scope),
		BaseRevision: in.BaseRevision,
	})
	if err != nil {
		env, mapped := mapErr(err)
		if !mapped {
			return nil, scopeSetOut{}, err
		}
		out := scopeSetOut{envelope: env}
		var ce *recorder.ConflictError
		if errors.As(err, &ce) {
			// conflict 附盘上当前 revision：LLM 直接当新 base_revision 用。
			out.Revision = ce.Current
		}
		return nil, out, nil
	}

	out := scopeSetOut{
		Action:   res.Action,
		Revision: res.Revision,
		Scope:    res.Scope,
	}
	if res.Action == recorder.ActionNotPresent {
		out.Status = StatusNotFound
		out.Summary = fmt.Sprintf("%s is not in the list", in.Name)
	} else {
		out.Status = StatusOK
		out.Summary = fmt.Sprintf("%s scope archived (revision %d)", in.Name, res.Revision)
	}
	return nil, out, nil
}
