package dynplugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Manager struct {
	plugins []*PluginProcess
	timeout time.Duration
}

func NewManager(timeout time.Duration) *Manager {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Manager{
		plugins: make([]*PluginProcess, 0),
		timeout: timeout,
	}
}

func (m *Manager) LoadDir(dir string) error {
	manifestPath := filepath.Join(dir, "plugin.json")
	if data, err := os.ReadFile(manifestPath); err == nil {
		var manifest PluginManifest
		if err := json.Unmarshal(data, &manifest); err == nil {
			if manifest.Exec != "" {
				if manifest.Enabled != nil && !*manifest.Enabled {
					fmt.Printf("[dynplugin] INFO 插件 %s 已禁用 (enabled=false)\n", manifest.Name)
					return nil
				}
				if err := m.LoadPlugin(manifest, dir); err != nil {
					return fmt.Errorf("load plugin %s: %w", manifest.Name, err)
				}
				return nil
			}
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read plugin dir %s: %w", dir, err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		manifestPath := filepath.Join(dir, entry.Name(), "plugin.json")
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			continue
		}

		var manifest PluginManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			continue
		}

		if manifest.Exec == "" {
			continue
		}

		if manifest.Enabled != nil && !*manifest.Enabled {
			fmt.Printf("[dynplugin] INFO 插件 %s 已禁用 (enabled=false)\n", manifest.Name)
			continue
		}

		workingDir := filepath.Join(dir, entry.Name())
		if err := m.LoadPlugin(manifest, workingDir); err != nil {
			fmt.Printf("[dynplugin] WARN 加载插件 %s 失败: %v\n", manifest.Name, err)
			continue
		}
	}

	return nil
}

func (m *Manager) LoadPlugin(manifest PluginManifest, workingDir string) error {
	proc := NewPluginProcess(manifest, m.timeout)
	if err := proc.Start(workingDir); err != nil {
		return err
	}

	m.plugins = append(m.plugins, proc)
	fmt.Printf("[dynplugin] INFO 插件已加载: %s v%s (hooks: %v)\n",
		proc.Name(), manifest.Version, proc.HandshakeHooks())
	return nil
}

func (m *Manager) OnAccept(proto string, addr string) (bool, string) {
	for _, p := range m.plugins {
		if !p.HasHook(HookOnAccept) || !p.Alive() {
			continue
		}

		result, err := p.Call(HookOnAccept, map[string]interface{}{
			"proto": proto,
			"addr":  addr,
		})
		if err != nil {
			fmt.Printf("[dynplugin] WARN 插件 %s on_accept 错误: %v\n", p.Name(), err)
			continue
		}

		allowed, _ := result["allowed"].(bool)
		if !allowed {
			reason, _ := result["reason"].(string)
			return false, reason
		}
	}
	return true, ""
}

type PluginAction struct {
	Close  bool
	Reason string
}

func (m *Manager) OnOpen(proto, remoteAddr string, channelID uint32, localPort int) PluginAction {
	for _, p := range m.plugins {
		if !p.HasHook(HookOnOpen) || !p.Alive() {
			continue
		}

		result, err := p.Call(HookOnOpen, map[string]interface{}{
			"proto":       proto,
			"remote_addr": remoteAddr,
			"channel_id":  channelID,
			"local_port":  localPort,
		})
		if err != nil {
			fmt.Printf("[dynplugin] WARN 插件 %s on_open 错误: %v\n", p.Name(), err)
			continue
		}

		if action, _ := result["action"].(string); action == "close" {
			reason, _ := result["reason"].(string)
			return PluginAction{Close: true, Reason: reason}
		}
	}
	return PluginAction{}
}

func (m *Manager) OnClose(channelID uint32) {
	for _, p := range m.plugins {
		if !p.HasHook(HookOnClose) || !p.Alive() {
			continue
		}

		p.Notify(HookOnClose, map[string]interface{}{
			"channel_id": channelID,
		})
	}
}

func (m *Manager) OnData(channelID uint32, dir string, n int) {
	for _, p := range m.plugins {
		if !p.HasHook(HookOnData) || !p.Alive() {
			continue
		}

		p.Notify(HookOnData, map[string]interface{}{
			"channel_id": channelID,
			"dir":        dir,
			"bytes":      n,
		})
	}
}

func (m *Manager) OnCheck() []uint32 {
	var allChannels []uint32
	for _, p := range m.plugins {
		if !p.HasHook(HookOnCheck) || !p.Alive() {
			continue
		}

		result, err := p.Call(HookOnCheck, map[string]interface{}{})
		if err != nil {
			fmt.Printf("[dynplugin] WARN 插件 %s on_check 错误: %v\n", p.Name(), err)
			continue
		}

		if action, _ := result["action"].(string); action == "close" {
			if channels, ok := result["channels"].([]interface{}); ok {
				for _, ch := range channels {
					if chID, ok := ch.(float64); ok {
						allChannels = append(allChannels, uint32(chID))
					}
				}
			}
		}
	}
	return allChannels
}

func (m *Manager) Stop() {
	for _, p := range m.plugins {
		p.Stop()
	}
	m.plugins = nil
}

func (m *Manager) PluginCount() int {
	return len(m.plugins)
}

func (m *Manager) PluginNames() []string {
	names := make([]string, 0, len(m.plugins))
	for _, p := range m.plugins {
		names = append(names, p.Name())
	}
	return names
}
