package nicorender

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"imagepadserver/internal/niconico"

	"golang.org/x/net/websocket"
)

type frameTransportConfig struct {
	Endpoint     string
	Token        string
	SparseFrames bool
}

func (c frameTransportConfig) script(_ int, _ int) string {
	endpoint, _ := json.Marshal(c.Endpoint)
	return `<script>(()=>{
const endpoint=` + string(endpoint) + `;
const sparseFrames=` + strconv.FormatBool(c.SparseFrames) + `;
const canvas=document.getElementById('canvas');
let socket=null, stopped=false, hasFrame=false;
const pending=new Set();
let batch=[],batchCursor=0;
window.__binaryReady=false;
window.__binaryError='';
function fail(message){window.__binaryError=String(message||'binary transport failed');stopped=true;if(socket&&socket.readyState<2)socket.close();}
function capture(out){
  const width=canvas.width,height=canvas.height,row=width*4;
  const gl=canvas.getContext('webgl2');
  if(gl){
    gl.readPixels(0,0,width,height,gl.RGBA,gl.UNSIGNED_BYTE,out);
    const scratch=new Uint8Array(row);
    for(let y=0;y<Math.floor(height/2);y++){
      const top=y*row,bottom=(height-1-y)*row;
      scratch.set(out.subarray(top,top+row));
      out.copyWithin(top,bottom,bottom+row);out.set(scratch,bottom);
    }
    return;
  }
  const ctx=canvas.getContext('2d');
  if(!ctx)throw Error('renderer has no readable canvas context');
  const src=ctx.getImageData(0,0,width,height).data;
  for(let i=0;i<src.length;i+=4){const a=src[i+3];out[i]=Math.round(src[i]*a/255);out[i+1]=Math.round(src[i+1]*a/255);out[i+2]=Math.round(src[i+2]*a/255);out[i+3]=a;}
}
function header(sequence,timeMs,kind,payloadBytes){
  const out=new ArrayBuffer(40+payloadBytes),view=new DataView(out),bytes=new Uint8Array(out);
  bytes[0]=78;bytes[1]=67;bytes[2]=82;bytes[3]=49;bytes[4]=1;bytes[5]=kind;bytes[6]=1;bytes[7]=0;
  view.setBigUint64(8,BigInt(sequence),true);view.setBigUint64(16,BigInt(timeMs),true);view.setUint32(24,payloadBytes,true);
  view.setUint32(28,canvas.width,true);view.setUint32(32,canvas.height,true);view.setUint32(36,0,true);return out;
}
function compactRuns(packet){
  const src=new Uint8Array(packet,40),words=new Uint32Array(packet,40);
  const ranges=[];
  let size=0,i=0;
  while(i<words.length){
    while(i<words.length&&words[i]===0)i++;
    if(i===words.length)break;
    const start=i;let end=i,zeros=0;
    while(i<words.length){
      if(words[i]===0){if(++zeros===4){i++;break;}}else{end=i+1;zeros=0;}
      i++;
    }
    const count=end-start;
    ranges.push(start,count);
    size+=8+count*4;
    if(size>=src.length)return packet;
  }
  const out=new Uint8Array(40+size),view=new DataView(out.buffer);
  out.set(new Uint8Array(packet,0,40));out[5]=3;view.setUint32(24,size,true);
  let offset=40;
  for(let j=0;j<ranges.length;j+=2){
    const start=ranges[j],count=ranges[j+1],from=start*4;
    view.setUint32(offset,start,true);view.setUint32(offset+4,count,true);offset+=8;
    out.set(src.subarray(from,from+count*4),offset);offset+=count*4;
  }
  return out.buffer;
}
socket=new WebSocket(endpoint);socket.binaryType='arraybuffer';
socket.onopen=()=>{if(!stopped)window.__binaryReady=true;};
socket.onerror=()=>fail('binary websocket error');
socket.onclose=()=>{if(!stopped)fail('binary websocket closed');};
socket.onmessage=(event)=>{if(typeof event.data!=='string')return;try{const control=JSON.parse(event.data);if(control.type==='accepted'){if(!pending.delete(control.sequence)){fail('unexpected frame acknowledgement');return;}pump();}else if(control.type==='stop'){batch=[];stopped=true;if(control.reason!=='completed')fail(control.reason||'binary transport stopped');}}catch(error){fail(error);}};
window.__requestDraw=(sequence,timeMs,force)=>{
  if(stopped||!window.__binaryReady||socket.readyState!==1)throw Error(window.__binaryError||'binary transport is not ready');
  if(pending.size>=2)throw Error('binary transport backpressure limit reached');
  const changed=window.__niconi.drawCanvas(Math.floor(timeMs/10),!!force);
  if(typeof changed!=='boolean')throw Error('drawCanvas did not return boolean');
  const full=force||changed||!hasFrame;
  const size=canvas.width*canvas.height*4;
  const packet=header(sequence,timeMs,full?1:2,full?size:0);
  if(full){capture(new Uint8Array(packet,40));hasFrame=true;}
  pending.add(sequence);
  socket.send(full&&sparseFrames?compactRuns(packet):packet);
};
function pump(){
  while(!stopped&&pending.size<2&&batchCursor<batch.length){
    const request=batch[batchCursor++];
    window.__requestDraw(request.sequence,request.timeMs,request.force);
  }
}
window.__requestBatch=(sequence,times,force)=>{
  if(batchCursor<batch.length)throw Error('previous render batch is still queued');
  if(!Array.isArray(times)||times.length<1||times.length>120)throw Error('invalid render batch');
  batch=times.map((timeMs,i)=>({sequence:sequence+i,timeMs,force:!!force||sequence+i===0}));batchCursor=0;
  pump();
};
})();</script>`
}

type frameTransport struct {
	config    frameTransportConfig
	listener  net.Listener
	server    *http.Server
	connCh    chan *websocket.Conn
	done      chan struct{}
	once      sync.Once
	mu        sync.Mutex
	conn      *websocket.Conn
	connected bool
	maxBytes  int
}

func newFrameTransport(width, height int) (*frameTransport, error) {
	maxPayload, err := framePayloadSize(frameHeader{Kind: frameKindFull, Width: uint32(width), Height: uint32(height)})
	if err != nil {
		return nil, err
	}
	if maxPayload > int(^uint(0)>>1)-frameHeaderSize {
		return nil, errors.New("niconico: frame transport payload overflow")
	}
	maxPayload += frameHeaderSize
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("niconico: frame transport listen: %w", err)
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		listener.Close()
		return nil, fmt.Errorf("niconico: frame transport token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)
	t := &frameTransport{
		config: frameTransportConfig{
			Endpoint: "ws://" + listener.Addr().String() + "/ws?token=" + url.QueryEscape(token),
			Token:    token,
		},
		listener: listener,
		connCh:   make(chan *websocket.Conn, 1),
		done:     make(chan struct{}),
		maxBytes: maxPayload,
	}
	wsServer := websocket.Server{
		Handshake: func(_ *websocket.Config, req *http.Request) error {
			if req.URL.Query().Get("token") != token {
				return errors.New("niconico: invalid frame transport token")
			}
			origin := req.Header.Get("Origin")
			if origin != "" && origin != "null" && !strings.HasPrefix(origin, "file://") {
				return errors.New("niconico: invalid frame transport origin")
			}
			return nil
		},
		Handler: websocket.Handler(t.handleConn),
	}
	mux := http.NewServeMux()
	mux.Handle("/ws", wsServer)
	t.server = &http.Server{Handler: mux}
	go func() { _ = t.server.Serve(listener) }()
	return t, nil
}

func (t *frameTransport) handleConn(conn *websocket.Conn) {
	conn.MaxPayloadBytes = t.maxBytes
	t.mu.Lock()
	if t.connected {
		t.mu.Unlock()
		_ = conn.Close()
		return
	}
	t.connected = true
	t.conn = conn
	t.mu.Unlock()
	select {
	case t.connCh <- conn:
	case <-t.done:
		_ = conn.Close()
		return
	default:
		_ = conn.Close()
		return
	}
	<-t.done
	_ = conn.Close()
}

func (t *frameTransport) waitConn(ctx context.Context) (*websocket.Conn, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.done:
		return nil, errors.New("niconico: frame transport closed")
	case conn := <-t.connCh:
		return conn, nil
	}
}

// Read treats WebSocket fragments as one byte stream. Unlike Message.Receive,
// it does not allocate an intermediate buffer for each fragment. The declared
// frame length bounds allocation, and pixels keep ownership of this packet.
func (t *frameTransport) receivePacket(ctx context.Context, conn *websocket.Conn) (packet []byte, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(20 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err = conn.SetReadDeadline(deadline); err != nil {
		return nil, err
	}
	canceled := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = conn.SetReadDeadline(time.Now())
		close(canceled)
	})
	defer func() {
		if !stop() {
			<-canceled
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	var header [frameHeaderSize]byte
	if _, err = io.ReadFull(conn, header[:]); err != nil {
		return nil, err
	}
	if string(header[:4]) != string(frameMagic[:]) || header[4] != frameProtocolVersion || header[6] != frameFormatRGBA8 || header[7] != 0 || binary.LittleEndian.Uint32(header[36:40]) != 0 {
		return nil, errors.New("niconico: invalid frame transport header")
	}
	h := frameHeader{Kind: header[5], Width: binary.LittleEndian.Uint32(header[28:32]), Height: binary.LittleEndian.Uint32(header[32:36])}
	expected, err := framePayloadSize(h)
	if err != nil {
		return nil, err
	}
	payloadBytes := int(binary.LittleEndian.Uint32(header[24:28]))
	if payloadBytes < 0 || payloadBytes > t.maxBytes-frameHeaderSize {
		return nil, errors.New("niconico: frame transport payload exceeds limit")
	}
	if (h.Kind == frameKindFull && payloadBytes != expected) || (h.Kind == frameKindRepeat && payloadBytes != 0) {
		return nil, errors.New("niconico: invalid frame transport payload length")
	}
	if h.Kind == frameKindSparseRuns && payloadBytes >= expected {
		return nil, errors.New("niconico: invalid sparse transport payload length")
	}
	packet = make([]byte, frameHeaderSize+payloadBytes)
	copy(packet, header[:])
	if _, err = io.ReadFull(conn, packet[frameHeaderSize:]); err != nil {
		return nil, err
	}
	return packet, nil
}

func (t *frameTransport) acknowledge(conn *websocket.Conn, sequence uint64) error {
	if err := conn.SetWriteDeadline(time.Now().Add(20 * time.Second)); err != nil {
		return err
	}
	data, err := json.Marshal(struct {
		Type     string `json:"type"`
		Sequence uint64 `json:"sequence"`
	}{"accepted", sequence})
	if err != nil {
		return err
	}
	return websocket.Message.Send(conn, string(data))
}

func (t *frameTransport) stop(conn *websocket.Conn, reason string) {
	if conn != nil {
		data, _ := json.Marshal(struct {
			Type   string `json:"type"`
			Reason string `json:"reason"`
		}{"stop", reason})
		_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
		_ = websocket.Message.Send(conn, string(data))
	}
}

func (t *frameTransport) close() {
	if t == nil {
		return
	}
	t.once.Do(func() {
		close(t.done)
		if t.server != nil {
			_ = t.server.Close()
		}
		if t.listener != nil {
			_ = t.listener.Close()
		}
		t.mu.Lock()
		conn := t.conn
		t.mu.Unlock()
		if conn != nil {
			_ = conn.Close()
		}
	})
}

func requestBinaryDraw(ctx context.Context, session *browserSession, sequence, timeMs uint64, force bool) error {
	expression := "window.__requestDraw(" + strconv.FormatUint(sequence, 10) + "," + strconv.FormatUint(timeMs, 10) + "," + strconv.FormatBool(force) + ")"
	return evaluateBinaryRequest(ctx, session, expression)
}

func requestBinaryBatch(ctx context.Context, session *browserSession, sequence uint64, times []int64, force bool) error {
	encoded, err := json.Marshal(times)
	if err != nil {
		return err
	}
	expression := "window.__requestBatch(" + strconv.FormatUint(sequence, 10) + "," + string(encoded) + "," + strconv.FormatBool(force) + ")"
	return evaluateBinaryRequest(ctx, session, expression)
}

func evaluateBinaryRequest(ctx context.Context, session *browserSession, expression string) error {
	result, err := session.call(ctx, "Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true})
	if err != nil {
		return err
	}
	var envelope struct {
		ExceptionDetails json.RawMessage `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(result, &envelope); err != nil {
		return err
	}
	if len(envelope.ExceptionDetails) > 0 && string(envelope.ExceptionDetails) != "null" {
		return fmt.Errorf("niconico: browser draw: %s", envelope.ExceptionDetails)
	}
	return nil
}

func renderBinary(ctx context.Context, snapshot niconico.Snapshot, options RenderOptions, sink FrameSink) (RenderReport, error) {
	threads := snapshot.RendererThreads()
	clock, _ := niconico.NewFrameClock(options.FPSNum, options.FPSDen)
	frameCount := clock.FrameCountForDurationMs(options.DurationMs)
	report := RenderReport{FrameCount: frameCount, Width: options.Width, Height: options.Height, FPSNum: options.FPSNum, FPSDen: options.FPSDen, RendererLabel: options.RendererLabel}
	if frameCount == 0 {
		return report, nil
	}
	bundlePath, cleanupBundle, err := materializeBundle(options.BundlePath)
	if err != nil {
		return RenderReport{}, err
	}
	defer cleanupBundle()
	transport, err := newFrameTransport(options.Width, options.Height)
	if err != nil {
		return RenderReport{}, err
	}
	defer transport.close()
	transport.config.SparseFrames = options.SparseFrames
	pagePath, err := writeRendererPage(options.Width, options.Height, bundlePath, threads, &transport.config)
	if err != nil {
		return RenderReport{}, err
	}
	defer os.Remove(pagePath)
	session, err := startBrowser(ctx, options.BrowserPath, pagePath)
	if err != nil {
		return RenderReport{}, err
	}
	defer session.close()
	if err := session.waitReady(ctx, true); err != nil {
		transport.stop(nil, "protocol-error")
		return RenderReport{}, err
	}
	conn, err := transport.waitConn(ctx)
	if err != nil {
		return RenderReport{}, err
	}
	lastFull := []byte(nil)
	batchFrames := int64(options.BatchFrames)
	if batchFrames == 0 {
		batchFrames = 1
	}
	for frame := int64(0); frame < frameCount; frame++ {
		select {
		case <-ctx.Done():
			transport.stop(conn, "canceled")
			return RenderReport{}, ctx.Err()
		default:
		}
		timeMs := clock.CommentTimeMs(frame)
		if timeMs < 0 {
			transport.stop(conn, "protocol-error")
			return RenderReport{}, fmt.Errorf("niconico: negative frame time %d", timeMs)
		}
		sequence := uint64(frame)
		force := !options.ReuseUnchanged || frame == 0
		var requestErr error
		if batchFrames == 1 {
			requestErr = requestBinaryDraw(ctx, session, sequence, uint64(timeMs), force)
		} else if frame%batchFrames == 0 {
			times := make([]int64, min(batchFrames, frameCount-frame))
			for i := range times {
				times[i] = clock.CommentTimeMs(frame + int64(i))
			}
			requestErr = requestBinaryBatch(ctx, session, sequence, times, !options.ReuseUnchanged)
		}
		if requestErr != nil {
			transport.stop(conn, "protocol-error")
			return RenderReport{}, fmt.Errorf("niconico: render frame %d: %w", frame, requestErr)
		}
		packet, err := transport.receivePacket(ctx, conn)
		if err != nil {
			transport.stop(conn, "protocol-error")
			return RenderReport{}, fmt.Errorf("niconico: receive frame %d: %w", frame, err)
		}
		want := frameHeader{Sequence: sequence, TimeMs: uint64(timeMs), Width: uint32(options.Width), Height: uint32(options.Height)}
		header, pixels, err := decodeFramePacket(packet, want)
		if err != nil {
			transport.stop(conn, "protocol-error")
			return RenderReport{}, fmt.Errorf("niconico: decode frame %d: %w", frame, err)
		}
		if header.Kind == frameKindRepeat {
			if lastFull == nil {
				transport.stop(conn, "protocol-error")
				return RenderReport{}, fmt.Errorf("niconico: repeat frame %d has no previous full frame", frame)
			}
			pixels = lastFull
		} else {
			lastFull = pixels
		}
		if err := sink.WriteRGBA(ctx, sequence, pixels); err != nil {
			transport.stop(conn, "sink-failed")
			return RenderReport{}, fmt.Errorf("niconico: write frame %d: %w", frame, err)
		}
		if err := transport.acknowledge(conn, sequence); err != nil {
			transport.stop(conn, "protocol-error")
			return RenderReport{}, fmt.Errorf("niconico: acknowledge frame %d: %w", frame, err)
		}
		if options.Progress != nil {
			options.Progress(frame+1, frameCount)
		}
	}
	transport.stop(conn, "completed")
	return report, nil
}
