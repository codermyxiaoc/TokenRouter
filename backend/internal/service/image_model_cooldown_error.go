package service

import (
	"errors"
	"time"
)

// 绝对截止只在进程内传递，避免多账号扫描和错误包装把剩余 TTL 重新起算。
type imageModelCooldownError struct {
	err      error
	deadline time.Time
}

func (e *imageModelCooldownError) Error() string { return e.err.Error() }
func (e *imageModelCooldownError) Unwrap() error { return e.err }

func ImageModelCooldownDeadline(err error) (time.Time, bool) {
	var cooling *imageModelCooldownError
	if errors.As(err, &cooling) {
		return cooling.deadline, true
	}
	return time.Time{}, false
}
