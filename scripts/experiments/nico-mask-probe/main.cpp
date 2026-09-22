#include "mask_renderer.h"
#include <algorithm>
#include <chrono>
#include <csignal>
#include <cstring>
#include <filesystem>
#include <fstream>
#include <iomanip>
#include <iostream>
#include <numeric>
#include <sstream>
#ifndef NOMINMAX
#define NOMINMAX
#endif
#include <windows.h>
#include <psapi.h>
extern "C" {
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavfilter/avfilter.h>
#include <libavfilter/buffersrc.h>
#include <libavfilter/buffersink.h>
#include <libavutil/opt.h>
#include <libavutil/md5.h>
}
using Clock = std::chrono::steady_clock;
static double ms(Clock::time_point start) { return std::chrono::duration<double,std::milli>(Clock::now()-start).count(); }
static volatile std::sig_atomic_t cancelled = 0;
static void signal_cancel(int) { cancelled=1; }
static void check(int code,const char* op) {
    if(code<0) { char err[256]; av_strerror(code,err,sizeof(err)); throw std::runtime_error(std::string(op)+": "+err); }
}
static void require(bool b,const char* op) { if(!b) throw std::runtime_error(op); }
struct Frame {
    AVFrame* p=av_frame_alloc();
    Frame() { require(p!=nullptr,"frame allocation failed"); }
    ~Frame() { av_frame_free(&p); }
    Frame(const Frame&)=delete; Frame& operator=(const Frame&)=delete;
    void rgba(int w,int h) { p->format=AV_PIX_FMT_RGBA; p->width=w;p->height=h;check(av_frame_get_buffer(p,64),"frame pixels"); }
};
struct Graph {
    AVFilterGraph* g=avfilter_graph_alloc();
    AVFilterContext *src=nullptr,*sink=nullptr;
    Graph(int w,int h,AVRational tb,bool convert) {
        require(g!=nullptr,"graph allocation failed"); g->nb_threads=2;
        try {
            std::ostringstream args; args<<"video_size="<<w<<"x"<<h<<":pix_fmt="<<AV_PIX_FMT_RGBA<<":time_base="<<tb.num<<"/"<<tb.den<<":pixel_aspect=1/1";
            check(avfilter_graph_create_filter(&src,avfilter_get_by_name("buffer"),"src",args.str().c_str(),nullptr,g),"buffer");
            check(avfilter_graph_create_filter(&sink,avfilter_get_by_name("buffersink"),"sink",nullptr,nullptr,g),"sink");
            AVFilterContext* mid=nullptr;
            check(avfilter_graph_create_filter(&mid,avfilter_get_by_name(convert?"format":"null"),"mid",convert?"pix_fmts=yuv420p":nullptr,nullptr,g),"mid");
            check(avfilter_link(src,0,mid,0),"link src");check(avfilter_link(mid,0,sink,0),"link sink");
            check(avfilter_graph_config(g,nullptr),"graph config");
        } catch(...) {avfilter_graph_free(&g);throw;}
    }
    ~Graph(){avfilter_graph_free(&g);}
};
static void lifetime_test() {
    // Holding a real downstream AVFrame must prevent producer slot reuse.
    Graph g(64,32,{1,60},false); Frame in,out;
    in.rgba(64,32); in.p->pts=7; auto* address=in.p->data[0];address[0]=91;
    require(av_frame_is_writable(in.p),"initial writable");
    check(av_buffersrc_add_frame_flags(g.src,in.p,AV_BUFFERSRC_FLAG_KEEP_REF),"selftest submit");
    require(in.p->data[0]==address,"KEEP_REF reset input");
    require(!av_frame_is_writable(in.p),"queued reference not held");
    check(av_buffersink_get_frame(g.sink,out.p),"selftest output");
    require(out.p->data[0]==address && out.p->pts==7 && out.p->data[0][0]==91,"null graph copied or changed frame");
    require(!av_frame_is_writable(in.p),"output reference not held");
    av_frame_unref(out.p); require(av_frame_is_writable(in.p),"released slot not writable");
    check(av_buffersrc_add_frame_flags(g.src,nullptr,0),"selftest EOF");
    require(av_buffersink_get_frame(g.sink,out.p)==AVERROR_EOF,"selftest drain");
    int rc=av_buffersrc_add_frame_flags(g.src,in.p,AV_BUFFERSRC_FLAG_KEEP_REF);
    require(rc<0 && in.p->data[0]==address && av_frame_is_writable(in.p),"failed submit damaged ownership");
}
struct Encoder {
    AVCodecContext* c=nullptr; AVFormatContext* f=nullptr; AVPacket* pkt=nullptr; AVStream* stream=nullptr;
    int64_t packets=0; bool eof=false; int eagain=0; double work_ms=0;
    Encoder(const std::string& path,int w,int h,AVRational fps) {
        try {
            const AVCodec* codec=avcodec_find_encoder_by_name("libx264");require(codec,"libx264 unavailable");
            c=avcodec_alloc_context3(codec);require(c,"encoder allocation");
            check(avformat_alloc_output_context2(&f,nullptr,"mp4",path.c_str()),"mux allocation");
            require(f,"mux context");c->width=w;c->height=h;c->pix_fmt=AV_PIX_FMT_YUV420P;
            c->time_base={fps.den,fps.num};c->framerate=fps;c->thread_count=8;c->sample_aspect_ratio={1,1};
            if(f->oformat->flags&AVFMT_GLOBALHEADER)c->flags|=AV_CODEC_FLAG_GLOBAL_HEADER;
            check(av_opt_set(c->priv_data,"preset","veryfast",0),"preset");
            check(av_opt_set(c->priv_data,"crf","26",0),"crf");
            check(av_opt_set(c->priv_data,"x264-params","sliced-threads=0:slices=1:slice-max-size=0:slice-max-mbs=0",0),"single slice");
            check(avcodec_open2(c,codec,nullptr),"encoder open");
            stream=avformat_new_stream(f,nullptr);require(stream,"stream allocation");stream->time_base=c->time_base;
            check(avcodec_parameters_from_context(stream->codecpar,c),"codec parameters");
            check(avio_open(&f->pb,path.c_str(),AVIO_FLAG_WRITE),"output open");
            check(avformat_write_header(f,nullptr),"mux header");pkt=av_packet_alloc();require(pkt,"packet allocation");
        } catch(...) {cleanup();throw;}
    }
    void cleanup(){av_packet_free(&pkt);avcodec_free_context(&c);if(f){if(f->pb)avio_closep(&f->pb);avformat_free_context(f);f=nullptr;}}
    ~Encoder(){cleanup();}
    int receive() {
        int count=0;
        for(;;){int rc=avcodec_receive_packet(c,pkt);if(rc==AVERROR(EAGAIN))break;if(rc==AVERROR_EOF){eof=true;break;}check(rc,"receive packet");
            // This probe is CFR. libx264 may leave packet duration unset; MP4's
            // edit list would then discard the final displayed frame.
            if(pkt->duration<=0)pkt->duration=1;
            av_packet_rescale_ts(pkt,c->time_base,stream->time_base);pkt->stream_index=stream->index;
            check(av_interleaved_write_frame(f,pkt),"mux packet");av_packet_unref(pkt);++count;++packets;}
        return count;
    }
    void send(AVFrame* frame) {
        auto t=Clock::now();
        for(;;){int rc=avcodec_send_frame(c,frame);if(rc==AVERROR(EAGAIN)){++eagain;require(receive()>0,"encoder EAGAIN without progress");continue;}check(rc,"send frame");break;}
        receive();work_ms+=ms(t);
    }
    void finish(){send(nullptr);require(eof,"encoder did not reach EOF");check(av_write_trailer(f),"mux trailer");}
};
struct Digest {
    AVMD5* p=av_md5_alloc();
    Digest(){require(p,"md5 allocation");av_md5_init(p);}
    ~Digest(){av_free(p);}
    std::string finish(){uint8_t out[16];av_md5_final(p,out);std::ostringstream s;for(auto b:out)s<<std::hex<<std::setw(2)<<std::setfill('0')<<int(b);return s.str();}
};
int main(int argc,char** argv) {
    av_log_set_level(AV_LOG_ERROR);std::signal(SIGINT,signal_cancel);
    try {
        if(argc==2 && std::string(argv[1])=="--selftest"){lifetime_test();std::cout<<"{\"lifetime\":\"PASS\",\"pointer_equal\":true,\"held_not_writable\":true,\"release_writable\":true,\"EOF\":true,\"failed_submit_preserved\":true,\"av_version\":\""<<av_version_info()<<"\"}\n";return 0;}
        if(argc==3 && std::string(argv[1])=="--encoder-selftest"){
            Encoder enc(argv[2],64,32,{60,1});bool encountered=false;
            for(int n=0;n<120;++n){Frame f;f.p->format=AV_PIX_FMT_YUV420P;f.p->width=64;f.p->height=32;f.p->pts=n;f.p->duration=1;
                check(av_frame_get_buffer(f.p,64),"test pixels");
                for(int plane=0;plane<3;++plane)for(int y=0;y<(plane?16:32);++y)std::memset(f.p->data[plane]+y*f.p->linesize[plane],plane?128:32+n,plane?32:64);
                if(encountered){enc.send(f.p);continue;}
                auto* address=f.p->data[0];int rc=avcodec_send_frame(enc.c,f.p);
                if(rc==AVERROR(EAGAIN)){encountered=true;require(address==f.p->data[0]&&f.p->pts==n,"EAGAIN changed input");enc.send(f.p);}else check(rc,"fill encoder queue");
            }
            enc.finish();require(encountered&&enc.eagain>0&&enc.packets==120,"EAGAIN retry/drain test");
            std::cout<<"{\"encoder_eagain_retry\":\"PASS\",\"packets\":"<<enc.packets<<",\"retries\":"<<enc.eagain<<"}\n";return 0;
        }
        require(argc>=5,"usage: probe scene.nmf bits C|D output.mp4 [--frames N] [--dump DIR] [--dump-all] [--cancel-at N] [--fail-at N]");
        std::string path=argv[1],mode=argv[3],dump;int bits=std::stoi(argv[2]);size_t limit=0;int cancel_at=-1,fail_at=-1;bool dump_all=false;
        require(mode=="C"||mode=="D","mode must be C or D");
        for(int a=5;a<argc;){std::string opt=argv[a++];
            if(opt=="--dump-all"){dump_all=true;continue;}
            require(a<argc,"option needs value");
            if(opt=="--frames")limit=std::stoull(argv[a++]);else if(opt=="--dump")dump=argv[a++];else if(opt=="--cancel-at")cancel_at=std::stoi(argv[a++]);else if(opt=="--fail-at")fail_at=std::stoi(argv[a++]);else throw std::runtime_error("unknown option "+opt);}
        auto total=Clock::now();auto scene=nm::load_scene(path);double read_ms=ms(total);require(!scene.frames.empty(),"scene has no frames");
        auto prep=Clock::now();nm::Renderer renderer(scene,bits);double prep_ms=ms(prep);
        AVRational fps={int(scene.fps_num),int(scene.fps_den)},tb={fps.den,fps.num};
        Graph graph(scene.width,scene.height,tb,true);Encoder enc(argv[4],scene.width,scene.height,fps);
        Frame pool[3];for(auto& slot:pool)slot.rgba(scene.width,scene.height);Frame filtered;
        std::vector<uint8_t> scratch(mode=="C"?size_t(scene.width)*scene.height*4:0);
        size_t wanted=limit?limit:scene.frames.size();require(wanted<=108000,"frame run limit");
        std::vector<double> latencies;latencies.reserve(wanted);
        double bg_ms=0,draw_ms=0,copy_ms=0,filter_ms=0;uint64_t copied=0;size_t frames=0,filtered_count=0,waits=0;
        std::ofstream events;
        if(!dump.empty()){
            std::filesystem::create_directories(std::filesystem::u8path(dump));
            events.open(std::filesystem::u8path(dump+"/events.csv"));require(bool(events),"events open");
            events<<"frame_pts,boundary,resource_id,generation,copy_B,refcount,writable,latency_ms\n";
        }
        Digest digest;
        auto drain=[&](){size_t before=filtered_count;for(;;){auto ft=Clock::now();int rc=av_buffersink_get_frame(graph.sink,filtered.p);filter_ms+=ms(ft);
            if(rc==AVERROR(EAGAIN)||rc==AVERROR_EOF)break;
            check(rc,"filter receive");
            require(filtered.p->pts==int64_t(filtered_count),"filter PTS missing/duplicated");enc.send(filtered.p);av_frame_unref(filtered.p);++filtered_count;}return filtered_count-before;};
        auto run=Clock::now();
        for(size_t n=0;n<wanted;++n){
            if(cancelled||int(n)==cancel_at){cancelled=1;break;}if(int(n)==fail_at)throw std::runtime_error("injected failure");
            Frame* slot=nullptr;for(int k=0;k<3;++k){auto& candidate=pool[(n+k)%3];if(av_frame_is_writable(candidate.p)){slot=&candidate;break;}}
            if(!slot){++waits;require(drain()>0,"pool exhausted without progress");for(auto& candidate:pool)if(av_frame_is_writable(candidate.p)){slot=&candidate;break;}require(slot,"pool remains referenced");}
            auto start=Clock::now();auto stage=start;
            uint8_t* pixels=mode=="C"?scratch.data():slot->p->data[0];int stride=mode=="C"?int(scene.width*4):slot->p->linesize[0];
            for(unsigned y=0;y<scene.height;++y){auto* row=reinterpret_cast<uint32_t*>(pixels+size_t(y)*stride);std::fill_n(row,scene.width,0xff403020u);}bg_ms+=ms(stage);
            stage=Clock::now();renderer.draw(n%scene.frames.size(),pixels,stride);draw_ms+=ms(stage);
            stage=Clock::now();if(mode=="C"){for(unsigned y=0;y<scene.height;++y)std::memcpy(slot->p->data[0]+size_t(y)*slot->p->linesize[0],scratch.data()+size_t(y)*scene.width*4,scene.width*4);copied+=uint64_t(scene.width)*scene.height*4;}copy_ms+=ms(stage);
            latencies.push_back(ms(start));slot->p->pts=n;slot->p->duration=1;slot->p->time_base=tb;
            if(!dump.empty()){
                for(unsigned y=0;y<scene.height;++y)av_md5_update(digest.p,slot->p->data[0]+size_t(y)*slot->p->linesize[0],scene.width*4);
                if(dump_all||n==0||n==30||n==75||n==180||n==300||n+1==wanted){std::ofstream file(std::filesystem::u8path(dump+"/frame-"+std::to_string(n)+".rgba"),std::ios::binary);require(bool(file),"dump open");for(unsigned y=0;y<scene.height;++y)file.write(reinterpret_cast<char*>(slot->p->data[0]+size_t(y)*slot->p->linesize[0]),scene.width*4);require(bool(file),"dump write");}
            }
            stage=Clock::now();check(av_buffersrc_add_frame_flags(graph.src,slot->p,AV_BUFFERSRC_FLAG_KEEP_REF),"filter submit");filter_ms+=ms(stage);
            if(events)events<<n<<",frame_handoff,"<<(slot-pool)<<","<<(n/3)<<","<<(mode=="C"?uint64_t(scene.width)*scene.height*4:0)<<","<<av_buffer_get_ref_count(slot->p->buf[0])<<","<<av_frame_is_writable(slot->p)<<","<<latencies.back()<<"\n";
            ++frames;drain();
        }
        check(av_buffersrc_add_frame_flags(graph.src,nullptr,0),"filter EOF");drain();
        require(filtered_count==frames,"filter frame count");enc.finish();require(enc.packets==int64_t(frames),"encoded packet count");
        for(auto& slot:pool)require(av_frame_is_writable(slot.p),"frame still referenced after drain");
        double run_ms=ms(run),wall_ms=ms(total);std::sort(latencies.begin(),latencies.end());
        auto percentile=[&](double q){return latencies.empty()?0:latencies[size_t(q*(latencies.size()-1))];};
        auto& stats=renderer.stats();
        PROCESS_MEMORY_COUNTERS_EX memory{};memory.cb=sizeof(memory);
        require(GetProcessMemoryInfo(GetCurrentProcess(),reinterpret_cast<PROCESS_MEMORY_COUNTERS*>(&memory),sizeof(memory)),"process memory counters");
        std::cout<<std::fixed<<std::setprecision(6)<<"{\"bits\":"<<bits<<",\"mode\":\""<<mode<<"\",\"frames\":"<<frames<<",\"packets\":"<<enc.packets<<",\"wall_s\":"<<wall_ms/1000<<",\"run_s\":"<<run_ms/1000<<",\"fps\":"<<(frames*1000/std::max(0.001,wall_ms))
          <<",\"read_ms\":"<<read_ms<<",\"prepare_ms\":"<<prep_ms<<",\"pack_ms\":"<<stats.pack_ms<<",\"expand_ms\":"<<stats.expand_ms<<",\"outline_ms\":"<<stats.outline_ms
          <<",\"background_ms\":"<<bg_ms<<",\"compose_ms\":"<<draw_ms<<",\"copy_ms\":"<<copy_ms<<",\"filter_ms\":"<<filter_ms<<",\"encode_mux_ms\":"<<enc.work_ms
          <<",\"raw_mask_B\":"<<stats.raw_mask_bytes<<",\"packed_mask_B\":"<<stats.packed_mask_bytes<<",\"cache_B\":"<<stats.cache_bytes<<",\"cpu_copy_B\":"<<copied
          <<",\"pool_allocs\":3,\"pool_waits\":"<<waits<<",\"source_refs\":"<<frames<<",\"make_writable_calls\":0,\"encoder_eagain\":"<<enc.eagain
          <<",\"format_conversion_frames\":"<<filtered_count<<",\"peak_working_set_B\":"<<memory.PeakWorkingSetSize<<",\"private_commit_B\":"<<memory.PrivateUsage
          <<",\"latency_p50_ms\":"<<percentile(.50)<<",\"latency_p95_ms\":"<<percentile(.95)<<",\"cancelled\":"<<(cancelled?"true":"false")<<",\"raw_md5\":\""<<(dump.empty()?"":digest.finish())<<"\"}\n";
        return cancelled?2:0;
    }catch(const std::exception& e){std::cerr<<"probe: "<<e.what()<<"\n";return 1;}
}
