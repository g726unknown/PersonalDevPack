package executor

import (
	"context"
	"fmt"
	"time"

	"github.com/g726unknown/pdp/internal/recorder"
)

// ScopeInput 是 SetScope 的调用参数。
type ScopeInput struct {
	Name         string
	Scope        recorder.Scope
	BaseRevision int
}

// SetScopeResult 是 SetScope 的输出。
type SetScopeResult struct {
	Action   recorder.Action // set | not_present
	Revision int
	// Scope 是值拷贝回显；调用方改它动不了盘——盘只在 Update 的
	// 窗口内被写过。not_present 时为零值。
	Scope recorder.Scope
}

// SetScope 把备份范围判断写进清单存档（base_revision 乐观锁下）。
//
// 它是唯一既不碰 observer 也不碰 pathobserver 的写用例：
// 存的是对话结果，不是系统事实——证据采集在 InspectPaths，
// 用户确认发生在对话里，这里只负责让判断落盘可审查。
//
// 校验只有一份、活在 recorder.SetScope（锁内执法，错误包
// ErrInvalidScope），本层不镜像规则——SetScope 没有探查可省，
// 预检毫无意义，哨兵让错误原样上抛也能在信封层正确分类。
// not_present 不是 error：action 原样返回，由 mcpserver 映射 not_found。
func (s *Service) SetScope(ctx context.Context, in ScopeInput) (SetScopeResult, error) {
	if in.Name == "" {
		return SetScopeResult{}, fmt.Errorf("%w: empty package name", ErrInvalidInput)
	}

	var act recorder.Action
	newList, err := s.rec.Update(ctx, in.BaseRevision, func(l *recorder.List) error {
		// 闭包捕获 act：RMW 回调的标准姿势（同 AddPackage）。
		// fn 的错误原样传出，Update 不包裹（recorder 既定契约）——
		// ErrConflict 与 ErrInvalidScope 都在信封层有归宿。
		var serr error
		act, serr = l.SetScope(in.Name, in.Scope, time.Now())
		return serr
	})
	if err != nil {
		return SetScopeResult{}, err
	}

	res := SetScopeResult{Action: act, Revision: newList.Revision}
	if p := newList.Packages[in.Name]; p != nil && p.Config != nil {
		// 值拷贝回显（同 AddResult.Package 的快照口径）。
		res.Scope = *p.Config
	}
	return res, nil
}
