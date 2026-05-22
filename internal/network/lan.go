package network

import (
	"net"
	"strings"
)

func BestLANIP() string {
	return BestReachableIP(false)
}

func BestReachableIP(preferTailscale bool) string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "127.0.0.1"
	}
	first := ""
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			ip = ip.To4()
			if ip == nil {
				continue
			}
			if first == "" {
				first = ip.String()
			}
			if preferTailscale && isTailscaleCandidate(iface.Name, ip) {
				return ip.String()
			}
		}
	}
	if first != "" {
		return first
	}
	return "127.0.0.1"
}

func isTailscaleCandidate(interfaceName string, ip net.IP) bool {
	name := strings.ToLower(interfaceName)
	return strings.Contains(name, "tailscale") || isTailscaleIP(ip)
}

func isTailscaleIP(ip net.IP) bool {
	v4 := ip.To4()
	if v4 == nil {
		return false
	}
	return v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127
}
