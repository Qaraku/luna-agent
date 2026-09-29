package httpapi

import (
	"net/http"
	"time"

	"github.com/Qaraku/luna-agent/internal/store"
)

// updateSessionConfig 让独立设置只修改自己的字段，同时保留最近已提交的其他设置。
// 存储的追加锁不能覆盖调用方的读改写，因此所有 HTTP 配置写入口共用此事务。
// 读取或校验失败时不写记录，锁在返回前释放，不包含后续事件记录和 HTTP 写回。
func (s *Server) updateSessionConfig(id string, change func(*store.ConfigRecord) (int, error)) (int, error) {
	s.sessionConfigMu.Lock()
	defer s.sessionConfigMu.Unlock()
	session, err := s.sessions.Read(id)
	if err != nil {
		return sessionStatus(err), err
	}
	var next store.ConfigRecord
	if session.Config != nil {
		next = *session.Config
	}
	if status, err := change(&next); err != nil {
		return status, err
	}
	next.Type = store.TypeConfig
	next.At = time.Now()
	if err := s.sessions.AppendConfig(id, next); err != nil {
		return sessionStatus(err), err
	}
	return http.StatusOK, nil
}
