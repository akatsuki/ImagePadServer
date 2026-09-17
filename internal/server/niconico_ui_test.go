package server

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestNiconicoCommentOptionIsURLGatedAndSubmitted(t *testing.T) {
	for _, want := range []string{
		`id="niconicoCommentsOption"`,
		`id="niconicoCommentsEnabled"`,
		`function isNiconicoWatchURL(raw)`,
		`niconicoComments: { enabled: !!(niconicoCommentsEnabled`,
		`uploadMode === 'link'`,
		`setMediaIntent('video', false, false)`,
		`取得時点のコメントを動画へ焼き込みます`,
	} {
		if !strings.Contains(indexHTML, want) {
			t.Fatalf("index HTML is missing %q", want)
		}
	}
}

func TestNiconicoBurnInDefaultAndURLTransitions(t *testing.T) {
	if !strings.Contains(indexHTML, `id="niconicoCommentsEnabled" role="switch" checked`) {
		t.Fatal("comment burn-in must be an initially enabled switch")
	}
	linkStart := strings.Index(indexHTML, `id="linkUploadPanel"`)
	linkEnd := strings.Index(indexHTML, `id="obsUploadPanel"`)
	option := strings.Index(indexHTML, `id="niconicoCommentsOption"`)
	if option < linkStart || option > linkEnd {
		t.Fatal("comment switch must be inside the URL input panel")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required for dashboard handler checks")
	}
	start := strings.Index(dashboardScriptUploadEvents, "    function isNiconicoWatchURL(raw)")
	end := strings.Index(dashboardScriptUploadEvents, "    function uploadFromFile(action)")
	uploadStart := strings.Index(dashboardScriptUploadEvents, "    function uploadFromLink(action, overrideURL)")
	uploadEnd := strings.Index(dashboardScriptUploadEvents[uploadStart:], "\n    }") + uploadStart + len("\n    }")
	script := `
const assert = require('node:assert/strict');
let mediaIntent = 'image', uploadMode = 'file';
const state = {videoPlayerEnabled:true}, imageURLInput = {value:''};
const niconicoCommentsOption = {}, niconicoCommentsEnabled = {checked:true}, niconicoCommentsHint = {};
function setMediaIntent(value){mediaIntent=value}
const uploadForm = {};
class FormData { get(){return null} }
function shareModeForUpload(){return 'local'}
function apiFetch(url,options){return JSON.parse(options.body)}
` + dashboardScriptUploadEvents[start:end] + dashboardScriptUploadEvents[uploadStart:uploadEnd] + `
updateNiconicoCommentsOption();
assert.equal(niconicoCommentsEnabled.checked,true,'initial hidden state must preserve default on');
uploadMode='link';imageURLInput.value='https://www.nicovideo.jp/watch/sm9';updateNiconicoCommentsOption();
assert.equal(mediaIntent,'video');assert.equal(niconicoCommentsOption.hidden,false);
assert.equal(uploadFromLink('publish').niconicoComments.enabled,true);
niconicoCommentsEnabled.checked=false;updateNiconicoCommentsOption();
assert.equal(uploadFromLink('queue').niconicoComments.enabled,false);
imageURLInput.value='';updateNiconicoCommentsOption();
imageURLInput.value='https://nico.ms/sm9';updateNiconicoCommentsOption();
assert.equal(niconicoCommentsEnabled.checked,false,'editing URL must preserve explicit off');
niconicoCommentsEnabled.checked=true;
imageURLInput.value='https://example.com/movie.mp4';updateNiconicoCommentsOption();
assert.equal(niconicoCommentsOption.hidden,true);
assert.equal(uploadFromLink('publish').niconicoComments.enabled,false,'non-Nico URL must never enable burn-in');
imageURLInput.value='https://www.nicovideo.jp/watch/sm9';updateNiconicoCommentsOption();
assert.equal(uploadFromLink('publish','https://example.com/movie.mp4').niconicoComments.enabled,false,'actual target URL must be checked');
uploadMode='file';updateNiconicoCommentsOption();assert.equal(niconicoCommentsOption.hidden,true);
assert.equal(uploadFromLink('publish').niconicoComments.enabled,false);
`
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "-e", script)
	hideDashboardTestWindow(cmd)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("comment toggle handlers: %v\n%s", err, output)
	}
}
