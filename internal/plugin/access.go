package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

// AccessPolicy 约束模型本轮操作，不控制宿主自己的会话记录或模型服务连接。
// 持久化同形字段只是偏好；是否授权由宿主在准入时决定。
type AccessDecision string

const (
	DecisionAllow AccessDecision = "allow"
	DecisionAsk   AccessDecision = "ask"
	DecisionDeny  AccessDecision = "deny"
)

type AccessKind string

const (
	AccessRead    AccessKind = "read"
	AccessWrite   AccessKind = "write"
	AccessNetwork AccessKind = "network"
	AccessExec    AccessKind = "exec"
)

var accessOrder = []AccessKind{AccessRead, AccessWrite, AccessNetwork, AccessExec}

type AccessPolicy struct {
	Read    AccessDecision `json:"read"`
	Write   AccessDecision `json:"write"`
	Network AccessDecision `json:"network"`
	Exec    AccessDecision `json:"exec"`
}

func DefaultAccessPolicy() AccessPolicy {
	return AccessPolicy{Read: DecisionAllow, Write: DecisionAsk, Network: DecisionAsk, Exec: DecisionAsk}
}
func (p AccessPolicy) Decision(kind AccessKind) AccessDecision {
	switch kind {
	case AccessRead:
		return p.Read
	case AccessWrite:
		return p.Write
	case AccessNetwork:
		return p.Network
	case AccessExec:
		return p.Exec
	default:
		return ""
	}
}
func (p AccessPolicy) Valid() bool {
	for _, kind := range accessOrder {
		switch p.Decision(kind) {
		case DecisionAllow, DecisionAsk, DecisionDeny:
		default:
			return false
		}
	}
	return true
}

// WithoutElevatedGrants 保留限制，只撤掉必须由当前服务进程确认的提升授权。
func (p AccessPolicy) WithoutElevatedGrants() AccessPolicy {
	if p.Write == DecisionAllow {
		p.Write = DecisionAsk
	}
	if p.Network == DecisionAllow {
		p.Network = DecisionAsk
	}
	if p.Exec == DecisionAllow {
		p.Exec = DecisionAsk
	}
	return p
}

// AccessRequest 是工具验证参数和范围之后提出的一次操作，不由浏览器审批正文指定。
// ParametersDigest 绑定实际参数；Preview 只供当前用户审批，不进入生命周期日志。
type AccessRequest struct {
	Tool             string       `json:"tool"`
	Summary          string       `json:"summary"`
	Target           string       `json:"target,omitempty"`
	Command          string       `json:"command,omitempty"`
	Cwd              string       `json:"cwd,omitempty"`
	Preview          string       `json:"preview,omitempty"`
	ParametersDigest string       `json:"parameters_digest,omitempty"`
	ReadRoots        []string     `json:"read_roots,omitempty"`
	WriteRoots       []string     `json:"write_roots,omitempty"`
	Permissions      []AccessKind `json:"permissions"`
	Ask              []AccessKind `json:"ask"`
	// ScopeApproval 要求另批本次目录范围，即使该类操作在已授权范围内为 allow。
	ScopeApproval bool `json:"scope_approval,omitempty"`
}
type ApprovalFunc func(context.Context, AccessRequest) error

var ErrApprovalRequired = errors.New("this operation requires user approval")
var ErrAccessDenied = errors.New("this operation is denied by the current permission policy")

func AccessPolicyFor(ctx context.Context) AccessPolicy {
	info, _ := Run(ctx)
	if info.ExecutionMode == ExecutionFullAccess {
		return AccessPolicy{Read: DecisionAllow, Write: DecisionAllow, Network: DecisionAllow, Exec: DecisionAllow}
	}
	if info.Permissions == nil {
		return DefaultAccessPolicy()
	}
	return *info.Permissions
}

// RequireAccess 必须在副作用和工具执行计时开始前调用。审批不能修改操作参数或扩大
// 范围；它只有批准/拒绝结果。调用返回后仍须遵守取消与具体文件/网络边界。
func RequireAccess(ctx context.Context, request AccessRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if request.Tool == "" || len(request.Permissions) == 0 {
		return fmt.Errorf("access request must identify a tool and required permissions")
	}
	info, _ := Run(ctx)
	if info.ExecutionMode != "" && !info.ExecutionMode.Valid() {
		return fmt.Errorf("invalid execution mode")
	}
	policy := AccessPolicyFor(ctx)
	if !policy.Valid() {
		return fmt.Errorf("invalid effective access policy")
	}
	required := make(map[AccessKind]bool)
	for _, kind := range request.Permissions {
		if policy.Decision(kind) == "" {
			return fmt.Errorf("unknown permission dimension")
		}
		required[kind] = true
	}
	if info.ExecutionMode == ExecutionFullAccess {
		return ctx.Err()
	}
	request.Permissions = nil
	request.Ask = nil
	for _, kind := range accessOrder {
		if !required[kind] {
			continue
		}
		request.Permissions = append(request.Permissions, kind)
		if policy.Decision(kind) == DecisionDeny {
			return fmt.Errorf("%w: %s", ErrAccessDenied, kind)
		}
		if policy.Decision(kind) == DecisionAsk {
			request.Ask = append(request.Ask, kind)
		}
	}
	if len(request.Ask) == 0 && !request.ScopeApproval {
		return nil
	}
	if info.Approve == nil {
		return ErrApprovalRequired
	}
	// 交给审批器的切片与执行调用自己的范围解耦，审批器没有回写授权的通道。
	request.ReadRoots = append([]string(nil), request.ReadRoots...)
	request.WriteRoots = append([]string(nil), request.WriteRoots...)
	if err := info.Approve(ctx, request); err != nil {
		return err
	}
	return ctx.Err()
}

// AccessDigest 只用于绑定参数，不保存或记录参数正文。
func AccessDigest(arguments string) string {
	sum := sha256.Sum256([]byte(arguments))
	return hex.EncodeToString(sum[:])
}

// CheckAccess 只检查明确拒绝，不请求批准。工具用它在探测路径等预处理之前失败关闭；
// 真正操作前仍须调用 RequireAccess，绑定已经验证的具体参数和范围。
func CheckAccess(ctx context.Context, kinds ...AccessKind) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, _ := Run(ctx)
	if info.ExecutionMode != "" && !info.ExecutionMode.Valid() {
		return fmt.Errorf("invalid execution mode")
	}
	policy := AccessPolicyFor(ctx)
	if !policy.Valid() {
		return fmt.Errorf("invalid effective access policy")
	}
	for _, kind := range kinds {
		decision := policy.Decision(kind)
		if decision == "" {
			return fmt.Errorf("unknown permission dimension")
		}
		if decision == DecisionDeny {
			return fmt.Errorf("%w: %s", ErrAccessDenied, kind)
		}
	}
	return nil
}
