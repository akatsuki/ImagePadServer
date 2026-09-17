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

static void fail(const char* what){std::fprintf(stderr,"nico compositor: %s\n",what);std::exit(2);}
static void check(HRESULT hr,const char* what){if(FAILED(hr)){std::fprintf(stderr,"HRESULT=%08lx ",static_cast<unsigned long>(hr));fail(what);}}
static bool bounded=false;static uint64_t remaining=0;
static void read(FILE* f,void* p,size_t n){if(bounded){if(n>remaining)fail("batch length underrun");remaining-=n;}if(std::fread(p,1,n,f)!=n)fail("truncated scene");}
template<class T> static T read(FILE* f){T t;read(f,&t,sizeof t);return t;}
struct Command {uint32_t id;float rect[4],proj[16],color[4];};
static_assert(sizeof(Command)==100,"scene ABI");
static const char* shader=R"(
cbuffer Params : register(b0) {float4 rect;column_major float4x4 projection;float4 color;};
struct Vertex {float4 pos:SV_Position;float2 uv:TEXCOORD0;};
Vertex vs(uint id:SV_VertexID){Vertex o;o.uv=float2(id&1,id>>1);o.pos=mul(projection,float4(rect.xy+o.uv*rect.zw,0,1));return o;}
Texture2D sprite:register(t0);SamplerState samp:register(s0);
float4 ps(Vertex v):SV_Target{return sprite.Sample(samp,v.uv)*color.x;}
float4 solid(Vertex v):SV_Target{return color;}
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
 bool copyOutput=false;
 if(!selftest){if(argc<2||std::strcmp(argv[1],"--stdin")!=0)fail("--stdin required");
  if(argc==3&&std::strcmp(argv[2],"--copy-output")==0)copyOutput=true;else if(argc!=2)fail("unknown argument");}
 FILE* f=stdin;_setmode(_fileno(stdin),_O_BINARY);
 uint32_t magic=0x3353504e,w=33,h=19,nf=1,fpsNum=30,fpsDen=1;
 if(!selftest){magic=read<uint32_t>(f);w=read<uint32_t>(f);h=read<uint32_t>(f);nf=read<uint32_t>(f);fpsNum=read<uint32_t>(f);fpsDen=read<uint32_t>(f);}
 if(magic!=0x3353504e||!w||!h||w>3840||h>2160||!nf||nf>1000000||!fpsNum||!fpsDen||fpsNum>1000000||fpsDen>1000000||uint64_t(fpsNum)>60ull*fpsDen)fail("scene header bound");
 const unsigned slots=3;
 const bool warp=true;
 IDXGIAdapter1* adapter=nullptr;
 if(!warp){IDXGIFactory1* factory=nullptr;check(CreateDXGIFactory1(__uuidof(IDXGIFactory1),(void**)&factory),"DXGI");SIZE_T best=0;
  for(UINT i=0;;i++){IDXGIAdapter1* item=nullptr;if(factory->EnumAdapters1(i,&item)==DXGI_ERROR_NOT_FOUND)break;DXGI_ADAPTER_DESC1 d;item->GetDesc1(&d);
   if(!(d.Flags&DXGI_ADAPTER_FLAG_SOFTWARE)&&(!adapter||d.DedicatedVideoMemory>best)){if(adapter)adapter->Release();adapter=item;best=d.DedicatedVideoMemory;}else item->Release();}factory->Release();
  if(!adapter)fail("no hardware adapter");DXGI_ADAPTER_DESC1 d;adapter->GetDesc1(&d);std::fwprintf(stderr,L"sprite adapter: %ls\n",d.Description);
 }
 ID3D11Device* device=nullptr;ID3D11DeviceContext* ctx=nullptr;D3D_FEATURE_LEVEL level;
 check(D3D11CreateDevice(adapter,warp?D3D_DRIVER_TYPE_WARP:D3D_DRIVER_TYPE_UNKNOWN,nullptr,0,nullptr,0,D3D11_SDK_VERSION,&device,&level,&ctx),"device");if(adapter)adapter->Release();
 ID3DBlob *vs=compile("vs","vs_5_0"),*ps=compile("ps","ps_5_0"),*solid=compile("solid","ps_5_0");
 ID3D11VertexShader* vshader;ID3D11PixelShader *pshader,*sshader;
 check(device->CreateVertexShader(vs->GetBufferPointer(),vs->GetBufferSize(),nullptr,&vshader),"vertex shader");
 check(device->CreatePixelShader(ps->GetBufferPointer(),ps->GetBufferSize(),nullptr,&pshader),"sprite shader");
 check(device->CreatePixelShader(solid->GetBufferPointer(),solid->GetBufferSize(),nullptr,&sshader),"solid shader");vs->Release();ps->Release();solid->Release();
 D3D11_BUFFER_DESC bd={};bd.ByteWidth=96;bd.Usage=D3D11_USAGE_DEFAULT;bd.BindFlags=D3D11_BIND_CONSTANT_BUFFER;ID3D11Buffer* constants;check(device->CreateBuffer(&bd,nullptr,&constants),"constants");
 D3D11_TEXTURE2D_DESC td={};td.Width=w;td.Height=h;td.MipLevels=td.ArraySize=1;td.Format=DXGI_FORMAT_R8G8B8A8_UNORM;td.SampleDesc.Count=1;td.Usage=D3D11_USAGE_DEFAULT;td.BindFlags=D3D11_BIND_RENDER_TARGET;
 ID3D11Texture2D *target;std::vector<ID3D11Texture2D*> staging(slots);check(device->CreateTexture2D(&td,nullptr,&target),"target");ID3D11RenderTargetView* rtv;check(device->CreateRenderTargetView(target,nullptr,&rtv),"target view");
 td.Usage=D3D11_USAGE_STAGING;td.BindFlags=0;td.CPUAccessFlags=D3D11_CPU_ACCESS_READ;for(auto& s:staging)check(device->CreateTexture2D(&td,nullptr,&s),"readback");
 D3D11_BLEND_DESC blend={};auto& b=blend.RenderTarget[0];b.BlendEnable=TRUE;b.SrcBlend=b.SrcBlendAlpha=D3D11_BLEND_ONE;b.DestBlend=b.DestBlendAlpha=D3D11_BLEND_INV_SRC_ALPHA;b.BlendOp=b.BlendOpAlpha=D3D11_BLEND_OP_ADD;b.RenderTargetWriteMask=D3D11_COLOR_WRITE_ENABLE_ALL;ID3D11BlendState* bs;check(device->CreateBlendState(&blend,&bs),"blend");
 D3D11_RASTERIZER_DESC rd={};rd.FillMode=D3D11_FILL_SOLID;rd.CullMode=D3D11_CULL_NONE;rd.DepthClipEnable=TRUE;ID3D11RasterizerState* rs;check(device->CreateRasterizerState(&rd,&rs),"rasterizer");
 D3D11_SAMPLER_DESC sd={};sd.Filter=D3D11_FILTER_MIN_MAG_MIP_LINEAR;sd.AddressU=sd.AddressV=sd.AddressW=D3D11_TEXTURE_ADDRESS_CLAMP;sd.MaxLOD=D3D11_FLOAT32_MAX;ID3D11SamplerState* sampler;check(device->CreateSamplerState(&sd,&sampler),"sampler");
 ctx->VSSetShader(vshader,nullptr,0);ctx->VSSetConstantBuffers(0,1,&constants);ctx->PSSetConstantBuffers(0,1,&constants);ctx->PSSetSamplers(0,1,&sampler);ctx->IASetPrimitiveTopology(D3D11_PRIMITIVE_TOPOLOGY_TRIANGLESTRIP);
 ctx->OMSetRenderTargets(1,&rtv,nullptr);ctx->OMSetBlendState(bs,nullptr,0xffffffff);ctx->RSSetState(rs);D3D11_VIEWPORT vp={0,0,(float)w,(float)h,0,1};ctx->RSSetViewports(1,&vp);
 struct Texture{ID3D11ShaderResourceView* view;size_t bytes;};
 std::unordered_map<uint32_t,Texture> textures;size_t totalBytes=0;uint32_t lastID=0;
 auto loadTextures=[&](uint32_t nt){
 if(nt>10000||textures.size()+nt>10000)fail("texture count bound");
 for(uint32_t i=0;i<nt;i++){
  uint32_t id=read<uint32_t>(f),tw=read<uint32_t>(f),th=read<uint32_t>(f);size_t bytes=size_t(tw)*th*4;totalBytes+=bytes;
  if(!id||id<=lastID||!tw||!th||tw>16384||th>16384||bytes>128*1024*1024||totalBytes>512*1024*1024)fail("texture bound");lastID=id;
  std::vector<uint8_t> pixels(bytes);read(f,pixels.data(),bytes);D3D11_TEXTURE2D_DESC d={};d.Width=tw;d.Height=th;d.MipLevels=d.ArraySize=1;d.Format=DXGI_FORMAT_R8G8B8A8_UNORM;d.SampleDesc.Count=1;d.Usage=D3D11_USAGE_IMMUTABLE;d.BindFlags=D3D11_BIND_SHADER_RESOURCE;
  D3D11_SUBRESOURCE_DATA data={pixels.data(),tw*4,0};ID3D11Texture2D* tex;check(device->CreateTexture2D(&d,&data,&tex),"sprite texture");ID3D11ShaderResourceView* srv;check(device->CreateShaderResourceView(tex,nullptr,&srv),"sprite view");tex->Release();textures.emplace(id,Texture{srv,bytes});
 }};
 _setmode(_fileno(stdout),_O_BINARY);std::setvbuf(stdout,nullptr,_IOFBF,1024*1024);
 std::vector<uint8_t> pixels(size_t(w)*h*4);const float clear[4]={0,0,0,0};
 uint32_t submitted=0,written=0;uint64_t copiedBytes=0;
 auto drain=[&](){
  auto s=staging[written%slots];D3D11_MAPPED_SUBRESOURCE mapped;
  // Map(flags=0) waits for this slot's CopyResource. Later copies may already
  // be queued; there is no need to poll a separate completion query.
  check(ctx->Map(s,0,D3D11_MAP_READ,0,&mapped),"map readback");
  const uint8_t* output=(uint8_t*)mapped.pData;
  if(copyOutput||mapped.RowPitch!=w*4){
   for(uint32_t y=0;y<h;y++)std::memcpy(pixels.data()+size_t(y)*w*4,output+size_t(y)*mapped.RowPitch,w*4);
   output=pixels.data();copiedBytes+=pixels.size();
  }
  if(selftest){for(size_t i=0;i<pixels.size();i++)if(output[i]!=0)fail("self test pixels");}
  else if(std::fwrite(output,1,pixels.size(),stdout)!=pixels.size())fail("output pipe");
  // fwrite has consumed the bytes before the mapping may be recycled.
  ctx->Unmap(s,0);written++;
  if(!selftest&&(written==1||written%30==0||written==nf))std::fprintf(stderr,"NICO_PROGRESS %u %u\n",written,nf);
 };
 auto drawFrame=[&](){
  uint64_t seq=read<uint64_t>(f),ts=read<uint64_t>(f);
  if(seq!=submitted||ts!=uint64_t(submitted)*fpsDen*1000/fpsNum)fail("frame sequence or timestamp");
  ctx->ClearRenderTargetView(rtv,clear);uint32_t nc=read<uint32_t>(f);if(nc>100000)fail("command count bound");
  for(uint32_t j=0;j<nc;j++){Command c=read<Command>(f);ctx->UpdateSubresource(constants,0,nullptr,c.rect,0,0);
   for(float v:c.rect)if(!std::isfinite(v))fail("invalid rect");for(float v:c.proj)if(!std::isfinite(v))fail("invalid projection");for(float v:c.color)if(!std::isfinite(v))fail("invalid color");
   if(c.id){auto it=textures.find(c.id);if(it==textures.end())fail("unknown texture");ctx->PSSetShader(pshader,nullptr,0);ctx->PSSetShaderResources(0,1,&it->second.view);}else ctx->PSSetShader(sshader,nullptr,0);
   ctx->Draw(4,0);
  }
  ctx->CopyResource(staging[submitted%slots],target);submitted++;
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
 if(!selftest&&std::fgetc(f)!=EOF)fail("trailing scene data");if(std::fflush(stdout)!=0)fail("flush output");
 if(selftest)std::puts(version);else{
  std::fprintf(stderr,"NICO_STATS copied_bytes=%llu frames=%u\n",static_cast<unsigned long long>(copiedBytes),written);
  std::fprintf(stderr,"NICO_DONE %u\n",written);
 }
 return 0;
}
