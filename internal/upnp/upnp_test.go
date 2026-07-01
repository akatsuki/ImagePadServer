package upnp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestIsGloballyRoutableIPv4(t *testing.T) {
	tests := map[string]bool{
		"8.8.8.8":        true,
		"127.0.0.1":      false,
		"10.0.0.1":       false,
		"172.16.0.1":     false,
		"192.168.1.1":    false,
		"100.64.0.1":     false,
		"169.254.1.1":    false,
		"224.0.0.1":      false,
		"2001:db8::1":    false,
		"not-an-address": false,
	}
	for input, want := range tests {
		if got := IsGloballyRoutableIPv4(input); got != want {
			t.Errorf("IsGloballyRoutableIPv4(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestTCPMappingCloseDeletesOwnedMappingOnce(t *testing.T) {
	var deleteCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch action := r.Header.Get("SOAPAction"); {
		case strings.Contains(action, "#AddPortMapping"):
			w.WriteHeader(http.StatusOK)
		case strings.Contains(action, "#GetExternalIPAddress"):
			_, _ = io.WriteString(w, `<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/">
  <s:Body>
    <u:GetExternalIPAddressResponse xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1">
      <NewExternalIPAddress>8.8.8.8</NewExternalIPAddress>
    </u:GetExternalIPAddressResponse>
  </s:Body>
</s:Envelope>`)
		case strings.Contains(action, "#DeletePortMapping"):
			deleteCalls.Add(1)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected action", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	service := gatewayService{
		DeviceURL:   server.URL,
		ControlURL:  server.URL,
		ServiceType: "urn:schemas-upnp-org:service:WANIPConnection:1",
		LocalIP:     "192.168.1.20",
	}
	mapping, result := mapTCPWithServices([]gatewayService{service}, 49152, 52000, "ImagePadServer RTSP")
	if !result.OK || mapping == nil {
		t.Fatalf("map result = %#v, mapping = %#v", result, mapping)
	}
	if got, want := mapping.ExternalIP(), "8.8.8.8"; got != want {
		t.Fatalf("ExternalIP = %q, want %q", got, want)
	}
	if got, want := mapping.ExternalPort(), 52000; got != want {
		t.Fatalf("ExternalPort = %d, want %d", got, want)
	}
	if got, want := mapping.InternalPort(), 49152; got != want {
		t.Fatalf("InternalPort = %d, want %d", got, want)
	}
	if err := mapping.Close(); err != nil {
		t.Fatal(err)
	}
	if err := mapping.Close(); err != nil {
		t.Fatal(err)
	}
	if got := deleteCalls.Load(); got != 1 {
		t.Fatalf("DeletePortMapping calls = %d, want 1", got)
	}
}

func TestMapTCPUsesDistinctInternalAndExternalPorts(t *testing.T) {
	var addBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch action := r.Header.Get("SOAPAction"); {
		case strings.Contains(action, "#AddPortMapping"):
			addBody = string(body)
			w.WriteHeader(http.StatusOK)
		case strings.Contains(action, "#GetExternalIPAddress"):
			_, _ = io.WriteString(w, `<Envelope><Body><GetExternalIPAddressResponse><NewExternalIPAddress>8.8.4.4</NewExternalIPAddress></GetExternalIPAddressResponse></Body></Envelope>`)
		case strings.Contains(action, "#DeletePortMapping"):
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected action", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	service := gatewayService{
		DeviceURL:   server.URL,
		ControlURL:  server.URL,
		ServiceType: "urn:schemas-upnp-org:service:WANIPConnection:1",
		LocalIP:     "192.168.1.20",
	}
	mapping, result := mapTCPWithServices([]gatewayService{service}, 49152, 52000, "ImagePadServer RTSP")
	if !result.OK || mapping == nil {
		t.Fatalf("map result = %#v, mapping = %#v", result, mapping)
	}
	t.Cleanup(func() { _ = mapping.Close() })
	for _, want := range []string{
		"<NewExternalPort>52000</NewExternalPort>",
		"<NewInternalPort>49152</NewInternalPort>",
	} {
		if !strings.Contains(addBody, want) {
			t.Fatalf("AddPortMapping body missing %q:\n%s", want, addBody)
		}
	}
}

func TestMapUDPUsesUDPProtocolAndDeletesUDP(t *testing.T) {
	var addBody, deleteBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch action := r.Header.Get("SOAPAction"); {
		case strings.Contains(action, "#AddPortMapping"):
			addBody = string(body)
			w.WriteHeader(http.StatusOK)
		case strings.Contains(action, "#GetExternalIPAddress"):
			_, _ = io.WriteString(w, `<Envelope><Body><GetExternalIPAddressResponse><NewExternalIPAddress>8.8.4.4</NewExternalIPAddress></GetExternalIPAddressResponse></Body></Envelope>`)
		case strings.Contains(action, "#DeletePortMapping"):
			deleteBody = string(body)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected action", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	service := gatewayService{
		DeviceURL:   server.URL,
		ControlURL:  server.URL,
		ServiceType: "urn:schemas-upnp-org:service:WANIPConnection:1",
		LocalIP:     "192.168.1.20",
	}
	mapping, result := mapUDPWithServices([]gatewayService{service}, 49153, 52001, "ImagePadServer RTSP RTP")
	if !result.OK || mapping == nil {
		t.Fatalf("map result = %#v, mapping = %#v", result, mapping)
	}
	if err := mapping.Close(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<NewProtocol>UDP</NewProtocol>",
		"<NewExternalPort>52001</NewExternalPort>",
		"<NewInternalPort>49153</NewInternalPort>",
	} {
		if !strings.Contains(addBody, want) {
			t.Fatalf("AddPortMapping body missing %q:\n%s", want, addBody)
		}
	}
	if !strings.Contains(deleteBody, "<NewProtocol>UDP</NewProtocol>") {
		t.Fatalf("DeletePortMapping body missing UDP protocol:\n%s", deleteBody)
	}
}

func TestAbsoluteURLPreservesRelativeQueryAndFragment(t *testing.T) {
	base := "http://192.168.1.1:1900/rootDesc.xml"
	got := absoluteURL(base, "upnp/control/WANIPConn1?service=wan#control")
	want := "http://192.168.1.1:1900/upnp/control/WANIPConn1?service=wan#control"
	if got != want {
		t.Fatalf("absoluteURL = %q, want %q", got, want)
	}
}

func TestServicesFromDeviceRejectsNonOKDescription(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`<root><device><serviceList><service><serviceType>urn:schemas-upnp-org:service:WANIPConnection:1</serviceType><controlURL>/control</controlURL></service></serviceList></device></root>`))
	}))
	defer srv.Close()

	if services, err := servicesFromDevice(srv.URL); err == nil {
		t.Fatalf("servicesFromDevice returned %d services for HTTP 500, want error", len(services))
	}
}
