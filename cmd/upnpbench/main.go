package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"imagepadserver/internal/upnp"
)

func main() {
	var modeFlag string
	var protocolFlag string
	var internalPort int
	var externalPort int
	var description string

	flag.StringVar(&modeFlag, "mode", "both", "discovery mode: legacy, race, or both")
	flag.StringVar(&protocolFlag, "protocol", "TCP", "mapping protocol: TCP, UDP, or both")
	flag.IntVar(&internalPort, "internal-port", 49152, "local port to publish")
	flag.IntVar(&externalPort, "external-port", 52000, "router external port to create temporarily")
	flag.StringVar(&description, "description", "ImagePadServer UPnP benchmark", "UPnP port mapping description")
	flag.Parse()

	modes, err := parseModes(modeFlag)
	if err != nil {
		exitUsage(err)
	}
	protocols, err := parseProtocols(protocolFlag)
	if err != nil {
		exitUsage(err)
	}
	if internalPort <= 0 || externalPort <= 0 {
		exitUsage(fmt.Errorf("internal-port and external-port must be positive"))
	}

	fmt.Println("ImagePadServer UPnP benchmark")
	fmt.Println("Creates a temporary router port mapping, measures it, then deletes it.")
	fmt.Println()

	for _, mode := range modes {
		for _, protocol := range protocols {
			result := upnp.BenchmarkMapping(upnp.BenchmarkOptions{
				Mode:         mode,
				Protocol:     protocol,
				InternalPort: internalPort,
				ExternalPort: externalPort,
				Description:  description,
			})
			printResult(result)
		}
	}
}

func parseModes(raw string) ([]upnp.DiscoveryMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "both":
		return []upnp.DiscoveryMode{upnp.DiscoveryLegacy, upnp.DiscoveryRace}, nil
	case "legacy":
		return []upnp.DiscoveryMode{upnp.DiscoveryLegacy}, nil
	case "race":
		return []upnp.DiscoveryMode{upnp.DiscoveryRace}, nil
	default:
		return nil, fmt.Errorf("unknown mode %q", raw)
	}
}

func parseProtocols(raw string) ([]string, error) {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "", "TCP":
		return []string{"TCP"}, nil
	case "UDP":
		return []string{"UDP"}, nil
	case "BOTH":
		return []string{"TCP", "UDP"}, nil
	default:
		return nil, fmt.Errorf("unknown protocol %q", raw)
	}
}

func printResult(result upnp.BenchmarkResult) {
	fmt.Printf("%s/%s:\n", result.Mode, result.Protocol)
	fmt.Printf("  ok:        %v\n", result.OK)
	if result.Message != "" {
		fmt.Printf("  message:   %s\n", result.Message)
	}
	if result.DiscoverySource != "" {
		fmt.Printf("  source:    %s\n", result.DiscoverySource)
	}
	if result.Gateway != "" {
		fmt.Printf("  gateway:   %s\n", result.Gateway)
	}
	if result.Service != "" {
		fmt.Printf("  service:   %s\n", result.Service)
	}
	if result.ExternalIP != "" {
		fmt.Printf("  external:  %s\n", result.ExternalIP)
	}
	fmt.Printf("  discovery: %s\n", formatDuration(result.DiscoveryDuration))
	fmt.Printf("  mapping:   %s\n", formatDuration(result.MappingDuration))
	if result.CleanupDuration > 0 {
		fmt.Printf("  cleanup:   %s\n", formatDuration(result.CleanupDuration))
	}
	if result.CleanupError != "" {
		fmt.Printf("  cleanup error: %s\n", result.CleanupError)
	}
	fmt.Printf("  total:     %s\n", formatDuration(result.TotalDuration))
	fmt.Println()
}

func formatDuration(value time.Duration) string {
	if value <= 0 {
		return "0s"
	}
	if value < time.Millisecond {
		return value.String()
	}
	return value.Round(time.Millisecond).String()
}

func exitUsage(err error) {
	fmt.Fprintf(os.Stderr, "upnpbench: %v\n\n", err)
	flag.Usage()
	os.Exit(2)
}
