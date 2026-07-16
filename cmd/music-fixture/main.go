package main

import (
    "crypto/sha256"
    "encoding/binary"
    "encoding/json"
    "flag"
    "fmt"
    "math"
    "os"
    "path/filepath"
)

type manifest struct { Fixtures []fixture `json:"fixtures"` }
type fixture struct { ID string `json:"id"`; Seed int64 `json:"seed"`; Duration float64 `json:"duration_s"` }

func main() {
    manifestPath := flag.String("manifest", "testdata/music-render/manifest.json", "fixture manifest")
    out := flag.String("out", ".tmp/music-fixtures", "output directory")
    flag.Parse()
    data, err := os.ReadFile(*manifestPath); if err != nil { panic(err) }
    var m manifest; if err := json.Unmarshal(data, &m); err != nil { panic(err) }
    if err := os.MkdirAll(*out, 0755); err != nil { panic(err) }
    for _, f := range m.Fixtures { if err := writeFixture(*out, f); err != nil { panic(err) } }
    sum := sha256.Sum256(data)
    report := map[string]any{"manifest_sha256": fmt.Sprintf("%x", sum), "fixtures": len(m.Fixtures), "generator": "cmd/music-fixture"}
    encoded, _ := json.MarshalIndent(report, "", "  "); _ = os.WriteFile(filepath.Join(*out, "analysis-report.json"), append(encoded, '\n'), 0644)
}

func writeFixture(dir string, f fixture) error {
    n := int(math.Round(f.Duration * 48000)); path := filepath.Join(dir, f.ID+".wav")
    file, err := os.Create(path); if err != nil { return err }; defer file.Close()
    dataBytes := n * 2 * 2
    h := make([]byte, 44); copy(h, []byte("RIFF")); binary.LittleEndian.PutUint32(h[4:], uint32(36+dataBytes)); copy(h[8:], []byte("WAVEfmt "))
    binary.LittleEndian.PutUint32(h[16:], 16); binary.LittleEndian.PutUint16(h[20:], 1); binary.LittleEndian.PutUint16(h[22:], 2)
    binary.LittleEndian.PutUint32(h[24:], 48000); binary.LittleEndian.PutUint32(h[28:], 48000*4); binary.LittleEndian.PutUint16(h[32:], 4); binary.LittleEndian.PutUint16(h[34:], 16); copy(h[36:], []byte("data")); binary.LittleEndian.PutUint32(h[40:], uint32(dataBytes))
    if _, err := file.Write(h); err != nil { return err }
    freq := 220.0 + float64(f.Seed%7)*31
    for i := 0; i < n; i++ { v := int16(12000 * math.Sin(2*math.Pi*freq*float64(i)/48000)); var b [2]byte; binary.LittleEndian.PutUint16(b[:], uint16(v)); if _, err := file.Write(b[:]); err != nil { return err }; if _, err := file.Write(b[:]); err != nil { return err } }
    return nil
}
