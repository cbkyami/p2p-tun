package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

type TOMLConfig struct {
	Local     string `toml:"local"`
	Port      string `toml:"port"`
	Target    string `toml:"target"`
	Stun      string `toml:"stun"`
	Stun2     string `toml:"stun2"`
	NatType   string `toml:"nat_type"`
	Relay     string `toml:"relay"`
	Proto     string `toml:"proto"`
	AuthKey   string `toml:"auth_key"`
	Compress  bool   `toml:"compress"`
	IPAllow   string `toml:"ip_allow"`
	IPDeny    string `toml:"ip_deny"`
	MaxConns  int    `toml:"max_conns"`
	RateLimit int64  `toml:"rate_limit"`
	Verbose   bool   `toml:"verbose"`
	GUI       bool   `toml:"gui"`
	GUIPort   int    `toml:"gui_port"`

	Service []TOMLService `toml:"service"`
}

type TOMLService struct {
	Local     string `toml:"local"`
	Port      string `toml:"port"`
	Target    string `toml:"target"`
	Proto     string `toml:"proto"`
	Compress  *bool  `toml:"compress"`
	IPAllow   string `toml:"ip_allow"`
	IPDeny    string `toml:"ip_deny"`
	MaxConns  int    `toml:"max_conns"`
	RateLimit int64  `toml:"rate_limit"`
	WebAuth   string `toml:"web_auth"`
}

func loadTOMLConfig(path string) (*TOMLConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	var cfg TOMLConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	if cfg.Relay == "" {
		return nil, fmt.Errorf("配置文件缺少 relay 参数")
	}

	return &cfg, nil
}

func (c *TOMLConfig) toParams() (localPortsStr, preferPortsStr, targetHostsStr, protosStr, stunServer, stunServer2, natTypeOverride, relayServer, proto string, compress bool, ipAllow, ipDeny string, maxConns int, rateLimit int64, serviceOverrides []ServiceOverride) {
	if len(c.Service) > 0 {
		var locals, ports, targets, protos []string
		for _, svc := range c.Service {
			if strings.TrimSpace(svc.Local) == "" {
				continue
			}
			locals = append(locals, svc.Local)
			if svc.Port == "" {
				ports = append(ports, "0")
			} else {
				ports = append(ports, svc.Port)
			}
			if svc.Target == "" {
				targets = append(targets, "127.0.0.1")
			} else {
				targets = append(targets, svc.Target)
			}
			if svc.Proto != "" {
				protos = append(protos, svc.Proto)
			} else if c.Proto != "" {
				protos = append(protos, c.Proto)
			} else {
				protos = append(protos, "tcp")
			}

			var ov ServiceOverride
			ov.LocalPortStr = svc.Local
			if svc.Compress != nil {
				ov.Compress = *svc.Compress
				ov.HasCompress = true
			}
			if svc.IPAllow != "" {
				ov.IPAllow = mergeIPLists(c.IPAllow, svc.IPAllow)
				ov.HasIPAllow = true
			}
			if svc.IPDeny != "" {
				ov.IPDeny = mergeIPLists(c.IPDeny, svc.IPDeny)
				ov.HasIPDeny = true
			}
			if svc.MaxConns != 0 {
				ov.MaxConns = svc.MaxConns
				ov.HasMaxConns = true
			}
			if svc.RateLimit != 0 {
				ov.RateLimit = svc.RateLimit
				ov.HasRateLimit = true
			}
			ov.WebAuth = svc.WebAuth
			serviceOverrides = append(serviceOverrides, ov)
		}
		if len(locals) == 0 {
			localPortsStr = c.Local
			preferPortsStr = c.Port
			targetHostsStr = c.Target
			protosStr = ""
		} else {
			localPortsStr = strings.Join(locals, ",")
			preferPortsStr = strings.Join(ports, ",")
			targetHostsStr = strings.Join(targets, ",")
			protosStr = strings.Join(protos, ",")
		}
		if c.Proto != "" {
			proto = c.Proto
		} else {
			proto = "tcp"
		}
	} else {
		if c.Local != "" {
			localPortsStr = c.Local
		} else {
			localPortsStr = "8080"
		}
		if c.Port == "" {
			preferPortsStr = "0"
		} else {
			preferPortsStr = c.Port
		}
		if c.Target == "" {
			targetHostsStr = "127.0.0.1"
		} else {
			targetHostsStr = c.Target
		}
		protosStr = ""
		if c.Proto != "" {
			proto = c.Proto
		} else {
			proto = "tcp"
		}
	}

	stunServer = c.Stun
	stunServer2 = c.Stun2
	if stunServer2 == "" && stunServer != "" {
		stunServer2 = "stun1.l.google.com:19302"
	}
	natTypeOverride = c.NatType
	relayServer = c.Relay
	compress = c.Compress
	ipAllow = c.IPAllow
	ipDeny = c.IPDeny
	maxConns = c.MaxConns
	rateLimit = c.RateLimit

	return
}

type ServiceOverride struct {
	LocalPortStr string
	Compress     bool
	HasCompress  bool
	IPAllow      string
	HasIPAllow   bool
	IPDeny       string
	HasIPDeny    bool
	MaxConns     int
	HasMaxConns  bool
	RateLimit    int64
	HasRateLimit bool
	WebAuth      string
}

func mergeIPLists(global, service string) string {
	global = strings.TrimSpace(global)
	service = strings.TrimSpace(service)
	if global == "" {
		return service
	}
	if service == "" {
		return global
	}
	return global + "," + service
}
