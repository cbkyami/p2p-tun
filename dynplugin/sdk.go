package dynplugin

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type FilterHandler func(params map[string]interface{}) map[string]interface{}

type PluginSDK struct {
	name    string
	version string
	hooks   []string
	filters map[string]FilterHandler
}

func NewPluginSDK(name, version string) *PluginSDK {
	return &PluginSDK{
		name:    name,
		version: version,
		filters: make(map[string]FilterHandler),
	}
}

func (s *PluginSDK) RegisterFilter(hook string, handler FilterHandler) {
	s.hooks = append(s.hooks, hook)
	s.filters[hook] = handler
}

func (s *PluginSDK) Run() {
	handshake := Handshake{
		Name:    s.name,
		Version: s.version,
		Hooks:   s.hooks,
	}
	writeJSON(handshake)

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()

		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}

		if msgType, ok := raw["type"]; ok {
			var t string
			json.Unmarshal(msgType, &t)
			if t == "config" {
				continue
			}
		}

		var req Request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}

		if req.ID == 0 {
			if handler, ok := s.filters[req.Method]; ok {
				handler(req.Params)
			}
			continue
		}

		resp := Response{ID: req.ID}
		if handler, ok := s.filters[req.Method]; ok {
			resp.Result = handler(req.Params)
		} else {
			resp.Error = fmt.Sprintf("unknown method: %s", req.Method)
		}
		writeJSON(resp)
	}
}

func writeJSON(v interface{}) {
	data, _ := json.Marshal(v)
	data = append(data, '\n')
	os.Stdout.Write(data)
}
