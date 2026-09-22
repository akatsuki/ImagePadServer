"""Generate the isolated prototype from the previously verified NPS3 parser."""
from pathlib import Path
p=Path(__file__).resolve().parent
s=(p.parent/'nico-native-probe/main.cpp').read_text(encoding='utf8')
s=s.replace('int main(int argc,char** argv){','int wmain(int argc,wchar_t** argv){')
a=s.index(' const char* version=');b=s.index(' uint32_t magic=',a)
s=s[:a]+''' const char* version="GPU_COMPOSE_TEST";
 const bool selftest=false;bool discardOutput=false,hardware=true,instanced=false,gpuTiming=false,rgba=false;
 const wchar_t* scene=nullptr;
 for(int i=1;i<argc;i++){
  if(!wcscmp(argv[i],L"--scene")&&i+1<argc)scene=argv[++i];
  else if(!wcscmp(argv[i],L"--rgba"))rgba=true;
  else if(!wcscmp(argv[i],L"--warp"))hardware=false;
  else fail("unknown argument");
 }
 if(!scene)fail("--scene required");FILE* f=_wfopen(scene,L"rb");if(!f)fail("open scene");
 _setmode(_fileno(stdin),_O_BINARY);auto wallStart=Clock::now();
''' + s[b:]
s=s.replace(' const unsigned slots=3;',' if(w%8||h%2)fail("width multiple8 height even required");\n const unsigned slots=3;')
s=s.replace('std::fwprintf(stderr,L"sprite adapter: %ls\\n",d.Description);','')
s=s.replace('td.BindFlags=D3D11_BIND_RENDER_TARGET;','td.BindFlags=D3D11_BIND_RENDER_TARGET|D3D11_BIND_SHADER_RESOURCE;')
s=s.replace('static ID3DBlob* compile(const char* entry,const char* profile){','static ID3DBlob* compile(const char* entry,const char* profile,const char* code=shader){').replace('D3DCompile(shader,std::strlen(shader),','D3DCompile(code,std::strlen(code),')
extra=r'''
static const char* composeCode=R"(
Texture2D<float4> bg:register(t0);Texture2D<float4> comments:register(t1);
float4 fullscreen(uint id:SV_VertexID):SV_Position{return float4(float(id&1)*2-1,1-float(id>>1)*2,0,1);}
float4 composite(float4 pos:SV_Position):SV_Target{int3 xy=int3(pos.xy,0);float4 c=comments.Load(xy);return float4(saturate(c.rgb+bg.Load(xy).rgb*(1-c.a)),1);}
Texture2D<float4> rgb:register(t0);RWByteAddressBuffer dst:register(u0);
uint pack4(uint4 b){return b.x|(b.y<<8)|(b.z<<16)|(b.w<<24);}
uint quant(float f){return (uint)clamp(floor(f+0.5),0,255);}
[numthreads(16,8,1)]
void convert(uint3 tid:SV_DispatchThreadID){
 uint w,h;rgb.GetDimensions(w,h);uint x=tid.x*8,y=tid.y*2;if(x>=w||y>=h)return;
 uint yt[8],yb[8];uint4 us=0,vs=0;
 for(uint k=0;k<4;k++){
  float3 a=rgb.Load(int3(x+k*2,y,0)).rgb,b=rgb.Load(int3(x+k*2+1,y,0)).rgb;
  float3 c=rgb.Load(int3(x+k*2,y+1,0)).rgb,d=rgb.Load(int3(x+k*2+1,y+1,0)).rgb;
  yt[k*2]=quant(16+219*dot(a,float3(.2126,.7152,.0722)));yt[k*2+1]=quant(16+219*dot(b,float3(.2126,.7152,.0722)));
  yb[k*2]=quant(16+219*dot(c,float3(.2126,.7152,.0722)));yb[k*2+1]=quant(16+219*dot(d,float3(.2126,.7152,.0722)));
  float3 av=(a+b+c+d)*.25;
  us[k]=quant(128+224*dot(av,float3(-.2126/(2*(1-.0722)),-.7152/(2*(1-.0722)),.5)));
  vs[k]=quant(128+224*dot(av,float3(.5,-.7152/(2*(1-.2126)),-.0722/(2*(1-.2126)))));
 }
 dst.Store(y*w+x,pack4(uint4(yt[0],yt[1],yt[2],yt[3])));dst.Store(y*w+x+4,pack4(uint4(yt[4],yt[5],yt[6],yt[7])));
 dst.Store((y+1)*w+x,pack4(uint4(yb[0],yb[1],yb[2],yb[3])));dst.Store((y+1)*w+x+4,pack4(uint4(yb[4],yb[5],yb[6],yb[7])));
 dst.Store(w*h+(y/2)*(w/2)+x/2,pack4(us));dst.Store(w*h+w*h/4+(y/2)*(w/2)+x/2,pack4(vs));
}
)";
'''
s=s.replace('int wmain(',extra+'\nint wmain(')
init=r'''
 ID3D11Texture2D *background,*composed;ID3D11ShaderResourceView *backgroundView,*commentsView,*composedView;
 D3D11_TEXTURE2D_DESC full=td;full.Usage=D3D11_USAGE_DEFAULT;full.CPUAccessFlags=0;full.BindFlags=D3D11_BIND_SHADER_RESOURCE|D3D11_BIND_RENDER_TARGET;
 check(device->CreateTexture2D(&full,nullptr,&background),"background");check(device->CreateTexture2D(&full,nullptr,&composed),"composed");
 check(device->CreateShaderResourceView(background,nullptr,&backgroundView),"bg srv");check(device->CreateShaderResourceView(target,nullptr,&commentsView),"comment srv");check(device->CreateShaderResourceView(composed,nullptr,&composedView),"composed srv");
 ID3D11RenderTargetView* composedRTV;check(device->CreateRenderTargetView(composed,nullptr,&composedRTV),"composed rtv");
 ID3D11VertexShader* fullVS;ID3D11PixelShader* fullPS;ID3D11ComputeShader* converter;
 ID3DBlob *cv=compile("fullscreen","vs_5_0",composeCode),*cp=compile("composite","ps_5_0",composeCode),*cc=compile("convert","cs_5_0",composeCode);
 check(device->CreateVertexShader(cv->GetBufferPointer(),cv->GetBufferSize(),nullptr,&fullVS),"fullscreen vs");check(device->CreatePixelShader(cp->GetBufferPointer(),cp->GetBufferSize(),nullptr,&fullPS),"composite ps");check(device->CreateComputeShader(cc->GetBufferPointer(),cc->GetBufferSize(),nullptr,&converter),"convert cs");cv->Release();cp->Release();cc->Release();
 UINT yuvBytes=w*h*3/2;D3D11_BUFFER_DESC outDesc={};outDesc.ByteWidth=yuvBytes;outDesc.Usage=D3D11_USAGE_DEFAULT;outDesc.BindFlags=D3D11_BIND_UNORDERED_ACCESS;outDesc.MiscFlags=D3D11_RESOURCE_MISC_BUFFER_ALLOW_RAW_VIEWS;
 ID3D11Buffer* yuv;check(device->CreateBuffer(&outDesc,nullptr,&yuv),"yuv buffer");
 D3D11_UNORDERED_ACCESS_VIEW_DESC ud={};ud.Format=DXGI_FORMAT_R32_TYPELESS;ud.ViewDimension=D3D11_UAV_DIMENSION_BUFFER;ud.Buffer.NumElements=yuvBytes/4;ud.Buffer.Flags=D3D11_BUFFER_UAV_FLAG_RAW;
 ID3D11UnorderedAccessView* yuvView;check(device->CreateUnorderedAccessView(yuv,&ud,&yuvView),"yuv uav");
 outDesc.Usage=D3D11_USAGE_STAGING;outDesc.BindFlags=outDesc.MiscFlags=0;outDesc.CPUAccessFlags=D3D11_CPU_ACCESS_READ;
 std::vector<ID3D11Buffer*> yuvStaging(slots);for(auto& b:yuvStaging)check(device->CreateBuffer(&outDesc,nullptr,&b),"yuv staging");
 std::vector<uint8_t> backgroundPixels(size_t(w)*h*4);double backgroundReadMs=0,backgroundUploadMs=0,compositeSubmitMs=0;
 ID3D11ShaderResourceView* nullSRV[2]={};ID3D11UnorderedAccessView* nullUAV=nullptr;
'''
s=s.replace(' D3D11_BLEND_DESC blend={};',init+'\n D3D11_BLEND_DESC blend={};')
s=s.replace('auto s=staging[written%slots];','ID3D11Resource* s=rgba?static_cast<ID3D11Resource*>(staging[written%slots]):static_cast<ID3D11Resource*>(yuvStaging[written%slots]);')
s=s.replace('if(mapped.RowPitch!=w*4){','if(rgba&&mapped.RowPitch!=w*4){')
s=s.replace('std::fwrite(output,1,pixels.size(),stdout)!=pixels.size()','std::fwrite(output,1,rgba?pixels.size():yuvBytes,stdout)!=(rgba?pixels.size():yuvBytes)')
s=s.replace('  auto submitStart=Clock::now();ctx->ClearRenderTargetView(rtv,clear);',r'''
  auto bgStart=Clock::now();if(fread(backgroundPixels.data(),1,backgroundPixels.size(),stdin)!=backgroundPixels.size())fail("truncated background");backgroundReadMs+=std::chrono::duration<double,std::milli>(Clock::now()-bgStart).count();
  bgStart=Clock::now();ctx->UpdateSubresource(background,0,nullptr,backgroundPixels.data(),w*4,0);backgroundUploadMs+=std::chrono::duration<double,std::milli>(Clock::now()-bgStart).count();
  ctx->OMSetRenderTargets(1,&rtv,nullptr);ctx->OMSetBlendState(bs,nullptr,0xffffffff);
  auto submitStart=Clock::now();ctx->ClearRenderTargetView(rtv,clear);''')
s=s.replace('  ctx->CopyResource(staging[submitted%slots],target);submitted++;',r'''
  auto composeStart=Clock::now();ctx->OMSetRenderTargets(1,&composedRTV,nullptr);ctx->OMSetBlendState(nullptr,nullptr,0xffffffff);ctx->VSSetShader(fullVS,nullptr,0);ctx->PSSetShader(fullPS,nullptr,0);
  ID3D11ShaderResourceView* inputs[2]={backgroundView,commentsView};ctx->PSSetShaderResources(0,2,inputs);ctx->Draw(4,0);ctx->PSSetShaderResources(0,2,nullSRV);ctx->OMSetRenderTargets(0,nullptr,nullptr);
  if(rgba)ctx->CopyResource(staging[submitted%slots],composed);
  else{ctx->CSSetShader(converter,nullptr,0);ctx->CSSetShaderResources(0,1,&composedView);ctx->CSSetUnorderedAccessViews(0,1,&yuvView,nullptr);ctx->Dispatch((w+127)/128,(h+15)/16,1);ctx->CSSetShaderResources(0,1,nullSRV);ctx->CSSetUnorderedAccessViews(0,1,&nullUAV,nullptr);ctx->CopyResource(yuvStaging[submitted%slots],yuv);}
  compositeSubmitMs+=std::chrono::duration<double,std::milli>(Clock::now()-composeStart).count();submitted++;''')
s=s.replace(' while(written<submitted)drain();',' if(std::fgetc(stdin)!=EOF)fail("trailing background data");\n while(written<submitted)drain();')
s=s.replace('  std::fprintf(stderr,"NICO_PROFILE ', '  std::fprintf(stderr,"NICO_BASE_PROFILE ')
index=s.index('  std::fprintf(stderr,"NICO_BASE_PROFILE ')
s=s[:index]+r'''
  std::fprintf(stderr,"NICO_GPU_PROFILE {\"wall_ms\":%.6f,\"background_read_ms\":%.6f,\"background_upload_ms\":%.6f,\"draw_submit_ms\":%.6f,\"composite_submit_ms\":%.6f,\"map_wait_ms\":%.6f,\"output_ms\":%.6f,\"frames\":%u,\"input_bytes\":%llu,\"readback_bytes\":%llu,\"adapter\":\"%s\",\"output\":\"%s\"}\n",wallMs,backgroundReadMs,backgroundUploadMs,drawSubmitMs,compositeSubmitMs,mapWaitMs,outputMs,written,(unsigned long long)w*h*4*nf,(unsigned long long)(rgba?w*h*4:yuvBytes)*nf,adapterName.c_str(),rgba?"rgba":"i420");
''' +s[index:]
(p/'main.cpp').write_text(s,encoding='utf8')
b=(p.parent/'nico-native-probe/build.ps1').read_text(encoding='utf8').replace('nico-native-probe','nico-gpu-compose')
b='\n'.join(line for line in b.splitlines() if '$baseline' not in line and '$baseSelf' not in line)
b=b[:b.index('$pathBefore =')]+'''Write-Output "Built: $probeExe (calibration is performed by verify.py)"\n'''
(p/'build.ps1').write_text(b,encoding='utf8')
