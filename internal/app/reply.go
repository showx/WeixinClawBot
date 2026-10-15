package app

import (
	"context"
	"strings"
	"time"
)

const receivedReplyPrefix = "收到 "

// ReceivedReply 是自动回复的正文：收到，加上当前时间。
func ReceivedReply(now time.Time) string {
	return receivedReplyPrefix + now.Local().Format("2006-01-02 15:04:05")
}

// IsReceivedReply 判断这段文字是不是刚才自动发出的回执，避免再回一次。
func IsReceivedReply(text string) bool {
	rest, ok := strings.CutPrefix(strings.TrimSpace(text), receivedReplyPrefix)
	if !ok {
		return false
	}
	_, err := time.ParseInLocation("2006-01-02 15:04:05", rest, time.Local)
	return err == nil
}

// ReplyReceived 给刚发来消息的人回「收到」和当前时间。自己的回执不会再回。
func (a *App) ReplyReceived(ctx context.Context, account, userID, inbound string) (string, error) {
	if a == nil || strings.TrimSpace(userID) == "" || IsReceivedReply(inbound) {
		return "", nil
	}
	reply := ReceivedReply(time.Now())
	var err error
	if strings.TrimSpace(account) != "" && a.Catalog != nil {
		_, err = a.SendAccount(ctx, account, userID, reply)
	} else {
		_, err = a.Send(ctx, userID, reply)
	}
	return reply, err
}

// AckIncoming 在后台回复「收到」和时间。done 在回复结束时调用，跳过时不会调用。
func (a *App) AckIncoming(msg Incoming, done func(reply string, err error)) {
	if a == nil || strings.TrimSpace(msg.UserID) == "" || IsReceivedReply(msg.Text) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		reply, err := a.ReplyReceived(ctx, msg.Account, msg.UserID, msg.Text)
		if err != nil {
			a.logf("回复收到失败 %s：%v", msg.UserID, err)
		} else if reply != "" {
			a.logf("已回复 %s：%s", msg.UserID, reply)
		}
		if done != nil {
			done(reply, err)
		}
	}()
}
