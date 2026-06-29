package upnp

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

type Result struct {
	OK         bool   `json:"ok"`
	Message    string `json:"message"`
	ExternalIP string `json:"externalIP,omitempty"`
	Gateway    string `json:"gateway,omitempty"`
	Service    string `json:"service,omitempty"`
}

type gatewayService struct {
	DeviceURL   string
	ControlURL  string
	ServiceType string
	LocalIP     string
}

type TCPMapping struct {
	mu           sync.Mutex
	service      gatewayService
	protocol     string
	externalPort int
	internalPort int
	externalIP   string
	closed       bool
}

func (m *TCPMapping) ExternalIP() string {
	if m == nil {
		return ""
	}
	return m.externalIP
}

func (m *TCPMapping) ExternalPort() int {
	if m == nil {
		return 0
	}
	return m.externalPort
}

func (m *TCPMapping) InternalPort() int {
	if m == nil {
		return 0
	}
	return m.internalPort
}

func (m *TCPMapping) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	service := m.service
	protocol := m.protocol
	externalPort := m.externalPort
	m.mu.Unlock()
	return deletePortMapping(service, protocol, externalPort)
}

func IsGloballyRoutableIPv4(raw string) bool {
	ip := net.ParseIP(strings.TrimSpace(raw))
	if ip == nil || ip.To4() == nil {
		return false
	}
	if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
		ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	_, carrierNAT, _ := net.ParseCIDR("100.64.0.0/10")
	return !carrierNAT.Contains(ip)
}

func TryMapTCP(port int, description string) Result {
	_, result := MapTCP(port, port, description)
	return result
}

func MapTCP(internalPort, externalPort int, description string) (*TCPMapping, Result) {
	services, err := discoverServices()
	if err != nil {
		return nil, Result{Message: err.Error()}
	}
	return mapTCPWithServices(services, internalPort, externalPort, description)
}

func mapTCPWithServices(services []gatewayService, internalPort, externalPort int, description string) (*TCPMapping, Result) {
	return mapProtocolWithServices(services, "TCP", internalPort, externalPort, description)
}

func MapUDP(internalPort, externalPort int, description string) (*TCPMapping, Result) {
	services, err := discoverServices()
	if err != nil {
		return nil, Result{Message: err.Error()}
	}
	return mapUDPWithServices(services, internalPort, externalPort, description)
}

func mapUDPWithServices(services []gatewayService, internalPort, externalPort int, description string) (*TCPMapping, Result) {
	return mapProtocolWithServices(services, "UDP", internalPort, externalPort, description)
}

func mapProtocolWithServices(services []gatewayService, protocol string, internalPort, externalPort int, description string) (*TCPMapping, Result) {
	if len(services) == 0 {
		return nil, Result{Message: "no UPnP WAN connection service found"}
	}
	protocol = normalizeProtocol(protocol)

	var failures []string
	for _, svc := range services {
		if svc.LocalIP == "" {
			localIP, err := localIPFor(svc.DeviceURL)
			if err == nil {
				svc.LocalIP = localIP
			}
		}
		if svc.LocalIP == "" {
			failures = append(failures, shortHost(svc.DeviceURL)+": local IP unavailable")
			continue
		}

		result := tryMapWithService(svc, protocol, internalPort, externalPort, description)
		if result.OK {
			return &TCPMapping{
				service:      svc,
				protocol:     protocol,
				externalPort: externalPort,
				internalPort: internalPort,
				externalIP:   result.ExternalIP,
			}, result
		}
		failures = append(failures, shortHost(svc.DeviceURL)+": "+result.Message)
	}

	return nil, Result{Message: "UPnP mapping failed: " + strings.Join(failures, " | ")}
}

func normalizeProtocol(protocol string) string {
	switch strings.ToUpper(strings.TrimSpace(protocol)) {
	case "UDP":
		return "UDP"
	default:
		return "TCP"
	}
}

func discoverServices() ([]gatewayService, error) {
	locations, err := discoverLocations()
	if err != nil {
		return nil, err
	}

	var services []gatewayService
	var failures []string
	for _, location := range locations {
		found, err := servicesFromDevice(location)
		if err != nil {
			failures = append(failures, shortHost(location)+": "+err.Error())
			continue
		}
		services = append(services, found...)
	}
	if len(services) == 0 && len(failures) > 0 {
		return nil, fmt.Errorf("no usable UPnP service: %s", strings.Join(failures, " | "))
	}
	sort.SliceStable(services, func(i, j int) bool {
		return serviceRank(services[i].ServiceType) < serviceRank(services[j].ServiceType)
	})
	return dedupeServices(services), nil
}

func discoverLocations() ([]string, error) {
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	searchTargets := []string{
		"urn:schemas-upnp-org:service:WANIPConnection:2",
		"urn:schemas-upnp-org:service:WANIPConnection:1",
		"urn:schemas-upnp-org:service:WANPPPConnection:1",
		"urn:schemas-upnp-org:device:InternetGatewayDevice:2",
		"urn:schemas-upnp-org:device:InternetGatewayDevice:1",
		"upnp:rootdevice",
		"ssdp:all",
	}
	dst, _ := net.ResolveUDPAddr("udp4", "239.255.255.250:1900")
	for _, st := range searchTargets {
		msg := strings.Join([]string{
			"M-SEARCH * HTTP/1.1",
			"HOST: 239.255.255.250:1900",
			`MAN: "ssdp:discover"`,
			"MX: 2",
			"ST: " + st,
			"", "",
		}, "\r\n")
		_, _ = conn.WriteTo([]byte(msg), dst)
	}

	deadline := time.Now().Add(4 * time.Second)
	_ = conn.SetDeadline(deadline)
	buf := make([]byte, 65535)
	seen := map[string]bool{}
	var locations []string
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			break
		}
		location := headerValue(string(buf[:n]), "location")
		if location == "" || seen[location] {
			continue
		}
		seen[location] = true
		locations = append(locations, location)
	}
	if len(locations) == 0 {
		return nil, fmt.Errorf("no UPnP gateway found")
	}
	return locations, nil
}

func servicesFromDevice(deviceURL string) ([]gatewayService, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(deviceURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := ioutil.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return nil, err
	}

	var doc deviceRoot
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}

	var services []gatewayService
	for _, svc := range collectServices(doc.Device) {
		if !isWANService(svc.ServiceType) || svc.ControlURL == "" {
			continue
		}
		services = append(services, gatewayService{
			DeviceURL:   deviceURL,
			ControlURL:  absoluteURL(deviceURL, svc.ControlURL),
			ServiceType: svc.ServiceType,
		})
	}
	if len(services) == 0 {
		return nil, fmt.Errorf("gateway has no WAN connection service")
	}
	return services, nil
}

func tryMapWithService(svc gatewayService, protocol string, internalPort, externalPort int, description string) Result {
	result := addPortMapping(svc, protocol, internalPort, externalPort, description, 0)
	if result.OK {
		return enrichSuccess(result, svc, protocol, externalPort)
	}

	// Some routers reject an occupied mapping instead of replacing it.
	_ = deletePortMapping(svc, protocol, externalPort)
	result = addPortMapping(svc, protocol, internalPort, externalPort, description, 0)
	if result.OK {
		return enrichSuccess(result, svc, protocol, externalPort)
	}

	// A few routers dislike permanent leases. A 24 hour lease is a good fallback.
	result = addPortMapping(svc, protocol, internalPort, externalPort, description, 86400)
	if result.OK {
		return enrichSuccess(result, svc, protocol, externalPort)
	}
	return result
}

func enrichSuccess(result Result, svc gatewayService, protocol string, port int) Result {
	externalIP, _ := externalIPAddress(svc)
	result.ExternalIP = externalIP
	result.Gateway = shortHost(svc.DeviceURL)
	result.Service = svc.ServiceType
	if externalIP != "" {
		result.Message = fmt.Sprintf("%s port %d mapped by UPnP", protocol, port)
	}
	return result
}

func addPortMapping(svc gatewayService, protocol string, internalPort, externalPort int, description string, leaseSeconds int) Result {
	protocol = normalizeProtocol(protocol)
	body := fmt.Sprintf(`<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
  <s:Body>
    <u:AddPortMapping xmlns:u="%s">
      <NewRemoteHost></NewRemoteHost>
      <NewExternalPort>%d</NewExternalPort>
      <NewProtocol>%s</NewProtocol>
      <NewInternalPort>%d</NewInternalPort>
      <NewInternalClient>%s</NewInternalClient>
      <NewEnabled>1</NewEnabled>
      <NewPortMappingDescription>%s</NewPortMappingDescription>
      <NewLeaseDuration>%d</NewLeaseDuration>
    </u:AddPortMapping>
  </s:Body>
</s:Envelope>`, svc.ServiceType, externalPort, protocol, internalPort, svc.LocalIP, xmlEscape(description), leaseSeconds)

	status, response, err := soap(svc, "AddPortMapping", body, 4096)
	if err != nil {
		return Result{Message: err.Error(), Gateway: shortHost(svc.DeviceURL), Service: svc.ServiceType}
	}
	if status >= 200 && status < 300 {
		return Result{OK: true, Message: protocol + " port mapped by UPnP", Gateway: shortHost(svc.DeviceURL), Service: svc.ServiceType}
	}
	return Result{Message: fmt.Sprintf("router rejected mapping: HTTP %d %s", status, faultSummary(response)), Gateway: shortHost(svc.DeviceURL), Service: svc.ServiceType}
}

func deletePortMapping(svc gatewayService, protocol string, port int) error {
	protocol = normalizeProtocol(protocol)
	body := fmt.Sprintf(`<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
  <s:Body>
    <u:DeletePortMapping xmlns:u="%s">
      <NewRemoteHost></NewRemoteHost>
      <NewExternalPort>%d</NewExternalPort>
      <NewProtocol>%s</NewProtocol>
    </u:DeletePortMapping>
  </s:Body>
</s:Envelope>`, svc.ServiceType, port, protocol)

	status, _, err := soap(svc, "DeletePortMapping", body, 2048)
	if err != nil {
		return err
	}
	if status >= 200 && status < 300 {
		return nil
	}
	return fmt.Errorf("delete mapping rejected: HTTP %d", status)
}

func externalIPAddress(svc gatewayService) (string, error) {
	body := fmt.Sprintf(`<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
  <s:Body>
    <u:GetExternalIPAddress xmlns:u="%s"></u:GetExternalIPAddress>
  </s:Body>
</s:Envelope>`, svc.ServiceType)

	status, data, err := soap(svc, "GetExternalIPAddress", body, 8192)
	if err != nil {
		return "", err
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("external IP rejected: HTTP %d", status)
	}
	type response struct {
		ExternalIP string `xml:"Body>GetExternalIPAddressResponse>NewExternalIPAddress"`
	}
	var parsed response
	if err := xml.Unmarshal(data, &parsed); err != nil {
		return "", err
	}
	if parsed.ExternalIP == "" {
		return "", fmt.Errorf("router did not return external IP")
	}
	return parsed.ExternalIP, nil
}

func soap(svc gatewayService, action, body string, limit int64) (int, []byte, error) {
	req, err := http.NewRequest("POST", svc.ControlURL, strings.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPAction", fmt.Sprintf(`"%s#%s"`, svc.ServiceType, action))

	client := &http.Client{Timeout: 6 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, _ := ioutil.ReadAll(io.LimitReader(resp.Body, limit))
	return resp.StatusCode, data, nil
}

type deviceRoot struct {
	Device device `xml:"device"`
}

type device struct {
	Services []service `xml:"serviceList>service"`
	Devices  []device  `xml:"deviceList>device"`
}

type service struct {
	ServiceType string `xml:"serviceType"`
	ControlURL  string `xml:"controlURL"`
}

func collectServices(d device) []service {
	services := append([]service{}, d.Services...)
	for _, child := range d.Devices {
		services = append(services, collectServices(child)...)
	}
	return services
}

func dedupeServices(services []gatewayService) []gatewayService {
	seen := map[string]bool{}
	var out []gatewayService
	for _, svc := range services {
		key := svc.ControlURL + "|" + svc.ServiceType
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, svc)
	}
	return out
}

func isWANService(serviceType string) bool {
	return strings.Contains(serviceType, "WANIPConnection") || strings.Contains(serviceType, "WANPPPConnection")
}

func serviceRank(serviceType string) int {
	switch {
	case strings.Contains(serviceType, "WANIPConnection:2"):
		return 0
	case strings.Contains(serviceType, "WANIPConnection:1"):
		return 1
	case strings.Contains(serviceType, "WANPPPConnection"):
		return 2
	default:
		return 3
	}
}

func absoluteURL(baseURL, ref string) string {
	u, err := url.Parse(ref)
	if err == nil && u.IsAbs() {
		return ref
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return ref
	}
	return base.ResolveReference(&url.URL{Path: ref}).String()
}

func localIPFor(remoteURL string) (string, error) {
	u, err := url.Parse(remoteURL)
	if err != nil {
		return "", err
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":80"
	}
	conn, err := net.DialTimeout("udp4", host, 2*time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	addr := conn.LocalAddr().(*net.UDPAddr)
	return addr.IP.String(), nil
}

func headerValue(raw, name string) string {
	name = strings.ToLower(name)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 && strings.ToLower(parts[0]) == name {
			return strings.TrimSpace(parts[1])
		}
	}
	return ""
}

func faultSummary(data []byte) string {
	type fault struct {
		ErrorCode        string `xml:"Body>Fault>detail>UPnPError>errorCode"`
		ErrorDescription string `xml:"Body>Fault>detail>UPnPError>errorDescription"`
		FaultString      string `xml:"Body>Fault>faultstring"`
	}
	var parsed fault
	if err := xml.Unmarshal(data, &parsed); err == nil {
		switch {
		case parsed.ErrorCode != "" && parsed.ErrorDescription != "":
			return parsed.ErrorCode + " " + parsed.ErrorDescription
		case parsed.ErrorCode != "":
			return parsed.ErrorCode
		case parsed.FaultString != "":
			return parsed.FaultString
		}
	}
	text := strings.TrimSpace(string(data))
	if len(text) > 240 {
		text = text[:240]
	}
	return text
}

func shortHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	return u.Host
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
