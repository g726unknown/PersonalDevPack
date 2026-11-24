package executor

import (
	"context"
	"fmt"
	"time"

	"github.com/g726unknown/pdp/internal/recorder"
)

// AddInput 是 AddPackage 的调用参数。
type AddInput struct {
	Name         string
	Type         string
	Source       string // 非空时覆盖自动判断
	BaseRevision int
}

// AddResult 是 AddPackage 的输出。
// Package 是值拷贝——不把 Upsert 返回的内部指针递给调用方：
// 调用方拿到的是快照，清单后续变化不影响它。
type AddResult struct {
	Action   recorder.Action
	Revision int
	Package  recorder.Package
	Warning  string // 未安装时非空
}

// AddPackage 探查系统 → upsert 清单，四步固定：
//
//	口径先行 → 探查 → source 裁定 → Update 落盘
//
// 校验放在探查之前：拒绝的调用不花一次子进程。recorder 仍是最终
// 执法者（Upsert 在锁内还会再验一次 type），而提前校验与最终校验
// 引用同一个函数（recorder.ValidType），镜像不存在，漂移不可能发生。
// unchanged 照常落盘、revision 照常 +1——recorder 的既定语义，本包不做特判。
// v1 假定目标环境有 pacman；无 pacman 机器的行为见 DESIGN 待定。
func (s *Service) AddPackage(ctx context.Context, in AddInput) (AddResult, error) {
	if in.Name == "" {
		return AddResult{}, fmt.Errorf("%w: empty package name", ErrInvalidInput)
	}
	if !recorder.ValidType(in.Type) {
		return AddResult{}, fmt.Errorf("%w: invalid type %q", ErrInvalidInput, in.Type)
	}

	statuses, err := s.obs.PacmanQuery(ctx, []string{in.Name})
	if err != nil {
		return AddResult{}, err
	}
	st := statuses[0]

	source := in.Source // 非空时覆盖自动判断
	if source == "" {
		source = deriveSource(st)
	}

	warning := ""
	if !st.Installed {
		warning = "not installed on this system; version is recorded as empty"
	}

	var act recorder.Action
	var pkg *recorder.Package
	newList, err := s.rec.Update(ctx, in.BaseRevision, func(l *recorder.List) error {
		// 闭包捕获 act 与 pkg：fn 只在这里能见到清单，
		// Upsert 的返回值只能经捕获变量带出去——
		// Go 写 RMW 回调的标准姿势。fn 的错误原样传出，
		// Update 不包裹（recorder 既定契约）。
		var uerr error
		act, pkg, uerr = l.Upsert(in.Name,
			recorder.Facts{Type: in.Type, Version: st.Version, Source: source}, time.Now())
		return uerr
	})
	if err != nil {
		// ConflictError 与 Upsert 的校验错误都从这里透传。
		return AddResult{}, err
	}
	return AddResult{
		Action:   act,
		Revision: newList.Revision,
		Package:  *pkg,
		Warning:  warning,
	}, nil
}
