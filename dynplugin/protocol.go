package dynplugin

type Handshake struct {
	Name    string   `json:"name"`
	Version string   `json:"version"`
	Hooks   []string `json:"hooks"`
}

type PluginManifest struct {
	Name    string                 `json:"name"`
	Version string                 `json:"version"`
	Type    string                 `json:"type"`
	Hooks   []string               `json:"hooks"`
	Exec    string                 `json:"exec"`
	Config  map[string]interface{} `json:"config"`
	Enabled *bool                  `json:"enabled"`
}

type Request struct {
	ID     int                    `json:"id"`
	Method string                 `json:"method"`
	Params map[string]interface{} `json:"params,omitempty"`
}

type Response struct {
	ID     int                    `json:"id"`
	Result map[string]interface{} `json:"result,omitempty"`
	Error  string                 `json:"error,omitempty"`
}

type ConfigMessage struct {
	Type string                 `json:"type"`
	Data map[string]interface{} `json:"data"`
}

const (
	HookOnAccept = "on_accept"
	HookOnOpen   = "on_open"
	HookOnClose  = "on_close"
	HookOnData   = "on_data"
	HookOnCheck  = "on_check"

	PluginTypeFilter   = "filter"
	PluginTypeLogger   = "logger"
	PluginTypeAlerting = "alerting"
)
