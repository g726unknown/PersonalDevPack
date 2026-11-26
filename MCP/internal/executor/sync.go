package executor

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/g726unknown/pdp/internal/recorder"
	"github.com/g726unknown/pdp/internal/snapper"
)

// SyncInput 是 BackupSync 的调用参数。
type SyncInput struct{ DryRun bool }

// SyncResult 是 BackupSync 的输出。
// Snapshot 透传 snapper.Result（Facts / Inventory 直通的先例——
// 引擎结果本身就是给信封消费的形态）。
// NoScopeYet 保留结构化名单；同时已折进 Snapshot.Warnings
// （口径职责归 executor：提醒 LLM 该提起范围话题）。
type SyncResult struct {
	Snapshot   snapper.Result
	NoScopeYet []string // 从未讨论过范围的未移除条目
}

// BackupSync 把存档的备份范围判断快照到 backups——判断的消费。
//
// 它不探查（guardExec 负向断言的家族）：读的是存档，不是系统。
// plan 翻译的三条规则（#11 两态语义在执行侧的延续）：
//
//	Config == nil     → 不进 plan，进 NoScopeYet——nil 的语义就是
//	                     “LLM 该提起”，sync 是第二个提醒点；
//	Include == false  → 静默跳过——明确决定不打扰；
//	Include == true   → plan：scope 条目全都是显式（非显式只来自
//	                     引擎的目录展开）；~ 按注入的 home 展开。
//
// 错误语义：Load 失败、引擎失败 → 原样上抛（真实故障）；
// 引擎内部的保守语义（洞优先、源缺失、skipped_error）已就位，
// 本层不加翻译。
func (s *Service) BackupSync(ctx context.Context, in SyncInput) (SyncResult, error) {
	l, err := s.rec.Load(ctx)
	if err != nil {
		return SyncResult{}, err
	}

	plan := []snapper.PkgPlan{}
	noScope := []string{}
	translateWarns := []string{}
	for name, p := range l.Packages {
		if p.RemovedAt != nil {
			continue
		}
		switch {
		case p.Config == nil:
			noScope = append(noScope, name)
		case !p.Config.Include:
			// 明确不备份：静默——明确决定不重复打扰（#11 两态）。
		default:
			pkg, warns, err := s.translateScope(name, p.Config)
			if err != nil {
				return SyncResult{}, err
			}
			// 展不开的条目（手改文件破坏了落档校验）跳过并 warning——
			// 报告而不猜测。
			translateWarns = append(translateWarns, warns...)
			if len(pkg.Includes) > 0 {
				plan = append(plan, pkg)
			}
		}
	}
	slices.Sort(noScope) // map 遍历随机化，报告要确定性

	res, err := s.snap.Sync(ctx, plan, l.Revision, time.Now(), in.DryRun)
	if err != nil {
		return SyncResult{}, err
	}
	if len(noScope) > 0 {
		res.Warnings = append(res.Warnings,
			fmt.Sprintf("no backup scope discussed yet for: %s", strings.Join(noScope, ", ")))
	}
	res.Warnings = append(res.Warnings, translateWarns...)
	return SyncResult{Snapshot: res, NoScopeYet: noScope}, nil
}

// BackupManifest 透传快照引擎的当前账本。
// 资源 pdp://backup/manifest 的底料；ErrNoSnapshot 由资源层映射为
// “资源不存在”（首次 sync 前的合法状态）。
func (s *Service) BackupManifest(ctx context.Context) (*snapper.Manifest, error) {
	return s.snap.Current(ctx)
}

// expandScopePath 把存档路径展开为绝对路径。scope 落档时已校验
// （绝对或 ~ 开头）；手改文件可能破坏该约定——展开不了的条目
// 由调用方记 warning 跳过，报告而不猜测。
func (s *Service) expandScopePath(path string) (string, error) {
	switch {
	case path == "~":
		return s.home, nil
	case strings.HasPrefix(path, "~/"):
		return filepath.Join(s.home, path[2:]), nil
	case filepath.IsAbs(path):
		return path, nil
	}
	return "", fmt.Errorf("scope path %q is neither absolute nor ~-prefixed (hand-edited list?)", path)
}

// translateScope 把一个已确认的 Scope 翻译为快照计划。
// include/exclude 全部按注入的 home 展开 ~；scope 条目一律显式。
func (s *Service) translateScope(name string, sc *recorder.Scope) (snapper.PkgPlan, []string, error) {
	pkg := snapper.PkgPlan{Name: name}
	var warns []string
	for _, sp := range sc.Paths {
		abs, err := s.expandScopePath(sp.Path)
		if err != nil {
			warns = append(warns, fmt.Sprintf("skipping %s of %s: %v", sp.Path, name, err))
			continue
		}
		switch sp.Action {
		case recorder.ActionInclude:
			pkg.Includes = append(pkg.Includes, snapper.Include{Path: abs, Explicit: true})
		case recorder.ActionExclude:
			pkg.Excludes = append(pkg.Excludes, abs)
		}
	}
	return pkg, warns, nil
}
