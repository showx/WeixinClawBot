package app

import (
	"fmt"
	"time"

	"weixinclawbot/internal/store"
)

const observedFreshWindow = 24 * time.Hour

// SessionStatus 是给人或接口看的会话状态，不包含 context_token。
type SessionStatus struct {
	UserID    string
	UpdatedAt time.Time
	SentCount int
	Status    string
	Detail    string
}

// DescribeSession 根据本地记录说明当前能不能发。
// 微信不下发过期时间，所以只有被微信拒绝过后才标成不可发送。
func DescribeSession(item store.Session, now time.Time) SessionStatus {
	view := SessionStatus{
		UserID:    item.UserID,
		UpdatedAt: item.UpdatedAt,
		SentCount: item.SentCount,
		Status:    "ready",
	}
	switch item.BlockReason {
	case store.BlockExpired:
		view.Status = "rejected"
		view.Detail = "微信已拒绝该令牌，需对方再发一条消息"
		return view
	case store.BlockRateLimited:
		view.Status = "rate_limited"
		view.Detail = "微信已限流，需对方再发一条消息"
		return view
	}
	view.Detail = fmt.Sprintf("可尝试发送，自上次收到消息后已发 %d 条", item.SentCount)
	if !item.UpdatedAt.IsZero() && now.Sub(item.UpdatedAt) > observedFreshWindow {
		view.Detail += "；距上次收到消息已超过 24 小时，微信可能拒绝"
	} else if item.SentCount >= 10 {
		view.Detail += "；已达到常见的约 10 条上限，继续发可能被限流"
	}
	return view
}
