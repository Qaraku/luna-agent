package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/Qaraku/luna-agent/internal/config"
	"github.com/Qaraku/luna-agent/internal/store"
)

type currentReasoning struct {
	Effort string `json:"reasoning_effort"`
	Origin string `json:"origin"`
}

// reasoningNow 与下一轮使用相同来源，不把进程启动值冒充会话当前值。
func (s *Server) reasoningNow(id string) (currentReasoning, error) {
	cfg, live, err := s.providerConfigNow()
	if err != nil {
		return currentReasoning{}, err
	}
	current := currentReasoning{Effort: s.info.ReasoningEffort, Origin: originGlobal}
	if live {
		current.Effort = cfg.ReasoningEffort
	}
	if id != "" {
		session, err := s.sessions.Read(id)
		if err != nil {
			return currentReasoning{}, err
		}
		if session.Config != nil && session.Config.ReasoningEffort != nil {
			current = currentReasoning{Effort: *session.Config.ReasoningEffort, Origin: originSession}
		}
	}
	return current, nil
}

func (s *Server) sendReasoning(w http.ResponseWriter, r *http.Request) {
	current, err := s.reasoningNow(strings.TrimSpace(r.URL.Query().Get("session")))
	if err != nil {
		fail(w, sessionStatus(err), err)
		return
	}
	send(w, http.StatusOK, struct {
		Current currentReasoning `json:"current"`
		Levels  []string         `json:"levels"`
	}{Current: current, Levels: config.ReasoningEffortLevels})
}

func (s *Server) setSessionReasoning(w http.ResponseWriter, r *http.Request, id string) {
	var in struct {
		Effort *string `json:"reasoning_effort"`
		Reset  bool    `json:"reset"`
	}
	if !decode(w, r, &in) {
		return
	}
	if (in.Effort == nil && !in.Reset) || (in.Effort != nil && in.Reset) {
		fail(w, 400, fmt.Errorf("provide reasoning_effort or reset=true, not both"))
		return
	}
	var choice *string
	if in.Effort != nil {
		parsed, err := config.ParseReasoningEffort(*in.Effort)
		if err != nil {
			fail(w, 400, err)
			return
		}
		choice = &parsed
	}
	// 恢复继承的有效值在写入前解析；读全局配置失败时不能保存后再报告失败。
	current, err := s.reasoningNow("")
	if err != nil {
		fail(w, 500, err)
		return
	}
	if choice != nil {
		current = currentReasoning{Effort: *choice, Origin: originSession}
	}
	status, err := s.updateSessionConfig(id, func(next *store.ConfigRecord) (int, error) { next.ReasoningEffort = choice; return http.StatusOK, nil })
	if err != nil {
		fail(w, status, err)
		return
	}
	send(w, http.StatusOK, struct {
		SessionID string           `json:"session_id"`
		Current   currentReasoning `json:"current"`
	}{SessionID: id, Current: current})
}

func sessionReasoningPath(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, "/api/sessions/")
	if !ok {
		return "", false
	}
	id, ok := strings.CutSuffix(rest, "/reasoning")
	return id, ok && id != ""
}
