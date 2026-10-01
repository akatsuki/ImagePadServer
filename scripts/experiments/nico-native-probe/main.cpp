// ImagePadServer offline comment compositor. NPS3, premultiplied RGBA, WARP.
#define NOMINMAX
#include <windows.h>
#include <d3d11.h>
#include <d3dcompiler.h>
#include <dxgi1_2.h>
#include <io.h>
#include <fcntl.h>
#include <cstdio>
#include <cstdlib>
#include <cstdint>
#include <vector>
#include <unordered_map>
#include <cstring>
#include <cwchar>
#include <cmath>
#include <chrono>
#include <string>
#include <sstream>
#include <iomanip>

static void fail(const char* what){std::fprintf(stderr,"nico compositor: %s\n",what);std::exit(2);}
static void check(HRESULT hr,const char* what){if(FAILED(hr)){std::fprintf(stderr,"HRESULT=%08lx ",static_cast<unsigned long>(hr));fail(what);}}
static const char* featureName(D3D_FEATURE_LEVEL level){switch(level){case D3D_FEATURE_LEVEL_11_1:return "11_1";case D3D_FEATURE_LEVEL_11_0:return "11_0";case D3D_FEATURE_LEVEL_10_1:return "10_1";case D3D_FEATURE_LEVEL_10_0:return "10_0";default:return "unknown";}}
using Clock=std::chrono::steady_clock;
static bool bounded=false;static uint64_t remaining=0;static double inputReadMs=0;
static void read(FILE* f,void* p,size_t n){
 auto started=Clock::now();
 if(bounded){if(n>remaining)fail("batch length underrun");remaining-=n;}
 if(std::fread(p,1,n,f)!=n)fail("truncated scene");
 inputReadMs+=std::chrono::duration<double,std::milli>(Clock::now()-started).count();
}
template<class T> static T read(FILE* f){T t;read(f,&t,sizeof t);return t;}
struct Command {uint32_t id;float rect[4],proj[16],color[4];};
static_assert(sizeof(Command)==100,"scene ABI");
struct Instance {float rect[4],proj[16],color[4];};
static_assert(sizeof(Instance)==96,"instance ABI");
struct InstanceBase {uint32_t base=0,padding[3]={};};
static_assert(sizeof(InstanceBase)==16,"instance base ABI");
static const char* shader=R"(
cbuffer Params : register(b0) {float4 rect;column_major float4x4 projection;float4 color;};
struct Vertex {float4 pos:SV_Position;float2 uv:TEXCOORD0;nointerpolation uint instance:TEXCOORD1;};
Vertex vs(uint id:SV_VertexID){Vertex o;o.uv=float2(id&1,id>>1);o.instance=0;o.pos=mul(projection,float4(rect.xy+o.uv*rect.zw,0,1));return o;}
struct Instance {float4 rect;column_major float4x4 proj;float4 color;};
cbuffer InstanceBase : register(b1) {uint instanceBase;float3 instancePadding;};
StructuredBuffer<Instance> instances:register(t1);
Vertex vsi(uint id:SV_VertexID,uint instance:SV_InstanceID){uint absoluteInstance=instanceBase+instance;Instance c=instances[absoluteInstance];Vertex o;o.uv=float2(id&1,id>>1);o.instance=absoluteInstance;o.pos=mul(c.proj,float4(c.rect.xy+o.uv*c.rect.zw,0,1));return o;}
Texture2D sprite:register(t0);SamplerState samp:register(s0);
float4 ps(Vertex v):SV_Target{return sprite.Sample(samp,v.uv)*color.x;}
float4 solid(Vertex v):SV_Target{return color;}
float4 psi(Vertex v):SV_Target{return sprite.Sample(samp,v.uv)*instances[v.instance].color.x;}
float4 solidi(Vertex v):SV_Target{return instances[v.instance].color;}
)";
static ID3DBlob* compile(const char* entry,const char* profile){
 ID3DBlob *out=nullptr,*errors=nullptr;HRESULT hr=D3DCompile(shader,std::strlen(shader),nullptr,nullptr,nullptr,entry,profile,D3DCOMPILE_OPTIMIZATION_LEVEL3,0,&out,&errors);
 if(FAILED(hr)){if(errors)std::fwrite(errors->GetBufferPointer(),1,errors->GetBufferSize(),stderr);check(hr,"shader compile");}if(errors)errors->Release();return out;
}
int main(int argc,char** argv){
 SetErrorMode(SEM_FAILCRITICALERRORS|SEM_NOGPFAULTERRORBOX);
 const char* version="NICO_COMPOSITOR 1 NPS3 WARP";
 if(argc==2&&std::strcmp(argv[1],"--version")==0){std::puts(version);return 0;}
 const bool selftest=argc==2&&std::strcmp(argv[1],"--self-test")==0;
 bool discardOutput=false,hardware=false,instanced=false,gpuTiming=false;
 if(!selftest){if(argc<2||std::strcmp(argv[1],"--stdin")!=0)fail("--stdin required");
  for(int i=2;i<argc;i++){
   if(std::strcmp(argv[i],"--discard-output")==0)discardOutput=true;
   else if(std::strcmp(argv[i],"--hardware")==0)hardware=true;
   else if(std::strcmp(argv[i],"--instanced")==0)instanced=true;
   else if(std::strcmp(argv[i],"--gpu-timing")==0)gpuTiming=true;
   else fail("unknown argument");
  }
 }
 FILE* f=stdin;_setmode(_fileno(stdin),_O_BINARY);auto wallStart=Clock::now();
 uint32_t magic=0x3353504e,w=33,h=19,nf=1,fpsNum=30,fpsDen=1;
 if(!selftest){magic=read<uint32_t>(f);w=read<uint32_t>(f);h=read<uint32_t>(f);nf=read<uint32_t>(f);fpsNum=read<uint32_t>(f);fpsDen=read<uint32_t>(f);}
 if(magic!=0x3353504e||!w||!h||w>3840||h>2160||!nf||nf>1000000||!fpsNum||!fpsDen||fpsNum>1000000||fpsDen>1000000||uint64_t(fpsNum)>60ull*fpsDen)fail("scene header bound");
 const unsigned slots=3;
 const bool warp=!hardware;
 auto initStart=Clock::now();
 IDXGIAdapter1* adapter=nullptr;
 std::string adapterName=warp?"WARP":"hardware";
 if(!warp){IDXGIFactory1* factory=nullptr;check(CreateDXGIFactory1(__uuidof(IDXGIFactory1),(void**)&factory),"DXGI");SIZE_T best=0;
  for(UINT i=0;;i++){IDXGIAdapter1* item=nullptr;if(factory->EnumAdapters1(i,&item)==DXGI_ERROR_NOT_FOUND)break;DXGI_ADAPTER_DESC1 d;item->GetDesc1(&d);
   if(!(d.Flags&DXGI_ADAPTER_FLAG_SOFTWARE)&&(!adapter||d.DedicatedVideoMemory>best)){if(adapter)adapter->Release();adapter=item;best=d.DedicatedVideoMemory;}else item->Release();}factory->Release();
  if(!adapter)fail("no hardware adapter");DXGI_ADAPTER_DESC1 d;adapter->GetDesc1(&d);std::fwprintf(stderr,L"sprite adapter: %ls\n",d.Description);
  char name[256]={};WideCharToMultiByte(CP_UTF8,0,d.Description,-1,name,sizeof(name),nullptr,nullptr);adapterName=name;
 }
 ID3D11Device* device=nullptr;ID3D11DeviceContext* ctx=nullptr;D3D_FEATURE_LEVEL level;
 check(D3D11CreateDevice(adapter,warp?D3D_DRIVER_TYPE_WARP:D3D_DRIVER_TYPE_UNKNOWN,nullptr,0,nullptr,0,D3D11_SDK_VERSION,&device,&level,&ctx),"device");if(adapter)adapter->Release();
 auto shaderStart=Clock::now();
 ID3DBlob *vs=compile("vs","vs_5_0"),*vsi=compile("vsi","vs_5_0"),*ps=compile("ps","ps_5_0"),*psi=compile("psi","ps_5_0"),*solid=compile("solid","ps_5_0"),*solidi=compile("solidi","ps_5_0");
 ID3D11VertexShader* vshader,*vishader;ID3D11PixelShader *pshader,*psishader,*sshader,*sishader;
 check(device->CreateVertexShader(vs->GetBufferPointer(),vs->GetBufferSize(),nullptr,&vshader),"vertex shader");
 check(device->CreateVertexShader(vsi->GetBufferPointer(),vsi->GetBufferSize(),nullptr,&vishader),"instanced vertex shader");
 check(device->CreatePixelShader(ps->GetBufferPointer(),ps->GetBufferSize(),nullptr,&pshader),"sprite shader");
 check(device->CreatePixelShader(psi->GetBufferPointer(),psi->GetBufferSize(),nullptr,&psishader),"instanced sprite shader");
 check(device->CreatePixelShader(solid->GetBufferPointer(),solid->GetBufferSize(),nullptr,&sshader),"solid shader");
 check(device->CreatePixelShader(solidi->GetBufferPointer(),solidi->GetBufferSize(),nullptr,&sishader),"instanced solid shader");
 vs->Release();vsi->Release();ps->Release();psi->Release();solid->Release();solidi->Release();
 const double shaderMs=std::chrono::duration<double,std::milli>(Clock::now()-shaderStart).count();
 D3D11_BUFFER_DESC bd={};bd.ByteWidth=96;bd.Usage=D3D11_USAGE_DEFAULT;bd.BindFlags=D3D11_BIND_CONSTANT_BUFFER;ID3D11Buffer* constants;check(device->CreateBuffer(&bd,nullptr,&constants),"constants");
 const UINT maxInstances=100000;
 UINT instanceCapacity=256;
 ID3D11Buffer* instanceBuffer=nullptr;ID3D11ShaderResourceView* instanceView=nullptr;
 auto createInstanceBuffer=[&](UINT capacity){
  D3D11_BUFFER_DESC ibd={};ibd.ByteWidth=sizeof(Instance)*capacity;ibd.Usage=D3D11_USAGE_DYNAMIC;ibd.BindFlags=D3D11_BIND_SHADER_RESOURCE;ibd.CPUAccessFlags=D3D11_CPU_ACCESS_WRITE;ibd.MiscFlags=D3D11_RESOURCE_MISC_BUFFER_STRUCTURED;ibd.StructureByteStride=sizeof(Instance);
  check(device->CreateBuffer(&ibd,nullptr,&instanceBuffer),"instance buffer");
  D3D11_SHADER_RESOURCE_VIEW_DESC isd={};isd.Format=DXGI_FORMAT_UNKNOWN;isd.ViewDimension=D3D11_SRV_DIMENSION_BUFFER;isd.Buffer.FirstElement=0;isd.Buffer.NumElements=capacity;
  check(device->CreateShaderResourceView(instanceBuffer,&isd,&instanceView),"instance view");
 };
 createInstanceBuffer(instanceCapacity);
 D3D11_BUFFER_DESC baseDesc={};baseDesc.ByteWidth=sizeof(InstanceBase);baseDesc.Usage=D3D11_USAGE_DEFAULT;baseDesc.BindFlags=D3D11_BIND_CONSTANT_BUFFER;
 ID3D11Buffer* instanceBaseBuffer=nullptr;check(device->CreateBuffer(&baseDesc,nullptr,&instanceBaseBuffer),"instance base buffer");
 D3D11_TEXTURE2D_DESC td={};td.Width=w;td.Height=h;td.MipLevels=td.ArraySize=1;td.Format=DXGI_FORMAT_R8G8B8A8_UNORM;td.SampleDesc.Count=1;td.Usage=D3D11_USAGE_DEFAULT;td.BindFlags=D3D11_BIND_RENDER_TARGET;
 ID3D11Texture2D *target;std::vector<ID3D11Texture2D*> staging(slots);check(device->CreateTexture2D(&td,nullptr,&target),"target");ID3D11RenderTargetView* rtv;check(device->CreateRenderTargetView(target,nullptr,&rtv),"target view");
 td.Usage=D3D11_USAGE_STAGING;td.BindFlags=0;td.CPUAccessFlags=D3D11_CPU_ACCESS_READ;for(auto& s:staging)check(device->CreateTexture2D(&td,nullptr,&s),"readback");
 D3D11_BLEND_DESC blend={};auto& b=blend.RenderTarget[0];b.BlendEnable=TRUE;b.SrcBlend=b.SrcBlendAlpha=D3D11_BLEND_ONE;b.DestBlend=b.DestBlendAlpha=D3D11_BLEND_INV_SRC_ALPHA;b.BlendOp=b.BlendOpAlpha=D3D11_BLEND_OP_ADD;b.RenderTargetWriteMask=D3D11_COLOR_WRITE_ENABLE_ALL;ID3D11BlendState* bs;check(device->CreateBlendState(&blend,&bs),"blend");
 D3D11_RASTERIZER_DESC rd={};rd.FillMode=D3D11_FILL_SOLID;rd.CullMode=D3D11_CULL_NONE;rd.DepthClipEnable=TRUE;ID3D11RasterizerState* rs;check(device->CreateRasterizerState(&rd,&rs),"rasterizer");
 D3D11_SAMPLER_DESC sd={};sd.Filter=D3D11_FILTER_MIN_MAG_MIP_LINEAR;sd.AddressU=sd.AddressV=sd.AddressW=D3D11_TEXTURE_ADDRESS_CLAMP;sd.MaxLOD=D3D11_FLOAT32_MAX;ID3D11SamplerState* sampler;check(device->CreateSamplerState(&sd,&sampler),"sampler");
 ctx->VSSetShader(vshader,nullptr,0);ctx->VSSetConstantBuffers(0,1,&constants);ctx->VSSetConstantBuffers(1,1,&instanceBaseBuffer);ctx->PSSetConstantBuffers(0,1,&constants);ctx->PSSetSamplers(0,1,&sampler);ctx->IASetPrimitiveTopology(D3D11_PRIMITIVE_TOPOLOGY_TRIANGLESTRIP);
 ctx->OMSetRenderTargets(1,&rtv,nullptr);ctx->OMSetBlendState(bs,nullptr,0xffffffff);ctx->RSSetState(rs);D3D11_VIEWPORT vp={0,0,(float)w,(float)h,0,1};ctx->RSSetViewports(1,&vp);
 const double initMs=std::chrono::duration<double,std::milli>(Clock::now()-initStart).count();
 struct Texture{ID3D11ShaderResourceView* view;size_t bytes;};
 std::unordered_map<uint32_t,Texture> textures;size_t totalBytes=0;uint32_t lastID=0;double textureMs=0;
 auto loadTextures=[&](uint32_t nt){
 if(nt>10000||textures.size()+nt>10000)fail("texture count bound");
 for(uint32_t i=0;i<nt;i++){
  uint32_t id=read<uint32_t>(f),tw=read<uint32_t>(f),th=read<uint32_t>(f);size_t bytes=size_t(tw)*th*4;totalBytes+=bytes;
  if(!id||id<=lastID||!tw||!th||tw>16384||th>16384||bytes>128*1024*1024||totalBytes>512*1024*1024)fail("texture bound");lastID=id;
  std::vector<uint8_t> pixels(bytes);read(f,pixels.data(),bytes);auto textureStart=Clock::now();D3D11_TEXTURE2D_DESC d={};d.Width=tw;d.Height=th;d.MipLevels=d.ArraySize=1;d.Format=DXGI_FORMAT_R8G8B8A8_UNORM;d.SampleDesc.Count=1;d.Usage=D3D11_USAGE_IMMUTABLE;d.BindFlags=D3D11_BIND_SHADER_RESOURCE;
  D3D11_SUBRESOURCE_DATA data={pixels.data(),tw*4,0};ID3D11Texture2D* tex;check(device->CreateTexture2D(&d,&data,&tex),"sprite texture");ID3D11ShaderResourceView* srv;check(device->CreateShaderResourceView(tex,nullptr,&srv),"sprite view");tex->Release();textures.emplace(id,Texture{srv,bytes});
  textureMs+=std::chrono::duration<double,std::milli>(Clock::now()-textureStart).count();
 }};
 _setmode(_fileno(stdout),_O_BINARY);std::setvbuf(stdout,nullptr,_IOFBF,1024*1024);
 std::vector<uint8_t> pixels(size_t(w)*h*4);const float clear[4]={0,0,0,0};
 uint32_t submitted=0,written=0;uint64_t copiedBytes=0,commandTotal=0,drawCalls=0;
 double drawSubmitMs=0,mapWaitMs=0,rowCopyMs=0,outputMs=0,fwriteMs=0,copySubmitMs=0,flushMs=0;
 auto drain=[&](){
  auto s=staging[written%slots];D3D11_MAPPED_SUBRESOURCE mapped;auto mapStart=Clock::now();
  // Map(flags=0) waits for this slot's CopyResource. Later copies may already
  // be queued; there is no need to poll a separate completion query.
  check(ctx->Map(s,0,D3D11_MAP_READ,0,&mapped),"map readback");
  mapWaitMs+=std::chrono::duration<double,std::milli>(Clock::now()-mapStart).count();
  const uint8_t* output=(uint8_t*)mapped.pData;
  if(mapped.RowPitch!=w*4){
   auto rowCopyStart=Clock::now();
   for(uint32_t y=0;y<h;y++)std::memcpy(pixels.data()+size_t(y)*w*4,output+size_t(y)*mapped.RowPitch,w*4);
   rowCopyMs+=std::chrono::duration<double,std::milli>(Clock::now()-rowCopyStart).count();
   output=pixels.data();copiedBytes+=pixels.size();
  }
  auto outputStart=Clock::now();
  if(selftest){for(size_t i=0;i<pixels.size();i++)if(output[i]!=0)fail("self test pixels");}
  else if(!discardOutput){
   auto fwriteStart=Clock::now();
   if(std::fwrite(output,1,pixels.size(),stdout)!=pixels.size())fail("output pipe");
   fwriteMs+=std::chrono::duration<double,std::milli>(Clock::now()-fwriteStart).count();
  }
  outputMs+=std::chrono::duration<double,std::milli>(Clock::now()-outputStart).count();
  // fwrite has consumed the bytes before the mapping may be recycled.
  ctx->Unmap(s,0);written++;
  if(!selftest&&(written==1||written%30==0||written==nf))std::fprintf(stderr,"NICO_PROGRESS %u %u\n",written,nf);
 };
 auto drawFrame=[&](){
  uint64_t seq=read<uint64_t>(f),ts=read<uint64_t>(f);
  if(seq!=submitted||ts!=uint64_t(submitted)*fpsDen*1000/fpsNum)fail("frame sequence or timestamp");
  uint32_t nc=read<uint32_t>(f);if(nc>100000)fail("command count bound");
  std::vector<Command> commands(nc);for(auto& c:commands){c=read<Command>(f);for(float v:c.rect)if(!std::isfinite(v))fail("invalid rect");for(float v:c.proj)if(!std::isfinite(v))fail("invalid projection");for(float v:c.color)if(!std::isfinite(v))fail("invalid color");}
  commandTotal+=nc;
  auto submitStart=Clock::now();ctx->ClearRenderTargetView(rtv,clear);
  if(!instanced){
   ctx->VSSetShader(vshader,nullptr,0);ID3D11ShaderResourceView* nullInstance=nullptr;ctx->VSSetShaderResources(1,1,&nullInstance);
   for(const Command& c:commands){ctx->UpdateSubresource(constants,0,nullptr,c.rect,0,0);if(c.id){auto it=textures.find(c.id);if(it==textures.end())fail("unknown texture");ctx->PSSetShader(pshader,nullptr,0);ctx->PSSetShaderResources(0,1,&it->second.view);}else ctx->PSSetShader(sshader,nullptr,0);ctx->Draw(4,0);++drawCalls;}
  }else{
   if(commands.size()>maxInstances)fail("instance count bound");
   if(commands.size()>instanceCapacity){
    UINT next=instanceCapacity;while(next<commands.size()){if(next>maxInstances/2){next=maxInstances;break;}next*=2;}
    ID3D11ShaderResourceView* nullView=nullptr;ctx->VSSetShaderResources(1,1,&nullView);ctx->PSSetShaderResources(1,1,&nullView);
    if(instanceView)instanceView->Release();if(instanceBuffer)instanceBuffer->Release();instanceView=nullptr;instanceBuffer=nullptr;instanceCapacity=next;createInstanceBuffer(instanceCapacity);
   }
   ctx->VSSetShader(vishader,nullptr,0);ctx->VSSetShaderResources(1,1,&instanceView);ctx->PSSetShaderResources(1,1,&instanceView);
   if(!commands.empty()){
    D3D11_MAPPED_SUBRESOURCE mapped{};check(ctx->Map(instanceBuffer,0,D3D11_MAP_WRITE_DISCARD,0,&mapped),"instance map");
    for(size_t i=0;i<commands.size();i++){const Command& c=commands[i];Instance instance{};std::memcpy(instance.rect,c.rect,sizeof(c.rect));std::memcpy(instance.proj,c.proj,sizeof(c.proj));std::memcpy(instance.color,c.color,sizeof(c.color));std::memcpy(static_cast<uint8_t*>(mapped.pData)+i*sizeof(Instance),&instance,sizeof(instance));}
    ctx->Unmap(instanceBuffer,0);
   }
   size_t begin=0;
   while(begin<commands.size()){
    size_t end=begin+1;while(end<commands.size()&&commands[end].id==commands[begin].id)++end;
    const size_t count=end-begin;
    InstanceBase base{};base.base=static_cast<uint32_t>(begin);ctx->UpdateSubresource(instanceBaseBuffer,0,nullptr,&base,0,0);
    if(commands[begin].id){auto it=textures.find(commands[begin].id);if(it==textures.end())fail("unknown texture");ctx->PSSetShader(psishader,nullptr,0);ctx->PSSetShaderResources(0,1,&it->second.view);}else ctx->PSSetShader(sishader,nullptr,0);
    ctx->DrawInstanced(4,static_cast<UINT>(count),0,0);++drawCalls;begin=end;
   }
  }
  drawSubmitMs+=std::chrono::duration<double,std::milli>(Clock::now()-submitStart).count();
  auto copyStart=Clock::now();ctx->CopyResource(staging[submitted%slots],target);copySubmitMs+=std::chrono::duration<double,std::milli>(Clock::now()-copyStart).count();submitted++;
  if(submitted-written>=slots)drain();
 };
 if(selftest){ctx->ClearRenderTargetView(rtv,clear);ctx->CopyResource(staging[0],target);submitted=1;}else{
  while(true){
   uint32_t size=read<uint32_t>(f);if(!size)break;if(size>512*1024*1024)fail("batch byte bound");remaining=size;bounded=true;
   loadTextures(read<uint32_t>(f));uint32_t batchFrames=read<uint32_t>(f);
   if(!batchFrames||batchFrames>30||submitted+batchFrames>nf)fail("batch frame count bound");
   for(uint32_t i=0;i<batchFrames;i++)drawFrame();
   uint32_t deleted=read<uint32_t>(f);if(deleted>textures.size())fail("delete count bound");
   ID3D11ShaderResourceView* nullView=nullptr;ctx->PSSetShaderResources(0,1,&nullView);
   for(uint32_t i=0;i<deleted;i++){uint32_t id=read<uint32_t>(f);auto it=textures.find(id);if(it==textures.end())fail("unknown or duplicate deleted texture");totalBytes-=it->second.bytes;it->second.view->Release();textures.erase(it);}
   if(remaining)fail("batch length overrun");bounded=false;
  }
  if(submitted!=nf)fail("stream ended before all frames");
 }
 while(written<submitted)drain();
 if(!selftest&&std::fgetc(f)!=EOF)fail("trailing scene data");auto flushStart=Clock::now();if(std::fflush(stdout)!=0)fail("flush output");flushMs=std::chrono::duration<double,std::milli>(Clock::now()-flushStart).count();
 if(selftest)std::puts(version);else{
  std::fprintf(stderr,"NICO_STATS copied_bytes=%llu frames=%u\n",static_cast<unsigned long long>(copiedBytes),written);
  std::fprintf(stderr,"NICO_DONE %u\n",written);
  const double wallMs=std::chrono::duration<double,std::milli>(Clock::now()-wallStart).count();
  std::fprintf(stderr,"NICO_PROFILE {\"init_ms\":%.6f,\"shader_ms\":%.6f,\"input_read_ms\":%.6f,\"texture_ms\":%.6f,\"draw_submit_ms\":%.6f,\"copy_submit_ms\":%.6f,\"map_wait_ms\":%.6f,\"row_copy_ms\":%.6f,\"output_ms\":%.6f,\"fwrite_ms\":%.6f,\"flush_ms\":%.6f,\"wall_ms\":%.6f,\"readback_bytes\":%llu,\"row_copy_bytes\":%llu,\"fwrite_bytes\":%llu,\"draws\":%llu,\"commands\":%llu,\"frames\":%u,\"backend\":\"%s\",\"adapter\":\"%s\",\"feature_level\":\"%s\",\"gpu_timing\":\"%s\"}\n",initMs,shaderMs,inputReadMs,textureMs,drawSubmitMs,copySubmitMs,mapWaitMs,rowCopyMs,outputMs,fwriteMs,flushMs,wallMs,static_cast<unsigned long long>(size_t(w)*h*written*4),static_cast<unsigned long long>(copiedBytes),static_cast<unsigned long long>(discardOutput?0:size_t(w)*h*written*4),static_cast<unsigned long long>(drawCalls),static_cast<unsigned long long>(commandTotal),written,warp?"WARP":"hardware",adapterName.c_str(),featureName(level),gpuTiming?"requested_unavailable":"disabled");
 }
 return 0;
}
