package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

type offerRequest struct {
	SDP  string `json:"sdp"`
	Type string `json:"type"`
}

type server struct {
	display      string
	audioSource  string
	api          *webrtc.API
	captureSize  string
	captureX     string
	captureY     string
	width        string
	height       string
	framerate    string
	videoCodec   string
	videoBitrate string
	audioBitrate string
	broadcast    *mediaBroadcaster

	mu    sync.Mutex
	peers map[*webrtc.PeerConnection]*peerSession
}

type peerSession struct {
	release   func()
	lease     string
	expiresAt time.Time
}

func main() {
	port := env("RB_WEBRTC_PORT", "6082")
	udpPort := envInt("RB_WEBRTC_UDP_PORT", 6083)
	iceIP := env("RB_WEBRTC_ICE_IP", "127.0.0.1")
	videoCodec := strings.ToLower(env("RB_WEBRTC_VIDEO_CODEC", "vp8"))

	api, err := newWebRTCAPI(udpPort, iceIP, videoCodec)
	if err != nil {
		log.Fatalf("create webrtc api: %v", err)
	}

	width := env("RB_WEBRTC_WIDTH", "1280")
	height := env("RB_WEBRTC_HEIGHT", "720")
	s := &server{
		display:      env("DISPLAY", ":1"),
		audioSource:  env("RB_AUDIO_SOURCE", "rb_audio.monitor"),
		api:          api,
		captureSize:  env("RB_WEBRTC_CAPTURE_SIZE", width+"x"+height),
		captureX:     env("RB_WEBRTC_CAPTURE_X", "0"),
		captureY:     env("RB_WEBRTC_CAPTURE_Y", env("RB_CHROME_WINDOW_TOP", "0")),
		width:        width,
		height:       height,
		framerate:    env("RB_WEBRTC_FRAMERATE", "24"),
		videoCodec:   videoCodec,
		videoBitrate: env("RB_WEBRTC_VIDEO_BITRATE", "1800k"),
		audioBitrate: env("RB_WEBRTC_AUDIO_BITRATE", "96k"),
		peers:        make(map[*webrtc.PeerConnection]*peerSession),
	}
	s.broadcast = newMediaBroadcaster(s)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/offer", s.handleOffer)
	mux.HandleFunc("/lease", s.handleLease)
	mux.HandleFunc("/stats", s.handleStats)
	leaseCtx, stopLeases := context.WithCancel(context.Background())
	defer stopLeases()
	go s.expireLeases(leaseCtx)

	httpServer := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("WebRTC service listening on :%s, ICE UDP %s:%d", port, iceIP, udpPort)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
	s.closeAll()
}

func (s *server) handleOffer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	lease := r.Header.Get("X-RB-Access-Lease")
	if len(lease) != 32 {
		http.Error(w, "manager access lease required", http.StatusForbidden)
		return
	}

	var req offerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	pc, err := s.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	videoTrack, err := webrtc.NewTrackLocalStaticRTP(
		s.videoCodecCapability(),
		"screen",
		"remotebrowser",
	)
	if err != nil {
		_ = pc.Close()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	audioTrack, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
		"audio",
		"remotebrowser",
	)
	if err != nil {
		_ = pc.Close()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	videoSender, err := pc.AddTrack(videoTrack)
	if err != nil {
		_ = pc.Close()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	audioSender, err := pc.AddTrack(audioTrack)
	if err != nil {
		_ = pc.Close()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	go readRTCP(videoSender)
	go readRTCP(audioSender)

	release, err := s.broadcast.add(videoTrack, audioTrack)
	if err != nil {
		_ = pc.Close()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Negotiation has a bounded grace period; the short access lease begins
	// when the answer is ready and can be renewed by the manager.
	ps := &peerSession{release: release, lease: lease, expiresAt: time.Now().Add(15 * time.Second)}
	s.setPeer(pc, ps)

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateFailed ||
			state == webrtc.PeerConnectionStateClosed ||
			state == webrtc.PeerConnectionStateDisconnected {
			s.closePeer(pc)
		}
	})

	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: req.SDP}); err != nil {
		s.closePeer(pc)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		s.closePeer(pc)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	gatherComplete := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		s.closePeer(pc)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	select {
	case <-gatherComplete:
	case <-r.Context().Done():
		s.closePeer(pc)
		return
	case <-time.After(10 * time.Second):
		s.closePeer(pc)
		http.Error(w, "ICE negotiation timed out", http.StatusGatewayTimeout)
		return
	}
	s.mu.Lock()
	ps.expiresAt = time.Now().Add(8 * time.Second)
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(pc.LocalDescription())
}

func (s *server) videoCodecCapability() webrtc.RTPCodecCapability {
	if s.videoCodec == "h264" {
		return webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeH264,
			ClockRate:   90000,
			SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e02a",
		}
	}
	return webrtc.RTPCodecCapability{
		MimeType:  webrtc.MimeTypeVP8,
		ClockRate: 90000,
	}
}

func newWebRTCAPI(udpPort int, iceIP string, videoCodec string) (*webrtc.API, error) {
	mediaEngine := &webrtc.MediaEngine{}
	videoCapability := webrtc.RTPCodecCapability{
		MimeType:     webrtc.MimeTypeVP8,
		ClockRate:    90000,
		RTCPFeedback: []webrtc.RTCPFeedback{{Type: "nack"}, {Type: "nack", Parameter: "pli"}},
	}
	if videoCodec == "h264" {
		videoCapability = webrtc.RTPCodecCapability{
			MimeType:     webrtc.MimeTypeH264,
			ClockRate:    90000,
			SDPFmtpLine:  "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e02a",
			RTCPFeedback: []webrtc.RTCPFeedback{{Type: "nack"}, {Type: "nack", Parameter: "pli"}},
		}
	}
	if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: videoCapability,
		PayloadType:        96,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, err
	}
	if err := mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeOpus,
			ClockRate: 48000,
			Channels:  2,
		},
		PayloadType: 111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, err
	}

	settingEngine := webrtc.SettingEngine{}
	mux, err := ice.NewMultiUDPMuxFromPort(udpPort)
	if err != nil {
		return nil, err
	}
	settingEngine.SetICEUDPMux(mux)
	if iceIP != "" {
		settingEngine.SetNAT1To1IPs([]string{iceIP}, webrtc.ICECandidateTypeHost)
	}

	return webrtc.NewAPI(
		webrtc.WithMediaEngine(mediaEngine),
		webrtc.WithSettingEngine(settingEngine),
	), nil
}

func (s *server) startVideoEncoder(ctx context.Context, port int) *exec.Cmd {
	framerate := envInt("RB_WEBRTC_FRAMERATE", 24)
	gop := strconv.Itoa(framerate)
	input := s.display
	if s.captureX != "0" || s.captureY != "0" {
		input = fmt.Sprintf("%s+%s,%s", s.display, s.captureX, s.captureY)
	}
	args := []string{
		"-hide_banner", "-loglevel", "warning",
		"-nostdin",
		"-f", "x11grab",
		"-draw_mouse", "0",
		"-video_size", s.captureSize,
		"-framerate", s.framerate,
		"-i", input,
		"-an",
		"-vf", s.videoFilter(),
	}
	if s.videoCodec == "h264" {
		args = append(args,
			"-c:v", "libx264",
			"-preset", "ultrafast",
			"-tune", "zerolatency",
			"-pix_fmt", "yuv420p",
			"-profile:v", "baseline",
			"-level", "4.2",
			"-b:v", s.videoBitrate,
			"-maxrate", s.videoBitrate,
			"-bufsize", "3600k",
			"-g", gop,
			"-keyint_min", gop,
			"-x264-params", "scenecut=0",
			"-bf", "0",
		)
	} else {
		args = append(args,
			"-c:v", "libvpx",
			"-deadline", "realtime",
			"-cpu-used", "8",
			"-threads", "4",
			"-lag-in-frames", "0",
			"-error-resilient", "1",
			"-b:v", s.videoBitrate,
			"-maxrate", s.videoBitrate,
			"-bufsize", "3600k",
			"-g", gop,
			"-keyint_min", gop,
			"-quality", "realtime",
		)
	}
	args = append(args,
		"-f", "rtp",
		"-payload_type", "96",
		fmt.Sprintf("rtp://127.0.0.1:%d?pkt_size=1200", port),
	)
	return startFFmpeg(ctx, args...)
}

func (s *server) videoFilter() string {
	outputSize := s.width + "x" + s.height
	if s.captureSize == outputSize {
		return "fps=" + s.framerate
	}
	return fmt.Sprintf("scale=%s:%s:flags=fast_bilinear,fps=%s", s.width, s.height, s.framerate)
}

func (s *server) startAudioEncoder(ctx context.Context, port int) *exec.Cmd {
	args := []string{
		"-hide_banner", "-loglevel", "warning",
		"-nostdin",
		"-fflags", "+genpts",
		"-use_wallclock_as_timestamps", "1",
		"-f", "pulse",
		"-i", s.audioSource,
		"-vn",
		"-ac", "2",
		"-ar", "48000",
		"-af", "aresample=async=1:first_pts=0",
		"-c:a", "libopus",
		"-b:a", s.audioBitrate,
		"-application", "lowdelay",
		"-frame_duration", "20",
		"-flush_packets", "1",
		"-f", "rtp",
		"-payload_type", "111",
		fmt.Sprintf("rtp://127.0.0.1:%d?pkt_size=1200", port),
	}
	return startFFmpeg(ctx, args...)
}

func startFFmpeg(ctx context.Context, args ...string) *exec.Cmd {
	// #nosec G204 -- the executable is fixed and arguments are passed directly without a shell.
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		log.Printf("start ffmpeg: %v", err)
		return cmd
	}
	go func() {
		if err := cmd.Wait(); err != nil && ctx.Err() == nil {
			log.Printf("ffmpeg exited: %v", err)
		}
	}()
	return cmd
}

type mediaBroadcaster struct {
	server *server

	mu          sync.Mutex
	cancel      context.CancelFunc
	cmds        []*exec.Cmd
	videoConn   *net.UDPConn
	audioConn   *net.UDPConn
	videoTracks map[*webrtc.TrackLocalStaticRTP]struct{}
	audioTracks map[*webrtc.TrackLocalStaticRTP]struct{}
}

func newMediaBroadcaster(s *server) *mediaBroadcaster {
	return &mediaBroadcaster{
		server:      s,
		videoTracks: make(map[*webrtc.TrackLocalStaticRTP]struct{}),
		audioTracks: make(map[*webrtc.TrackLocalStaticRTP]struct{}),
	}
}

func (b *mediaBroadcaster) add(videoTrack, audioTrack *webrtc.TrackLocalStaticRTP) (func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.videoTracks)+len(b.audioTracks) == 0 {
		if err := b.startLocked(); err != nil {
			return nil, err
		}
		log.Printf("first WebRTC peer connected, shared media pipeline started")
	}

	b.videoTracks[videoTrack] = struct{}{}
	b.audioTracks[audioTrack] = struct{}{}

	var once sync.Once
	return func() {
		once.Do(func() {
			b.remove(videoTrack, audioTrack)
		})
	}, nil
}

func (b *mediaBroadcaster) remove(videoTrack, audioTrack *webrtc.TrackLocalStaticRTP) {
	b.mu.Lock()
	defer b.mu.Unlock()

	delete(b.videoTracks, videoTrack)
	delete(b.audioTracks, audioTrack)

	if len(b.videoTracks)+len(b.audioTracks) == 0 {
		b.stopLocked()
		log.Printf("last WebRTC peer disconnected, shared media pipeline stopped")
	}
}

func (b *mediaBroadcaster) startLocked() error {
	ctx, cancel := context.WithCancel(context.Background())

	videoPort, videoConn, err := listenRTP()
	if err != nil {
		cancel()
		return err
	}
	audioPort, audioConn, err := listenRTP()
	if err != nil {
		_ = videoConn.Close()
		cancel()
		return err
	}

	b.cancel = cancel
	b.videoConn = videoConn
	b.audioConn = audioConn
	b.cmds = []*exec.Cmd{
		b.server.startVideoEncoder(ctx, videoPort),
		b.server.startAudioEncoder(ctx, audioPort),
	}

	go b.forwardRTP(ctx, videoConn, true)
	go b.forwardRTP(ctx, audioConn, false)
	return nil
}

func (b *mediaBroadcaster) stopLocked() {
	if b.cancel != nil {
		b.cancel()
		b.cancel = nil
	}
	for _, cmd := range b.cmds {
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGTERM)
		}
	}
	b.cmds = nil
	if b.videoConn != nil {
		_ = b.videoConn.Close()
		b.videoConn = nil
	}
	if b.audioConn != nil {
		_ = b.audioConn.Close()
		b.audioConn = nil
	}
}

func (b *mediaBroadcaster) stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stopLocked()
}

func (b *mediaBroadcaster) forwardRTP(ctx context.Context, conn *net.UDPConn, video bool) {
	buf := make([]byte, 1600)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			log.Printf("read rtp: %v", err)
			return
		}

		var pkt rtp.Packet
		if err := pkt.Unmarshal(buf[:n]); err != nil {
			log.Printf("unmarshal rtp: %v", err)
			continue
		}

		tracks := b.tracks(video)
		for _, track := range tracks {
			pktCopy := pkt
			if err := track.WriteRTP(&pktCopy); err != nil {
				if ctx.Err() == nil {
					log.Printf("write rtp: %v", err)
				}
				b.removeTrack(video, track)
			}
		}
	}
}

func (b *mediaBroadcaster) tracks(video bool) []*webrtc.TrackLocalStaticRTP {
	b.mu.Lock()
	defer b.mu.Unlock()

	source := b.audioTracks
	if video {
		source = b.videoTracks
	}
	tracks := make([]*webrtc.TrackLocalStaticRTP, 0, len(source))
	for track := range source {
		tracks = append(tracks, track)
	}
	return tracks
}

func (b *mediaBroadcaster) removeTrack(video bool, track *webrtc.TrackLocalStaticRTP) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if video {
		delete(b.videoTracks, track)
	} else {
		delete(b.audioTracks, track)
	}
	if len(b.videoTracks)+len(b.audioTracks) == 0 {
		b.stopLocked()
	}
}

func listenRTP() (int, *net.UDPConn, error) {
	addr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		return 0, nil, err
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return 0, nil, err
	}
	port := conn.LocalAddr().(*net.UDPAddr).Port
	return port, conn, nil
}

func readRTCP(sender *webrtc.RTPSender) {
	buf := make([]byte, 1500)
	for {
		if _, _, err := sender.Read(buf); err != nil {
			return
		}
	}
}

func (s *server) setPeer(pc *webrtc.PeerConnection, ps *peerSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.peers[pc] = ps
}

func (s *server) closePeer(pc *webrtc.PeerConnection) {
	s.mu.Lock()
	ps, ok := s.peers[pc]
	if ok {
		delete(s.peers, pc)
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	if ps.release != nil {
		ps.release()
	}
	_ = pc.Close()
}

func (s *server) closeAll() {
	s.mu.Lock()
	peers := make([]*webrtc.PeerConnection, 0, len(s.peers))
	for pc := range s.peers {
		peers = append(peers, pc)
	}
	s.mu.Unlock()
	for _, pc := range peers {
		s.closePeer(pc)
	}
	if s.broadcast != nil {
		s.broadcast.stop()
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}
