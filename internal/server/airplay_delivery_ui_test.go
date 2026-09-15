package server

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Runs the shipped handlers in Node with minimal DOM/HTTP boundaries. This
// proves handler behavior, not a rendered browser or real media playback.
func TestAirPlayDeliveryUIWaitsForCommittedGeneration(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required for dashboard handler checks")
	}
	start := strings.Index(dashboardScriptUploadEvents, "    async function updateOBSLatency(mode)")
	end := strings.Index(dashboardScriptUploadEvents, "    function showRTSPRiskDialog()")
	if start < 0 || end <= start {
		t.Fatal("latency handler unavailable")
	}
	script := `
const assert = require('node:assert/strict');
let uploadMode = 'airplay', resets = 0, refreshAgain = false;
const state = {airplay:{enabled:true},airplayQuality:null};
const airplayQualityRow = {}, airplayQualityStatus = {}, toast = {};
const airplayQualityMode = {value:'720',disabled:false,addEventListener(){}};
const obsLatencyMode = {value:'rtsp-ultra',disabled:false};
const airplayStartButton = null, airplayEndButton = null, airplayRetryButton = null;
function resetOBSPreview(){resets++}
function scheduleRefresh(){}
function showToast(){}
function announceLocalChange(){}
async function refreshState(){return true}
let appliedOBS;
function applyOBS(data){appliedOBS=data}
let response;
async function apiFetch(){ return {ok:true,status:202,text:async()=>JSON.stringify(response),json:async()=>response}; }
` + dashboardScriptAirPlay + dashboardScriptUploadEvents[start:end] + `
(async()=>{
 const initial={managed:true,sessionID:'same',generation:1,desiredMode:'720',activeMode:'720',activeHeight:720,desiredLatencyMode:'rtsp-ultra',changePending:false,restartRequired:false};
 state.airplayQuality=initial;
 applyAirPlayQuality(initial);
 assert.equal(resets,0);
 const pending={...initial,desiredMode:'1080',changePending:true,restartRequired:true};
 response={ok:true,airplayQuality:pending};
 airplayQualityMode.value='1080';
 await saveAirPlayQuality();
 assert.equal(airplayQualityMode.disabled,true,'HTTP completion must not clear server pending');
 assert.equal(obsLatencyMode.disabled,true,'second setting shares the pending transaction');
 assert.equal(resets,0,'uncommitted settings reset preview');
 assert.match(airplayQualityStatus.textContent,/切替中/);
 const committed={...pending,generation:2,activeMode:'1080',activeHeight:1080,changePending:false,restartRequired:false};
 applyAirPlayQuality({...committed,changePending:true});
 assert.equal(resets,0,'commit still retiring must not reset yet');
 state.airplayQuality=committed;
 applyAirPlayQuality(committed);
 applyAirPlayQuality(committed);
 assert.equal(resets,1,'exactly one reset for a new committed generation');
 assert.equal(airplayQualityMode.disabled,false);
 response={obs:{latency:{mode:'rtsp-ultra'}},airplayQuality:{...committed,desiredLatencyMode:'hls',changePending:true,restartRequired:true}};
 await updateOBSLatency('hls');
 assert.deepEqual(appliedOBS,response.obs,'managed response must unwrap OBS state');
 assert.equal(resets,1,'latency acceptance must not reset preview');
 assert.equal(obsLatencyMode.disabled,true,'latency finally must preserve server pending');
 assert.equal(obsLatencyMode.value,'hls','pending selector must show saved desired latency, not old active latency');
 // Failure without a commit must restore controls without replacing playback.
 state.airplayQuality={...response.airplayQuality,changePending:false};
 applyAirPlayQuality(state.airplayQuality);
 assert.equal(obsLatencyMode.disabled,false);
 assert.equal(airplayQualityMode.disabled,false);
 assert.equal(resets,1);
 assert.match(airplayQualityStatus.textContent,/未適用/);
 // Compensation is itself a committed generation, even with old settings.
 state.airplayQuality={...state.airplayQuality,generation:3};
 applyAirPlayQuality(state.airplayQuality);
 applyAirPlayQuality(state.airplayQuality);
 assert.equal(resets,2);
 // Ordinary OBS keeps its old, non-envelope response and immediate reset.
 state.airplayQuality={desiredMode:'720',activeHeight:720,changePending:false};
 response={latency:{mode:'hls'}};
 await updateOBSLatency('hls');
 assert.deepEqual(state.obs,{latency:{mode:'hls'}});
 assert.equal(resets,3);
 assert.equal(obsLatencyMode.disabled,false);
})().catch(error=>{console.error(error);process.exitCode=1});
`
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "-e", script)
	hideDashboardTestWindow(cmd)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dashboard handlers: %v\n%s", err, output)
	}
}
