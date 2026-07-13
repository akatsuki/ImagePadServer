package upnp

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

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

func TestDiscoverLocationsUsesRaceByDefault(t *testing.T) {
	oldSSDP := discoverLocationsFunc
	oldDirect := discoverGatewayLocationsDirectFunc
	discoverLocationsFunc = func() ([]string, error) {
		time.Sleep(250 * time.Millisecond)
		return []string{"http://192.168.0.1:1900/rootDesc.xml"}, nil
	}
	discoverGatewayLocationsDirectFunc = func() ([]string, error) {
		return []string{"http://192.168.0.1:80/InternetGatewayDevice.xml"}, nil
	}
	t.Cleanup(func() {
		discoverLocationsFunc = oldSSDP
		discoverGatewayLocationsDirectFunc = oldDirect
	})

	start := time.Now()
	locations, err := discoverGatewayLocations()
	if err != nil {
		t.Fatalf("discoverGatewayLocations error = %v", err)
	}
	if time.Since(start) > 150*time.Millisecond {
		t.Fatalf("default discovery waited for slow SSDP path")
	}
	if got, want := locations[0], "http://192.168.0.1:80/InternetGatewayDevice.xml"; got != want {
		t.Fatalf("location = %q, want %q", got, want)
	}
}

func TestBenchmarkMappingMeasuresDiscoveryMappingAndCleanup(t *testing.T) {
	var addCalls atomic.Int32
	var deleteCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch action := r.Header.Get("SOAPAction"); {
		case strings.Contains(action, "#AddPortMapping"):
			addCalls.Add(1)
			w.WriteHeader(http.StatusOK)
		case strings.Contains(action, "#GetExternalIPAddress"):
			_, _ = io.WriteString(w, `<Envelope><Body><GetExternalIPAddressResponse><NewExternalIPAddress>8.8.8.8</NewExternalIPAddress></GetExternalIPAddressResponse></Body></Envelope>`)
		case strings.Contains(action, "#DeletePortMapping"):
			deleteCalls.Add(1)
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected action", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	oldDiscoverLegacy := serviceDiscovererLegacy
	serviceDiscovererLegacy = func() ([]gatewayService, string, error) {
		return []gatewayService{{
			DeviceURL:   server.URL,
			ControlURL:  server.URL,
			ServiceType: "urn:schemas-upnp-org:service:WANIPConnection:1",
			LocalIP:     "192.168.1.20",
		}}, "legacy-ssdp", nil
	}
	t.Cleanup(func() { serviceDiscovererLegacy = oldDiscoverLegacy })

	result := BenchmarkMapping(BenchmarkOptions{
		Mode:         DiscoveryLegacy,
		Protocol:     "TCP",
		InternalPort: 49152,
		ExternalPort: 52000,
		Description:  "ImagePadServer UPnP benchmark",
	})
	if !result.OK {
		t.Fatalf("BenchmarkMapping result = %#v", result)
	}
	if got, want := result.DiscoverySource, "legacy-ssdp"; got != want {
		t.Fatalf("DiscoverySource = %q, want %q", got, want)
	}
	if result.DiscoveryDuration <= 0 || result.MappingDuration <= 0 || result.TotalDuration <= 0 {
		t.Fatalf("durations were not populated: %#v", result)
	}
	if result.CleanupError != "" {
		t.Fatalf("CleanupError = %q", result.CleanupError)
	}
	if got := addCalls.Load(); got != 1 {
		t.Fatalf("AddPortMapping calls = %d, want 1", got)
	}
	if got := deleteCalls.Load(); got != 1 {
		t.Fatalf("DeletePortMapping calls = %d, want 1", got)
	}
}

func TestCleanupImagePadRTSPMappingsDeletesOnlyOwnedRTSPEntries(t *testing.T) {
	type mappingEntry struct {
		protocol    string
		external    int
		internal    int
		description string
	}
	entries := []mappingEntry{
		{protocol: "TCP", external: 49714, internal: 49714, description: "ImagePadServer RTSP TCP"},
		{protocol: "UDP", external: 49715, internal: 49715, description: "ImagePadServer RTSP RTP"},
		{protocol: "UDP", external: 49716, internal: 49716, description: "ImagePadServer RTSP RTCP"},
		{protocol: "TCP", external: 38810, internal: 38810, description: "Virtual Desktop"},
	}
	var deleted []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := r.Header.Get("SOAPAction")
		body, _ := io.ReadAll(r.Body)
		switch {
		case strings.Contains(action, "#GetGenericPortMappingEntry"):
			index := extractSOAPInt(string(body), "NewPortMappingIndex")
			if index < 0 || index >= len(entries) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `<Envelope><Body><Fault><detail><UPnPError><errorCode>713</errorCode><errorDescription>SpecifiedArrayIndexInvalid</errorDescription></UPnPError></detail></Fault></Body></Envelope>`)
				return
			}
			entry := entries[index]
			_, _ = fmt.Fprintf(w, `<Envelope><Body><GetGenericPortMappingEntryResponse><NewRemoteHost></NewRemoteHost><NewExternalPort>%d</NewExternalPort><NewProtocol>%s</NewProtocol><NewInternalPort>%d</NewInternalPort><NewInternalClient>192.168.0.234</NewInternalClient><NewEnabled>1</NewEnabled><NewPortMappingDescription>%s</NewPortMappingDescription><NewLeaseDuration>0</NewLeaseDuration></GetGenericPortMappingEntryResponse></Body></Envelope>`,
				entry.external, entry.protocol, entry.internal, entry.description)
		case strings.Contains(action, "#DeletePortMapping"):
			external := extractSOAPInt(string(body), "NewExternalPort")
			protocol := extractSOAPText(string(body), "NewProtocol")
			deleted = append(deleted, fmt.Sprintf("%s:%d", protocol, external))
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected action", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	oldDiscover := serviceDiscovererRace
	serviceDiscovererRace = func() ([]gatewayService, string, error) {
		return []gatewayService{{
			DeviceURL:   server.URL,
			ControlURL:  server.URL,
			ServiceType: "urn:schemas-upnp-org:service:WANIPConnection:1",
			LocalIP:     "192.168.0.234",
		}}, "test", nil
	}
	t.Cleanup(func() { serviceDiscovererRace = oldDiscover })

	count, err := CleanupImagePadRTSPMappings()
	if err != nil {
		t.Fatalf("CleanupImagePadRTSPMappings error = %v", err)
	}
	if count != 3 {
		t.Fatalf("deleted count = %d, want 3", count)
	}
	got := strings.Join(deleted, ",")
	want := "TCP:49714,UDP:49715,UDP:49716"
	if got != want {
		t.Fatalf("deleted = %s, want %s", got, want)
	}
}

func extractSOAPInt(body, name string) int {
	text := extractSOAPText(body, name)
	var n int
	_, _ = fmt.Sscanf(text, "%d", &n)
	return n
}

func extractSOAPText(body, name string) string {
	startTag := "<" + name + ">"
	endTag := "</" + name + ">"
	start := strings.Index(body, startTag)
	if start < 0 {
		return ""
	}
	start += len(startTag)
	end := strings.Index(body[start:], endTag)
	if end < 0 {
		return ""
	}
	return body[start : start+end]
}
