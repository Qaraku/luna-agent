package plugin

import (
	"fmt"
	"path/filepath"
)

// StateDir 把一个插件认领的状态命名空间解析成绝对路径：状态根加命名空间目录名。
//
// 内核只负责给出这个目录。目录里放什么文件、叫什么名字，由插件自己决定，内核不知道
// 也不需要知道——否则“记忆文件叫什么”这类业务事实又会回到内核里。返回的路径不保证
// 已经存在：需要它的插件自己创建。
func (r *Registry) StateDir(id, root string) (string, error) {
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("state root %q must be absolute", root)
	}
	entry, ok := r.Entry(id)
	if !ok {
		return "", fmt.Errorf("plugin %q is not registered", id)
	}
	for _, c := range entry.Descriptor.Claims {
		if c.Kind == ClaimStateNamespace {
			return filepath.Join(root, c.ID), nil
		}
	}
	return "", fmt.Errorf("plugin %q claims no state namespace", id)
}
