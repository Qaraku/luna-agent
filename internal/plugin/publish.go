package plugin

import "fmt"

// Publish 将验证通过的能力替换/登记到目标状态。commit 只做持久化，不得重入 Registry。
// 声明冲突发生在 commit 前；commit 失败不改变运行态，成功后发布不再进行可失败操作。
// 旧 Entry 的持有者仍持有旧对象；版本文件和调用收尾由部署方维护。
func (r *Registry) Publish(p Plugin, state State, commit func() error) error {
	if p == nil {
		return fmt.Errorf("plugin is required")
	}
	if state != StateRegistered && state != StateEnabled && state != StateDisabled {
		return fmt.Errorf("invalid published lifecycle state")
	}
	d := p.Descriptor()
	r.mu.RLock()
	grants := map[PermissionKind]bool{}
	for k, v := range r.granted {
		grants[k] = v
	}
	r.mu.RUnlock()
	if err := validateDescriptor(d, grants); err != nil {
		return err
	}
	if err := checkConsistency(p, d); err != nil {
		return err
	}
	if state == StateEnabled && d.Deployment == DeploymentBrowser {
		return fmt.Errorf("browser modules use their own mounting lifecycle")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	pending := map[claimKey]bool{}
	for _, c := range d.Claims {
		k := claimKey{c.Kind, c.ID}
		if pending[k] {
			return fmt.Errorf("duplicate resource claim")
		}
		pending[k] = true
		if owner, ok := r.claims[k]; ok && owner != d.ID {
			return fmt.Errorf("claim %s %q belongs to %q", c.Kind, c.ID, owner)
		}
	}
	contributions := map[contribKey]bool{}
	for _, c := range d.Contributions {
		if !exposedGlobally(c.Kind) {
			continue
		}
		k := contribKey{c.Kind, c.ID}
		contributions[k] = true
		if owner, ok := r.contributions[k]; ok && owner != d.ID {
			return fmt.Errorf("contribution %s %q belongs to %q", c.Kind, c.ID, owner)
		}
	}
	if commit != nil {
		if err := commit(); err != nil {
			return err
		}
	}
	r.releaseClaims(d.ID)
	entry := Entry{Plugin: p, Descriptor: cloneDescriptor(d), State: state}
	if i, ok := r.index[d.ID]; ok {
		r.entries[i] = entry
	} else {
		r.index[d.ID] = len(r.entries)
		r.entries = append(r.entries, entry)
	}
	for k := range pending {
		r.claims[k] = d.ID
	}
	for k := range contributions {
		r.contributions[k] = d.ID
	}
	r.revision++
	return nil
}
func (r *Registry) Remove(id string, commit func() error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	i, ok := r.index[id]
	if !ok {
		return fmt.Errorf("plugin %q is not registered", id)
	}
	if commit != nil {
		if err := commit(); err != nil {
			return err
		}
	}
	r.releaseClaims(id)
	r.entries = append(r.entries[:i], r.entries[i+1:]...)
	delete(r.index, id)
	for j := i; j < len(r.entries); j++ {
		r.index[r.entries[j].Descriptor.ID] = j
	}
	r.revision++
	return nil
}
func (r *Registry) releaseClaims(id string) {
	for key, owner := range r.claims {
		if owner == id {
			delete(r.claims, key)
		}
	}
	for key, owner := range r.contributions {
		if owner == id {
			delete(r.contributions, key)
		}
	}
}

// StateController 让拥有独立持久化目录的能力协调其启停；HTTP 宿主不另写一份冲突偏好。
type StateController interface{ SetEnabled(bool) error }
