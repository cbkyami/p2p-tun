package plugin

import (
	"fmt"
	"net"
	"strings"
)

type IPFilter struct {
	allowNets []*net.IPNet
	denyNets  []*net.IPNet
}

func NewIPFilter(allow, deny string) (*IPFilter, error) {
	f := &IPFilter{}

	if allow != "" {
		for _, cidr := range strings.Split(allow, ",") {
			cidr = strings.TrimSpace(cidr)
			if cidr == "" {
				continue
			}
			if !strings.Contains(cidr, "/") {
				cidr = cidr + "/32"
			}
			_, ipnet, err := net.ParseCIDR(cidr)
			if err != nil {
				return nil, fmt.Errorf("invalid allow CIDR %q: %w", cidr, err)
			}
			f.allowNets = append(f.allowNets, ipnet)
		}
	}

	if deny != "" {
		for _, cidr := range strings.Split(deny, ",") {
			cidr = strings.TrimSpace(cidr)
			if cidr == "" {
				continue
			}
			if !strings.Contains(cidr, "/") {
				cidr = cidr + "/32"
			}
			_, ipnet, err := net.ParseCIDR(cidr)
			if err != nil {
				return nil, fmt.Errorf("invalid deny CIDR %q: %w", cidr, err)
			}
			f.denyNets = append(f.denyNets, ipnet)
		}
	}

	return f, nil
}

func (f *IPFilter) OnAccept(proto string, addr net.Addr) bool {
	ipStr, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		ipStr = addr.String()
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return true
	}

	if len(f.allowNets) > 0 {
		allowed := false
		for _, n := range f.allowNets {
			if n.Contains(ip) {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}

	for _, n := range f.denyNets {
		if n.Contains(ip) {
			return false
		}
	}

	return true
}
