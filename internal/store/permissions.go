package store

// PermissionRecord 只是会话偏好，不包含审批凭证或服务进程授权。
// 单独保留存储类型，避免会话日志直接依赖执行内核的数据结构。
type PermissionRecord struct {
	Read    string `json:"read"`
	Write   string `json:"write"`
	Network string `json:"network"`
	Exec    string `json:"exec"`
}
