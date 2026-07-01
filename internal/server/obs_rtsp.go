package server

import (
	"fmt"

	"imagepadserver/internal/obsrtmp"
	"imagepadserver/internal/upnp"
)

type rtspMappingHandle interface {
	ExternalIP() string
	ExternalPort() int
	Close() error
}

type rtspPortMapper func(protocol string, internalPort, externalPort int, description string) (rtspMappingHandle, upnp.Result)

type rtspMappingSet struct {
	control rtspMappingHandle
	owned   []rtspMappingHandle
}

func (m *rtspMappingSet) ExternalIP() string {
	if m == nil || m.control == nil {
		return ""
	}
	return m.control.ExternalIP()
}

func (m *rtspMappingSet) ExternalPort() int {
	if m == nil || m.control == nil {
		return 0
	}
	return m.control.ExternalPort()
}

func (m *rtspMappingSet) Close() error {
	if m == nil {
		return nil
	}
	var firstErr error
	for _, mapping := range m.owned {
		if mapping == nil {
			continue
		}
		if err := mapping.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *Server) handleRTSPReady(endpoint obsrtmp.RTSPEndpoint) {
	s.mu.Lock()
	s.rtspReadySeq++
	seq := s.rtspReadySeq
	mapPort := s.mapRTSPPort
	setURL := s.setRTSPURL
	s.mu.Unlock()

	if mapPort == nil || setURL == nil {
		return
	}
	mapping, result := mapRTSPCompatibilityPorts(mapPort, endpoint)
	if mapping == nil || !result.OK {
		message := "RTSP is available on LAN/Tailscale; UPnP publication failed"
		if result.Message != "" {
			message += ": " + result.Message
		}
		setURL(endpoint.SessionID, endpoint.LocalURL, message)
		return
	}
	if !upnp.IsGloballyRoutableIPv4(mapping.ExternalIP()) {
		_ = mapping.Close()
		setURL(endpoint.SessionID, endpoint.LocalURL,
			"RTSP is available on LAN/Tailscale; CGNAT or upstream NAT prevents direct publication.")
		return
	}

	s.mu.Lock()
	if seq != s.rtspReadySeq {
		s.mu.Unlock()
		_ = mapping.Close()
		return
	}
	previous := s.rtspMap
	s.rtspMap = mapping
	s.rtspSessionID = endpoint.SessionID
	s.mu.Unlock()
	if previous != nil {
		_ = previous.Close()
	}

	publicURL := fmt.Sprintf("rtsp://%s:%d/%s", mapping.ExternalIP(), mapping.ExternalPort(), endpoint.Path)
	if !setURL(endpoint.SessionID, publicURL,
		"RTSP TCP/UDP is published through UPnP at "+mapping.ExternalIP()+".") {
		s.closeRTSPMapping(endpoint.SessionID)
	}
}

func mapRTSPCompatibilityPorts(mapPort rtspPortMapper, endpoint obsrtmp.RTSPEndpoint) (rtspMappingHandle, upnp.Result) {
	type requestedMapping struct {
		protocol    string
		internal    int
		external    int
		description string
	}
	requests := []requestedMapping{
		{protocol: "TCP", internal: endpoint.Port, external: endpoint.Port, description: "ImagePadServer RTSP TCP"},
	}
	if endpoint.RTPPort > 0 {
		requests = append(requests, requestedMapping{protocol: "UDP", internal: endpoint.RTPPort, external: endpoint.RTPPort, description: "ImagePadServer RTSP RTP"})
	}
	if endpoint.RTCPPort > 0 {
		requests = append(requests, requestedMapping{protocol: "UDP", internal: endpoint.RTCPPort, external: endpoint.RTCPPort, description: "ImagePadServer RTSP RTCP"})
	}

	var owned []rtspMappingHandle
	for i, request := range requests {
		mapping, result := mapPort(request.protocol, request.internal, request.external, request.description)
		if mapping == nil || !result.OK {
			for _, previous := range owned {
				_ = previous.Close()
			}
			return nil, result
		}
		owned = append(owned, mapping)
		if i == 0 && !upnp.IsGloballyRoutableIPv4(mapping.ExternalIP()) {
			return &rtspMappingSet{control: mapping, owned: owned}, result
		}
	}
	return &rtspMappingSet{control: owned[0], owned: owned}, upnp.Result{
		OK:         true,
		Message:    "RTSP TCP/UDP ports mapped by UPnP",
		ExternalIP: owned[0].ExternalIP(),
	}
}

func (s *Server) handleRTSPDone(sessionID string) {
	s.closeRTSPMapping(sessionID)
}

func (s *Server) closeRTSPMapping(sessionID string) {
	s.mu.Lock()
	if sessionID != "" && s.rtspSessionID != sessionID {
		s.mu.Unlock()
		return
	}
	mapping := s.rtspMap
	s.rtspMap = nil
	s.rtspSessionID = ""
	s.rtspReadySeq++
	s.mu.Unlock()
	if mapping != nil {
		_ = mapping.Close()
	}
}
