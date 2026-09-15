package server

import (
	"errors"
	"html/template"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"imagepadserver/internal/airplay"
)

var airplayLicensePage = template.Must(template.New("airplay-licenses").Parse(`<!doctype html>
<html lang="ja"><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<title>AirPlay ライセンスと対応ソース</title>
<style>body{font:16px system-ui;max-width:960px;margin:32px auto;padding:0 20px;line-height:1.7}a{overflow-wrap:anywhere}li{margin:8px 0}</style>
<h1>AirPlay ライセンスと対応ソース</h1>
<p>ImagePadServer本体のライセンスはMITです。同梱のUxPlay、GStreamer、x264などには各コンポーネントのライセンスが適用されます。</p>
<p>通常の利用に再ビルドは不要です。以下のライセンス本文と取得案内はexeに内蔵され、オフラインでも確認できます。</p>
{{with .Sources}}<p>完全な対応ソース・パッチ・再ビルド手順: <a href="{{.SourceArchive.URL}}">{{.SourceArchive.AssetName}}</a>（同じGitHubリリース、無償）</p>
<p>SHA-256: <code>{{.SourceArchive.SHA256}}</code></p>{{end}}
<p>対応一覧は source-manifest.json、詳細は SOURCE-OFFER.md を参照してください。</p>
<ul>{{range .Assets}}<li><a href="?file={{.Name}}">{{.Name}}</a> ({{.Size}} bytes)</li>{{end}}</ul></html>`))

// License materials are public, contain no user data, and remain accessible before provisioning.
func (s *Server) handleAirPlayLicenses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
	name := r.URL.Query().Get("file")
	if name != "" {
		reader, size, err := airplay.OpenRuntimeLicenseAsset(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer reader.Close()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if strings.HasPrefix(name, "sources/") && (strings.HasSuffix(name, ".gz") || strings.HasSuffix(name, ".tgz") || strings.HasSuffix(name, ".xz") || strings.HasSuffix(name, ".bz2") || strings.HasSuffix(name, ".zip") || strings.HasSuffix(name, ".zst")) {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(name)}))
		}
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		if r.Method == http.MethodGet {
			_, _ = io.Copy(w, reader)
		}
		return
	}
	assets, err := airplay.RuntimeLicenseAssets()
	if err != nil {
		http.Error(w, "この開発ビルドにはAirPlayランタイムが内蔵されていません。", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	sources, err := airplay.ReadRuntimeSourceDistribution()
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		http.Error(w, "対応ソースの取得情報を読み込めません。", http.StatusInternalServerError)
		return
	}
	if r.Method == http.MethodGet {
		_ = airplayLicensePage.Execute(w, struct {
			Assets  []airplay.RuntimeLicenseAsset
			Sources *airplay.RuntimeSourceDistribution
		}{assets, sources})
	}
}
