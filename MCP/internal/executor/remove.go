package executor

import (
	"context"
	"fmt"
	"time"

	"github.com/g726unknown/pdp/internal/recorder"
)

// RemoveInput 是 RemovePackage 的调用参数。
type RemoveInput struct {
	Name         string
	BaseRevision int
}

// RemoveResult 是 RemovePackage 的输出。
type RemoveResult struct {
	Action   recorder.Action // removed / already_removed / not_present
	Revision int
}

// RemovePackage 从清单软删除一个条目。
//
// 它是不需要探查的写用例（SetScope 同族）：从清单移除与系统无关——
// 包在机器上装没装，与“用户不再想跟踪它”这个意图无关。
// not_present 不是 error：action 原样返回，
// 由 mcpserver 映射为信封 status=not_found。
func (s *Service) RemovePackage(ctx context.Context, in RemoveInput) (RemoveResult, error) {
	if in.Name == "" {
		return RemoveResult{}, fmt.Errorf("%w: empty package name", ErrInvalidInput)
	}

	var act recorder.Action
	newList, err := s.rec.Update(ctx, in.BaseRevision, func(l *recorder.List) error {
		act = l.MarkRemoved(in.Name, time.Now())
		return nil
	})
	if err != nil {
		return RemoveResult{}, err
	}
	return RemoveResult{Action: act, Revision: newList.Revision}, nil
}
