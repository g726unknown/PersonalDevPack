package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/g726unknown/pdp/internal/executor"
)

// registerBackupTools 注册阶段三的工具：pdp_backup_sync（L1）。
// 刻意不设 IdempotentHint：重跑换新快照，是覆盖写不是幂等
// （scope_set 的同款裁定）。
func (a *api) registerBackupTools(s *mcp.Server) {
	mcp.AddTool(s,
		&mcp.Tool{
			Name: "pdp_backup_sync",
			Description: "Snapshot the archived backup scope into PDP_HOME/backups (single snapshot, replaces the previous one). " +
				"Faithful to the archive: include walls minus exclude holes; symlinks copied as links, never followed; " +
				"sensitive-floor hits inside includes ARE copied and reported in floor_included — discuss with the user, then keep or exclude+re-sync. " +
				"dry_run previews the diff without writing. The machine side is never touched. " +
				"This is also the way to answer \"has anything changed since the last backup?\" (dry_run). " +
				"Tell the user what will be synced before the first real call.",
		},
		a.backupSync,
	)
}

type backupSyncIn struct {
	DryRun bool `json:"dry_run,omitempty" jsonschema:"true = show what would change without writing anything"`
}

type backupDiff struct {
	Added    int `json:"added"`
	Modified int `json:"modified"`
	Deleted  int `json:"deleted"`
}

type backupSyncOut struct {
	envelope
	SnapshotID    string     `json:"snapshot_id"` // dry_run 为空
	Diff          backupDiff `json:"diff"`
	FloorIncluded []string   `json:"floor_included"` // 非 nil 空形态，老规矩
	SkippedError  []string   `json:"skipped_error"`
	FilesTotal    int        `json:"files_total"`
}

// handler 三件事：转参 → 调 svc.BackupSync → 包信封。
//
// status 口径（本卡的裁决点）：skipped_error 非空 → partial
// （有想做的没做完）；floor_included、NoScopeYet → ok + warnings
// （做完了，只是有话要说）。
// 本工具没有 rejected/conflict 路径：唯一入参是布尔，revision
// 不参与——快照是存档的投影，不是对存档的写。
// 所有 error 都是真实故障（盘坏、引擎失败）→ IsError。
func (a *api) backupSync(ctx context.Context, _ *mcp.CallToolRequest, in backupSyncIn) (*mcp.CallToolResult, backupSyncOut, error) {
	res, err := a.svc.BackupSync(ctx, executor.SyncInput{DryRun: in.DryRun})
	if err != nil {
		return nil, backupSyncOut{}, err
	}

	snap := res.Snapshot
	out := backupSyncOut{
		SnapshotID:    snap.SnapshotID,
		Diff:          backupDiff{Added: snap.Diff.Added, Modified: snap.Diff.Modified, Deleted: snap.Diff.Deleted},
		FloorIncluded: snap.FloorIncluded,
		SkippedError:  snap.SkippedError,
		FilesTotal:    snap.FilesTotal,
		Warnings:      snap.Warnings,
	}

	switch {
	case len(snap.SkippedError) > 0:
		out.Status = StatusPartial
		out.Summary = fmt.Sprintf("incomplete sync: %d of %d files skipped (see skipped_error)",
			len(snap.SkippedError), snap.FilesTotal+len(snap.SkippedError))
	case in.DryRun:
		out.Status = StatusOK
		out.Summary = fmt.Sprintf("dry-run: would snapshot %d files (+%d/±%d/-%d)",
			snap.FilesTotal, snap.Diff.Added, snap.Diff.Modified, snap.Diff.Deleted)
	default:
		out.Status = StatusOK
		out.Summary = fmt.Sprintf("snapshot %s: %d files (+%d/±%d/-%d)",
			snap.SnapshotID, snap.FilesTotal, snap.Diff.Added, snap.Diff.Modified, snap.Diff.Deleted)
	}
	return nil, out, nil
}
