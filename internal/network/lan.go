package network

import (
	"net"
	"strings"
)

func BestLANIP() string {
	return BestReachableIP(false)
}

func BestReachableIP(preferTailscale bool) string {
	routeIP := defaultRouteIPv4()
	ifaces, err := net.Interfaces()
	if err != nil {
		if routeIP != "" {
			return routeIP
		}
		return "127.0.0.1"
	}
	first := ""
	firstPrivate := ""
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
			if preferTailscale && isTailscaleCandidate(iface.Name, ip) {
				return ip.String()
			}
			ipString := ip.String()
			if first == "" {
				first = ipString
			}
			if !isTailscaleIP(ip) && ip.IsPrivate() {
				if ipString == routeIP {
					return ipString
				}
				if firstPrivate == "" {
					firstPrivate = ipString
				}
			}
		}
	}
	if !preferTailscale && routeIP != "" && isPrivateNonTailscaleIPv4(net.ParseIP(routeIP)) {
		return routeIP
	}
	if firstPrivate != "" {
		return firstPrivate
	}
	if first != "" {
		return first
	}
	if routeIP != "" {
		return routeIP
	}
	return "127.0.0.1"
}

func defaultRouteIPv4() string {
	conn, err := net.Dial("udp4", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	local, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || local.IP == nil {
		return ""
	}
	ip := local.IP.To4()
	if ip == nil || ip.IsLoopback() {
		return ""
	}
	return ip.String()
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

func isPrivateNonTailscaleIPv4(ip net.IP) bool {
	v4 := ip.To4()
	return v4 != nil && v4.IsPrivate() && !isTailscaleIP(v4)
}
