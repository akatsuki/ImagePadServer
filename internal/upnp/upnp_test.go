package upnp

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
