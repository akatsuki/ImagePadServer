package server

import (
	"bytes"
	"html/template"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestXPostVoiceSettingsBelowURLAndDashboardParses(t *testing.T) {
	input := strings.Index(indexHTML, `id="imageURLInput"`)
	options := strings.Index(indexHTML, `id="xPostOption"`)
	nextPanel := strings.Index(indexHTML, `id="obsUploadPanel"`)
	if input < 0 || options < input || options > nextPanel {
		t.Fatal("X options must be directly below URL input in link panel")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	cmd := exec.Command(node, "--check")
	values := map[string]string{}
	for _, key := range []string{"imageURL", "videoURL", "hlsURL", "shareURL", "shareURLLabel", "phoneURL", "localImageURL", "previewImageURL", "publicImageURL"} {
		values[key] = ""
	}
	var script bytes.Buffer
	if err := template.Must(template.New("dashboard").Parse(indexHTML)).Execute(&script, values); err != nil {
		t.Fatal(err)
	}
	html := script.String()
	start := strings.LastIndex(html, "<script>") + len("<script>")
	end := strings.LastIndex(html, "</script>")
	if start < 0 || end < start {
		t.Fatal("dashboard script missing")
	}
	cmd.Stdin = strings.NewReader(html[start:end])
	if path := os.Getenv("XPOST_BROWSER_HTML"); path != "" {
		if err := os.WriteFile(path, script.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	hideDashboardTestWindow(cmd)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dashboard syntax: %v %s", err, output)
	}
}

func TestXPostURLGatingAndVoiceIdentityInHandlers(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	script := `
const assert=require('node:assert/strict');
const elements={};
const document={getElementById(id){return elements[id] ||= {hidden:true, value:'', checked:false, addEventListener(){}}}};
let uploadMode='link',mediaIntent='image';
const state={videoPlayerEnabled:true},imageURLInput={value:'https://x.com/alice/status/123'};
` + dashboardScriptXPost + `
updateXPostOption();assert.equal(xPostOption.hidden,false);assert.equal(xPostVoiceFields.hidden,true);
for(const url of ['https://x.com/alice/status/123','https://twitter.com/alice/status/123/video/1','https://x.com/i/web/status/123'])assert.equal(isXPostURL(url),true,url);
for(const url of ['https://example.com/alice/status/123','https://x.com/alice','javascript:alert(1)','https://x.com.evil/status/123'])assert.equal(isXPostURL(url),false,url);
xPostVoicesLoaded=true;mediaIntent='video';updateXPostOption();assert.equal(xPostVoiceFields.hidden,false);
xPostSpeakers=[{speaker_uuid:'a',name:'声',styles:[{id:0,name:'普通'}]}];
xPostVoiceSelect.value=JSON.stringify(['a',0]);xPostSpeed.value='1.2';
const options=xPostUploadOptions(imageURLInput.value);assert.equal(options.mode,'video');assert.equal(options.voice.styleId,0);assert.equal(options.voice.speakerUuid,'a');assert.equal(options.voice.speed,1.2);
imageURLInput.value='https://example.com/movie.mp4';updateXPostOption();assert.equal(xPostOption.hidden,true);assert.equal(xPostUploadOptions(imageURLInput.value),undefined);
imageURLInput.value='https://x.com/alice/status/123';uploadMode='file';updateXPostOption();assert.equal(xPostOption.hidden,true);
`
	cmd := exec.Command(node, "-e", script)
	hideDashboardTestWindow(cmd)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("X settings handlers: %v %s", err, output)
	}
}

func TestXPostRuntimeProgressReadyAndCancellationUI(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	script := `
const assert=require('node:assert/strict');
const elements={},timers=[];
const document={getElementById(id){return elements[id] ||= {hidden:true,value:'',checked:false,disabled:false,textContent:'',addEventListener(){},replaceChildren(){this.value=''},append(){}}},createElement(){return {append(){}}}};
function Option(label,value){this.value=value}
function setTimeout(fn){timers.push(fn);return timers.length}
function clearTimeout(){timers.length=0}
let uploadMode='link',mediaIntent='video';
const state={videoPlayerEnabled:true},imageURLInput={value:'https://x.com/alice/status/123'};
let phase='downloading',voiceCalls=0;
async function apiFetch(url,options={}){
 const status={phase,percent:40,message:'VOICEVOXを準備中'};
 if(url==='/api/xpost/voicevox-runtime'){
  if(options.method==='POST')phase=JSON.parse(options.body).action==='cancel'?'stopped':'starting';
  return {ok:true,json:async()=>({...status,phase})};
 }
 voiceCalls++;
 if(phase!=='ready')return {ok:false,status:503,headers:{get:()=> 'application/json'},json:async()=>({runtime:status,managed:true})};
 return {ok:true,status:200,engineUrl:'http://127.0.0.1:50121',json:async()=>({managed:true,engineUrl:'http://127.0.0.1:50121',speakers:[{speaker_uuid:'a',name:'声',styles:[{id:0,name:'普通'}]}],lastVoice:{speakerUuid:'a',styleId:0,speed:1.2}})};
}
` + dashboardScriptXPost + `
(async()=>{
 updateXPostOption();await new Promise(setImmediate);
 assert.match(xPostRuntimeHint.textContent,/40%/);assert.equal(xPostPreview.disabled,true);assert.equal(timers.length,1);
 phase='ready';await timers.shift()();await new Promise(setImmediate);
 assert.equal(xPostVoicesLoaded,true);assert.equal(selectedXPostVoice().styleId,0);assert.equal(selectedXPostVoice().engineUrl,'http://127.0.0.1:50121');assert.equal(selectedXPostVoice().speed,1.2);
 await changeXPostRuntime('cancel');assert.equal(xPostRetryRuntime.hidden,false);assert.equal(xPostCancelRuntime.hidden,true);
 const calls=voiceCalls;updateXPostOption();if(timers.length)await timers.shift()();assert.equal(voiceCalls,calls,'cancelled runtime restarted automatically');
 await changeXPostRuntime('retry');assert.equal(xPostCancelRuntime.hidden,false);assert.equal(timers.length,1);
})().catch(error=>{console.error(error);process.exitCode=1});
`
	cmd := exec.Command(node, "-e", script)
	hideDashboardTestWindow(cmd)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("runtime UI: %v %s", err, output)
	}
}
