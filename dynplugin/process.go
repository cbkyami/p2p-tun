package dynplugin

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type PluginProcess struct {
	manifest  PluginManifest
	handshake Handshake
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    *bufio.Scanner
	mu        sync.Mutex
	writeMu   sync.Mutex
	nextID    int
	pending   map[int]chan *Response
	timeout   time.Duration
	stopCh    chan struct{}
}

func NewPluginProcess(manifest PluginManifest, timeout time.Duration) *PluginProcess {
	return &PluginProcess{
		manifest: manifest,
		pending:  make(map[int]chan *Response),
		timeout:  timeout,
		stopCh:   make(chan struct{}),
	}
}

func (p *PluginProcess) Start(workingDir string) error {
	parts, err := parseExec(p.manifest.Exec)
	if err != nil {
		return fmt.Errorf("parse exec command: %w", err)
	}

	cmdPath := parts[0]
	if strings.ContainsAny(cmdPath, "/\\") && !filepath.IsAbs(cmdPath) {
		cmdPath = filepath.Join(workingDir, cmdPath)
	}

	args := parts[1:]
	p.cmd = exec.Command(cmdPath, args...)
	p.cmd.Dir = workingDir
	p.cmd.Stderr = os.Stderr
	p.cmd.SysProcAttr = getSysProcAttr()

	stdin, err := p.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	p.stdin = stdin

	stdout, err := p.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	p.stdout = bufio.NewScanner(stdout)
	p.stdout.Buffer(make([]byte, 1024*1024), 1024*1024)

	if err := p.cmd.Start(); err != nil {
		return fmt.Errorf("start process: %w", err)
	}

	if !p.stdout.Scan() {
		return fmt.Errorf("failed to read handshake from plugin %s", p.manifest.Name)
	}

	if err := json.Unmarshal([]byte(p.stdout.Text()), &p.handshake); err != nil {
		p.cmd.Process.Kill()
		return fmt.Errorf("invalid handshake from plugin %s: %w", p.manifest.Name, err)
	}

	if p.handshake.Name == "" {
		p.cmd.Process.Kill()
		return fmt.Errorf("plugin %s: handshake missing name", p.manifest.Name)
	}

	if len(p.manifest.Config) > 0 {
		cfgMsg := ConfigMessage{Type: "config", Data: p.manifest.Config}
		if err := p.sendRaw(cfgMsg); err != nil {
			p.cmd.Process.Kill()
			return fmt.Errorf("send config to plugin %s: %w", p.manifest.Name, err)
		}
	}

	go p.readLoop()

	return nil
}

func (p *PluginProcess) readLoop() {
	for {
		select {
		case <-p.stopCh:
			return
		default:
		}

		if !p.stdout.Scan() {
			return
		}

		var resp Response
		if err := json.Unmarshal([]byte(p.stdout.Text()), &resp); err != nil {
			continue
		}

		p.mu.Lock()
		ch, ok := p.pending[resp.ID]
		if ok {
			delete(p.pending, resp.ID)
		}
		p.mu.Unlock()

		if ok && ch != nil {
			ch <- &resp
		}
	}
}

func (p *PluginProcess) sendRaw(v interface{}) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = p.stdin.Write(data)
	return err
}

func (p *PluginProcess) Call(method string, params map[string]interface{}) (map[string]interface{}, error) {
	p.mu.Lock()
	p.nextID++
	id := p.nextID
	ch := make(chan *Response, 1)
	p.pending[id] = ch
	p.mu.Unlock()

	req := Request{ID: id, Method: method, Params: params}
	if err := p.sendRaw(req); err != nil {
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
		return nil, fmt.Errorf("send request to plugin %s: %w", p.manifest.Name, err)
	}

	select {
	case resp := <-ch:
		if resp.Error != "" {
			return nil, fmt.Errorf("plugin %s error: %s", p.manifest.Name, resp.Error)
		}
		return resp.Result, nil
	case <-time.After(p.timeout):
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
		return nil, fmt.Errorf("plugin %s timeout on %s", p.manifest.Name, method)
	case <-p.stopCh:
		return nil, fmt.Errorf("plugin %s stopped", p.manifest.Name)
	}
}

func (p *PluginProcess) Notify(method string, params map[string]interface{}) {
	req := Request{ID: 0, Method: method, Params: params}
	_ = p.sendRaw(req)
}

func (p *PluginProcess) HasHook(hook string) bool {
	for _, h := range p.handshake.Hooks {
		if h == hook {
			return true
		}
	}
	return false
}

func (p *PluginProcess) Stop() {
	close(p.stopCh)
	if p.stdin != nil {
		p.stdin.Close()
	}
	if p.cmd != nil && p.cmd.Process != nil {
		p.cmd.Process.Kill()
		p.cmd.Wait()
	}
}

func (p *PluginProcess) Name() string {
	return p.handshake.Name
}

func (p *PluginProcess) HandshakeHooks() []string {
	return p.handshake.Hooks
}

func (p *PluginProcess) Alive() bool {
	if p.cmd == nil || p.cmd.Process == nil {
		return false
	}
	return p.cmd.ProcessState == nil || !p.cmd.ProcessState.Exited()
}

func parseExec(execStr string) ([]string, error) {
	var parts []string
	var current []rune
	inQuote := false
	quoteChar := rune(0)

	for _, r := range execStr {
		switch {
		case r == '"' && !inQuote:
			inQuote = true
			quoteChar = r
		case r == quoteChar && inQuote:
			inQuote = false
			quoteChar = 0
		case r == ' ' && !inQuote:
			if len(current) > 0 {
				parts = append(parts, string(current))
				current = nil
			}
		default:
			current = append(current, r)
		}
	}
	if len(current) > 0 {
		parts = append(parts, string(current))
	}

	if len(parts) == 0 {
		return nil, fmt.Errorf("empty exec command")
	}
	return parts, nil
}
