function sessionPageData(sessionId) {
  return {
    filePanelOpen: false,
    uploadError: '',
    isFullscreen: false,
    remoteFullscreen: false,
    toolbarRevealed: false,
    toolbarHover: false,
    toolbarHideTimer: null,
    fullscreenHandler: null,
    fullscreenPollTimer: null,
    clipboardStatus: 'pending',
    remoteClipboardStatus: 'idle',
    clipboardAutoSync: false,
    clipboardPollTimer: null,
    clipboardPollInterval: 1500,
    lastLocalClipboardText: null,
    clipboardSyncBusy: false,
    webrtcStatus: 'connecting',
    peerConnection: null,
    audioStatus: 'pending',
    audioEnabled: false,
    audioContext: null,
    audioGainNode: null,
    audioSources: new Set(),
    audioSocket: null,
    audioNextTime: 0,
    audioSampleRate: 48000,
    dragOver: false,
    resizeHandler: null,
    inputSocket: null,
    inputReconnectTimer: null,
    inputReconnectAttempts: 0,
    inputStatus: 'connecting',
    sessionGone: false,
    textKeyCodes: new Set(),
    compositionActive: false,
    pendingMouseMove: null,
    mouseMoveTimer: null,
    lastMouseMoveAt: 0,
    mouseMoveInterval: 33,
    pendingWheelX: 0,
    pendingWheelY: 0,
    wheelTimer: null,
    lastWheelAt: 0,
    lastWheelDirection: '',
    wheelInterval: 180,
    wheelCooldown: 220,
    wheelMinDelta: 40,
    remoteWidth: 1280,
    remoteHeight: 720,

    init() {
      this.fitRemoteScreen();
      this.resizeHandler = () => this.fitRemoteScreen();
      window.addEventListener('resize', this.resizeHandler);
      this.fullscreenHandler = () => {
        this.updateFullscreenState();
        window.requestAnimationFrame(() => this.fitRemoteScreen());
      };
      document.addEventListener('fullscreenchange', this.fullscreenHandler);
      document.addEventListener('webkitfullscreenchange', this.fullscreenHandler);
      this.fullscreenPollTimer = window.setInterval(() => this.pollRemoteFullscreen(), 500);
      this.$watch('filePanelOpen', () => {
        window.requestAnimationFrame(() => this.fitRemoteScreen());
      });
      this.sessionErrorHandler = (event) => {
        const xhr = event.detail && event.detail.xhr;
        if (xhr && xhr.status === 404) this.handleSessionGone();
      };
      document.body.addEventListener('htmx:responseError', this.sessionErrorHandler);
      this.startInput();
      this.startWebRTC();
    },

    destroy() {
      if (this.resizeHandler) {
        window.removeEventListener('resize', this.resizeHandler);
        this.resizeHandler = null;
      }
      if (this.sessionErrorHandler) {
        document.body.removeEventListener('htmx:responseError', this.sessionErrorHandler);
        this.sessionErrorHandler = null;
      }
      if (this.fullscreenHandler) {
        document.removeEventListener('fullscreenchange', this.fullscreenHandler);
        document.removeEventListener('webkitfullscreenchange', this.fullscreenHandler);
        this.fullscreenHandler = null;
      }
      this.clearToolbarHideTimer();
      if (this.fullscreenPollTimer) {
        window.clearInterval(this.fullscreenPollTimer);
        this.fullscreenPollTimer = null;
      }
      this.stopInput();
      this.stopClipboardPolling();
      this.audioEnabled = false;
      this.stopWebRTC('fallback');
      this.stopAudio();
    },

    fitRemoteScreen() {
      const container = document.getElementById('noVNC_container');
      const screen = container ? container.querySelector('.remote-screen') : null;
      if (!container || !screen) return;
      screen.style.width = '';
      screen.style.height = '';
    },

    clearToolbarHideTimer() {
      if (this.toolbarHideTimer) {
        window.clearTimeout(this.toolbarHideTimer);
        this.toolbarHideTimer = null;
      }
    },

    documentFullscreenActive() {
      return Boolean(
        document.fullscreenElement ||
        document.webkitFullscreenElement ||
        document.mozFullScreenElement ||
        document.msFullscreenElement
      );
    },

    updateFullscreenState() {
      const active = this.documentFullscreenActive() || this.remoteFullscreen;
      if (active === this.isFullscreen) return;
      this.isFullscreen = active;
      this.toolbarRevealed = false;
      this.toolbarHover = false;
      this.clearToolbarHideTimer();
      window.requestAnimationFrame(() => this.fitRemoteScreen());
    },

    pollRemoteFullscreen() {
      if (this.sessionGone) return;
      fetch(`/sessions/${sessionId}/input/state`, { cache: 'no-store' })
        .then((response) => response.ok ? response.json() : null)
        .then((state) => {
          if (!state) return;
          this.remoteFullscreen = state.fullscreen === true;
          this.updateFullscreenState();
        })
        .catch(() => {});
    },

    revealToolbar() {
      if (!this.isFullscreen) return;
      this.toolbarRevealed = true;
      this.clearToolbarHideTimer();
      this.toolbarHideTimer = window.setTimeout(() => {
        this.toolbarHideTimer = null;
        if (this.toolbarHover) {
          this.revealToolbar();
          return;
        }
        this.toolbarRevealed = false;
      }, 1800);
    },

    handlePagePointerMove(event) {
      if (!this.isFullscreen) return;
      const toolbarHeight = 56;
      if (event.clientY <= toolbarHeight) this.revealToolbar();
    },

    async toggleFullscreen() {
      try {
        const active = this.documentFullscreenActive();
        if (active) {
          if (document.exitFullscreen) await document.exitFullscreen();
          else if (document.webkitExitFullscreen) document.webkitExitFullscreen();
          return;
        }
        // A fullscreen state owned by the remote Chromium window must be
        // exited from the remote player's own control; do not stack another
        // fullscreen layer on top of it.
        if (this.remoteFullscreen) return;
        const root = document.querySelector('.session-view');
        if (!root) return;
        if (root.requestFullscreen) await root.requestFullscreen();
        else if (root.webkitRequestFullscreen) root.webkitRequestFullscreen();
      } catch (err) {
        console.error('Fullscreen request failed:', err);
      }
    },

    startInput() {
      if (this.inputSocket) return;
      const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const ws = new WebSocket(`${protocol}//${window.location.host}/sessions/${sessionId}/input`);
      this.inputSocket = ws;
      this.inputStatus = 'connecting';
      ws.onopen = () => {
        this.inputStatus = 'connected';
        this.inputReconnectAttempts = 0;
      };
      ws.onmessage = (event) => {
        try {
          const msg = JSON.parse(event.data);
          if (msg.type === 'clipboardValue' && navigator.clipboard) {
            navigator.clipboard.writeText(msg.text || '')
              .then(() => { this.remoteClipboardStatus = 'synced'; })
              .catch(() => { this.remoteClipboardStatus = 'error'; });
          } else if (msg.type === 'clipboardValue') {
            this.remoteClipboardStatus = 'error';
          }
        } catch (_) {}
      };
      ws.onclose = () => {
        if (this.inputSocket === ws) {
          this.inputSocket = null;
          this.inputStatus = 'error';
          if (this.webrtcStatus === 'connected' || this.webrtcStatus === 'connecting') {
            this.stopWebRTC('fallback');
          }
          this.checkSessionBeforeReconnect();
        }
      };
      ws.onerror = () => ws.close();
    },

    checkSessionBeforeReconnect() {
      if (this.sessionGone || this.inputReconnectTimer) return;
      fetch(`/api/sessions/${sessionId}`, { cache: 'no-store' })
        .then((response) => {
          if (response.status === 404) {
            this.handleSessionGone();
            return;
          }
          this.scheduleInputReconnect();
        })
        .catch(() => this.scheduleInputReconnect());
    },

    scheduleInputReconnect() {
      if (this.sessionGone || this.inputReconnectTimer) return;
      this.inputReconnectAttempts += 1;
      const delay = Math.min(1000 * (2 ** Math.min(this.inputReconnectAttempts - 1, 4)), 15000);
      this.inputReconnectTimer = window.setTimeout(() => {
        this.inputReconnectTimer = null;
        this.startInput();
      }, delay);
    },

    handleSessionGone() {
      if (this.sessionGone) return;
      this.sessionGone = true;
      this.inputStatus = 'closed';
      this.stopInput();
      this.stopClipboardPolling();
      this.stopWebRTC('fallback');
      this.stopAudio();
      const fileList = document.getElementById('file-list');
      if (fileList) fileList.remove();
    },

    stopInput() {
      if (this.inputReconnectTimer) {
        window.clearTimeout(this.inputReconnectTimer);
        this.inputReconnectTimer = null;
      }
      if (this.mouseMoveTimer) {
        window.clearTimeout(this.mouseMoveTimer);
        this.mouseMoveTimer = null;
      }
      this.pendingMouseMove = null;
      if (this.wheelTimer) {
        window.clearTimeout(this.wheelTimer);
        this.wheelTimer = null;
      }
      this.pendingWheelX = 0;
      this.pendingWheelY = 0;
      if (this.inputSocket) {
        const socket = this.inputSocket;
        this.inputSocket = null;
        this.inputStatus = 'closed';
        socket.close();
      }
    },

    focusInputTarget() {
      const input = document.querySelector('.ime-input');
      if (input && this.usingWebRTCInput()) {
        input.focus({ preventScroll: true });
        return;
      }
      const screen = document.querySelector('.remote-screen');
      if (screen) screen.focus({ preventScroll: true });
    },

    sendInput(message) {
      if (!this.inputSocket || this.inputSocket.readyState !== WebSocket.OPEN) return false;
      this.inputSocket.send(JSON.stringify(message));
      return true;
    },

    pointerPosition(event) {
      const target = document.querySelector('.remote-screen');
      const rect = target.getBoundingClientRect();
      const x = Math.max(0, Math.min(this.remoteWidth - 1, Math.round((event.clientX - rect.left) * this.remoteWidth / rect.width)));
      const y = Math.max(0, Math.min(this.remoteHeight - 1, Math.round((event.clientY - rect.top) * this.remoteHeight / rect.height)));
      return { x, y };
    },

    handlePointerDown(event) {
      if (!this.usingWebRTCInput()) return;
      event.preventDefault();
      this.focusInputTarget();
      event.currentTarget.setPointerCapture(event.pointerId);
      const pos = this.pointerPosition(event);
      this.flushMouseMove(pos);
      this.sendInput({ type: 'mouseDown', button: event.button });
    },

    handlePointerMove(event) {
      if (!this.usingWebRTCInput()) return;
      event.preventDefault();
      this.queueMouseMove(this.pointerPosition(event));
    },

    handlePointerUp(event) {
      if (!this.usingWebRTCInput()) return;
      event.preventDefault();
      const pos = this.pointerPosition(event);
      this.flushMouseMove(pos);
      this.sendInput({ type: 'mouseUp', button: event.button });
    },

    queueMouseMove(pos) {
      this.pendingMouseMove = pos;
      const now = performance.now();
      const elapsed = now - this.lastMouseMoveAt;
      if (elapsed >= this.mouseMoveInterval) {
        this.flushMouseMove();
        return;
      }
      if (!this.mouseMoveTimer) {
        this.mouseMoveTimer = window.setTimeout(() => {
          this.mouseMoveTimer = null;
          this.flushMouseMove();
        }, this.mouseMoveInterval - elapsed);
      }
    },

    flushMouseMove(pos = null) {
      const next = pos || this.pendingMouseMove;
      if (!next) return;
      if (this.mouseMoveTimer) {
        window.clearTimeout(this.mouseMoveTimer);
        this.mouseMoveTimer = null;
      }
      this.pendingMouseMove = null;
      this.lastMouseMoveAt = performance.now();
      this.sendInput({ type: 'mouseMove', ...next });
    },

    handleWheel(event) {
      if (!this.usingWebRTCInput()) return;
      event.preventDefault();
      this.queueWheel(event.deltaX, event.deltaY);
    },

    queueWheel(deltaX, deltaY) {
      this.pendingWheelX += deltaX;
      this.pendingWheelY += deltaY;
      const now = performance.now();
      const elapsed = now - this.lastWheelAt;
      if (elapsed >= this.wheelInterval) {
        this.flushWheel();
        return;
      }
      if (!this.wheelTimer) {
        this.wheelTimer = window.setTimeout(() => {
          this.wheelTimer = null;
          this.flushWheel();
        }, this.wheelInterval - elapsed);
      }
    },

    flushWheel() {
      const dominantDelta = Math.abs(this.pendingWheelY) >= Math.abs(this.pendingWheelX)
        ? this.pendingWheelY
        : this.pendingWheelX;
      if (Math.abs(dominantDelta) < this.wheelMinDelta) return;
      if (this.wheelTimer) {
        window.clearTimeout(this.wheelTimer);
        this.wheelTimer = null;
      }
      const vertical = Math.abs(this.pendingWheelY) >= Math.abs(this.pendingWheelX);
      const direction = `${vertical ? 'y' : 'x'}:${dominantDelta > 0 ? 1 : -1}`;
      const now = performance.now();
      this.pendingWheelX = 0;
      this.pendingWheelY = 0;
      if (direction === this.lastWheelDirection && now - this.lastWheelAt < this.wheelCooldown) {
        return;
      }
      this.lastWheelAt = now;
      this.lastWheelDirection = direction;
      const deltaX = vertical ? 0 : (dominantDelta > 0 ? 120 : -120);
      const deltaY = vertical ? (dominantDelta > 0 ? 120 : -120) : 0;
      this.sendInput({ type: 'wheel', deltaX, deltaY });
    },

    handleKeyDown(event) {
      if (!this.usingWebRTCInput()) return;
      const isIMEInput = event.target && event.target.classList && event.target.classList.contains('ime-input');
      // Keep native IME key handling intact. The committed text is forwarded
      // from compositionend instead of trying to synthesize intermediate keys.
      if (event.isComposing || this.compositionActive || event.key === 'Process') return;

      // Let the hidden textarea receive printable keys. This is important on
      // macOS Pinyin IME, which may emit a few keydown events before
      // compositionstart; forwarding those would leak the pinyin letters to
      // the remote browser before the composed Chinese text arrives.
      if (isIMEInput && this.shouldSendText(event)) return;

      event.preventDefault();

      if (this.shouldSendText(event)) {
        this.textKeyCodes.add(event.code || event.key);
        this.sendInput({ type: 'text', text: event.key });
        return;
      }

      this.sendInput({ type: 'keyDown', key: event.key, code: event.code });
    },

    handleKeyUp(event) {
      if (!this.usingWebRTCInput()) return;
      const isIMEInput = event.target && event.target.classList && event.target.classList.contains('ime-input');
      if (event.isComposing || this.compositionActive || event.key === 'Process') return;
      if (isIMEInput && this.shouldSendText(event)) return;
      event.preventDefault();
      const textKey = event.code || event.key;
      if (this.textKeyCodes.has(textKey)) {
        this.textKeyCodes.delete(textKey);
        return;
      }
      this.sendInput({ type: 'keyUp', key: event.key, code: event.code });
    },

    handleInput(event) {
      if (!this.usingWebRTCInput() || event.isComposing || this.compositionActive) return;
      const target = event.target;
      const text = target && target.value ? target.value : '';
      if (!text) return;
      this.sendInput({ type: 'text', text });
      target.value = '';
    },

    handleCompositionStart() {
      if (!this.usingWebRTCInput()) return;
      this.compositionActive = true;
    },

    handleCompositionUpdate() {
      if (!this.usingWebRTCInput()) return;
      this.compositionActive = true;
    },

    handleCompositionEnd(event) {
      if (!this.usingWebRTCInput()) return;
      this.compositionActive = false;
      const text = event.data || event.target.value || '';
      if (text) this.sendInput({ type: 'text', text });
      if (event.target && 'value' in event.target) event.target.value = '';
    },

    shouldSendText(event) {
      if (event.metaKey || event.ctrlKey || event.altKey) return false;
      if (!event.key || event.key.length !== 1) return false;
      return event.key !== '\r' && event.key !== '\n';
    },

    usingWebRTCInput() {
      return (this.webrtcStatus === 'connected' || this.webrtcStatus === 'connecting') &&
        this.inputStatus === 'connected';
    },

    noVNCSrc() {
      if (this.webrtcStatus === 'connected' || this.webrtcStatus === 'connecting') {
        return 'about:blank';
      }
      return `/sessions/${sessionId}/novnc/vnc.html?autoconnect=true&reconnect=true&resize=scale&path=sessions%2F${sessionId}%2Fnovnc%2Fwebsockify`;
    },

    requestClipboard() {
      if (!navigator.clipboard) {
        this.clipboardStatus = 'degraded';
        return;
      }
      if (this.clipboardSyncBusy) return;
      this.clipboardSyncBusy = true;
      navigator.clipboard.readText()
        .then((text) => {
          this.clipboardAutoSync = true;
          this.lastLocalClipboardText = text;
          if (this.sendInput({ type: 'clipboardSet', text })) {
            this.clipboardStatus = 'synced';
            this.startClipboardPolling();
          } else {
            this.clipboardAutoSync = false;
            this.clipboardStatus = 'degraded';
          }
        })
        .catch(() => { this.clipboardStatus = 'degraded'; })
        .finally(() => { this.clipboardSyncBusy = false; });
    },

    pullClipboard() {
      this.remoteClipboardStatus = 'syncing';
      if (!this.sendInput({ type: 'clipboardGet' })) this.remoteClipboardStatus = 'error';
    },

    startClipboardPolling() {
      if (this.clipboardPollTimer || !this.clipboardAutoSync) return;
      this.clipboardPollTimer = window.setInterval(() => this.pollClipboard(), this.clipboardPollInterval);
    },

    stopClipboardPolling() {
      if (this.clipboardPollTimer) {
        window.clearInterval(this.clipboardPollTimer);
        this.clipboardPollTimer = null;
      }
    },

    pollClipboard() {
      if (!this.clipboardAutoSync || this.clipboardSyncBusy || document.hidden || !navigator.clipboard) return;
      this.clipboardSyncBusy = true;
      navigator.clipboard.readText()
        .then((text) => {
          if (text === this.lastLocalClipboardText) return;
          this.lastLocalClipboardText = text;
          if (this.sendInput({ type: 'clipboardSet', text })) {
            this.clipboardStatus = 'synced';
          }
        })
        .catch(() => {
          // Clipboard reads can be denied outside a user gesture. Keep the
          // explicit button available as a permission-recovery fallback.
          this.clipboardAutoSync = false;
          this.clipboardStatus = 'degraded';
          this.stopClipboardPolling();
        })
        .finally(() => { this.clipboardSyncBusy = false; });
    },

    clipboardLabel() {
      if (this.clipboardStatus === 'synced') return '\u2713 \u672c\u673a \u2192 \u8fdc\u7a0b \u81ea\u52a8';
      if (this.clipboardStatus === 'degraded') return '\u26a0 \u672c\u673a \u2192 \u8fdc\u7a0b \u5931\u8d25';
      return '\u672c\u673a \u2192 \u8fdc\u7a0b';
    },

    remoteClipboardLabel() {
      if (this.remoteClipboardStatus === 'syncing') return '\u8fdc\u7a0b \u2192 \u672c\u673a \u540c\u6b65\u4e2d';
      if (this.remoteClipboardStatus === 'synced') return '\u2713 \u8fdc\u7a0b \u2192 \u672c\u673a \u5df2\u5199\u5165';
      if (this.remoteClipboardStatus === 'error') return '\u26a0 \u5199\u5165\u5931\u8d25\uff0c\u91cd\u8bd5';
      return '\u8fdc\u7a0b \u2192 \u672c\u673a';
    },

    audioLabel() {
      if (this.audioStatus === 'connected') return '\u266b \u58f0\u97f3\u5df2\u5f00\u542f';
      if (this.audioStatus === 'connecting') return '\u266b \u58f0\u97f3\u8fde\u63a5\u4e2d';
      if (this.audioStatus === 'error') return '\u26a0 \u91cd\u8bd5\u58f0\u97f3';
      return '\u266b \u542f\u7528\u58f0\u97f3';
    },

    webrtcLabel() {
      if (this.webrtcStatus === 'connected') return 'WebRTC \u5df2\u8fde\u63a5';
      if (this.webrtcStatus === 'connecting') return 'WebRTC \u8fde\u63a5\u4e2d';
      if (this.webrtcStatus === 'fallback') return 'noVNC \u6a21\u5f0f';
      return 'WebRTC \u91cd\u8bd5';
    },

    toggleWebRTC() {
      if (this.webrtcStatus === 'connected' || this.webrtcStatus === 'connecting') {
        this.stopWebRTC('fallback');
        return;
      }
      this.startWebRTC();
    },

    async startWebRTC() {
      if (!window.RTCPeerConnection) {
        this.webrtcStatus = 'fallback';
        if (this.audioEnabled && !this.audioSocket) this.startAudio();
        return;
      }

      // A PCM stream must never overlap the WebRTC audio track.
      this.stopAudio();
      this.stopWebRTC('connecting');
      const video = document.getElementById('webrtc_video');
      const pc = new RTCPeerConnection({ iceServers: [] });
      this.peerConnection = pc;
      this.webrtcStatus = 'connecting';

      pc.addTransceiver('video', { direction: 'recvonly' });
      pc.addTransceiver('audio', { direction: 'recvonly' });

      pc.ontrack = (event) => {
        if (this.peerConnection !== pc) return;
        if (video && video.srcObject !== event.streams[0]) {
          video.srcObject = event.streams[0];
          video.muted = !this.audioEnabled;
          video.volume = 1;
          video.play()
            .then(() => {
              if (this.peerConnection === pc && this.audioEnabled) {
                this.audioStatus = 'connected';
              }
            })
            .catch(() => {
              if (this.peerConnection === pc && this.audioEnabled) {
                // Keep the preference so the toolbar can retry after a
                // browser autoplay rejection.
                video.muted = true;
                this.audioStatus = 'error';
              }
            });
        }
      };

      pc.onconnectionstatechange = () => {
        if (this.peerConnection !== pc) return;
        if (pc.connectionState === 'connected') {
          this.webrtcStatus = 'connected';
        } else if (pc.connectionState === 'failed' || pc.connectionState === 'closed') {
          this.stopWebRTC('error');
        } else if (pc.connectionState === 'disconnected') {
          window.setTimeout(() => {
            if (this.peerConnection === pc && pc.connectionState === 'disconnected') {
              this.stopWebRTC('error');
            }
          }, 1500);
        }
      };

      try {
        const offer = await pc.createOffer();
        await pc.setLocalDescription(offer);
        await this.waitForIceGathering(pc);

        const response = await fetch(`/sessions/${sessionId}/webrtc/offer`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(pc.localDescription)
        });
        if (!response.ok) throw new Error(`WebRTC offer failed: ${response.status}`);

        const answer = await response.json();
        if (this.peerConnection !== pc) {
          pc.close();
          return;
        }
        await pc.setRemoteDescription(answer);
      } catch (err) {
        console.error(err);
        if (this.peerConnection === pc) {
          this.stopWebRTC('error');
        } else {
          pc.close();
        }
      }
    },

    waitForIceGathering(pc) {
      if (pc.iceGatheringState === 'complete') return Promise.resolve();
      return new Promise((resolve) => {
        const timeout = window.setTimeout(resolve, 2000);
        pc.addEventListener('icegatheringstatechange', () => {
          if (pc.iceGatheringState === 'complete') {
            window.clearTimeout(timeout);
            resolve();
          }
        });
      });
    },

    stopWebRTC(nextStatus = 'fallback') {
      if (this.peerConnection) {
        this.peerConnection.close();
        this.peerConnection = null;
      }
      const video = document.getElementById('webrtc_video');
      if (video) {
        video.muted = true;
        video.pause();
        video.srcObject = null;
      }
      this.webrtcStatus = nextStatus;
      if (nextStatus === 'fallback' && !this.sessionGone && this.audioEnabled && !this.audioSocket) {
        this.startAudio();
      }
    },

    toggleAudio() {
      if (this.webrtcStatus === 'connected') {
        if (this.audioEnabled && this.audioStatus === 'connected') {
          this.disableWebRTCAudio();
        } else {
          this.enableWebRTCAudio();
        }
        return;
      }
      if (this.audioEnabled && this.audioSocket) {
        this.stopAudio();
        this.audioEnabled = false;
        return;
      }
      this.startAudio();
    },

    enableWebRTCAudio() {
      const video = document.getElementById('webrtc_video');
      if (!video || !video.srcObject) {
        this.audioStatus = 'error';
        return;
      }
      this.stopAudio();
      this.audioEnabled = true;
      video.muted = false;
      video.volume = 1;
      video.play()
        .then(() => { this.audioStatus = 'connected'; })
        .catch(() => {
          video.muted = true;
          this.audioStatus = 'error';
        });
    },

    disableWebRTCAudio() {
      this.audioEnabled = false;
      const video = document.getElementById('webrtc_video');
      if (video) video.muted = true;
      this.audioStatus = 'pending';
    },

    startAudio() {
      const AudioContext = window.AudioContext || window.webkitAudioContext;
      if (!AudioContext) {
        this.audioStatus = 'error';
        return;
      }

      this.audioEnabled = true;
      if (!this.audioContext) {
        this.audioContext = new AudioContext({ sampleRate: this.audioSampleRate });
        this.audioGainNode = this.audioContext.createGain();
        this.audioGainNode.connect(this.audioContext.destination);
      }
      this.audioGainNode.gain.value = 1;
      this.audioContext.resume();
      this.audioNextTime = this.audioContext.currentTime + 0.08;
      this.audioStatus = 'connecting';

      const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const ws = new WebSocket(`${protocol}//${window.location.host}/sessions/${sessionId}/audio`);
      ws.binaryType = 'arraybuffer';
      this.audioSocket = ws;

      ws.onopen = () => {
        if (this.audioSocket !== ws) return;
        this.audioStatus = 'connected';
      };
      ws.onmessage = (event) => {
        if (this.audioSocket !== ws) return;
        this.playPcmChunk(event.data);
      };
      ws.onerror = () => {
        if (this.audioSocket !== ws) return;
        this.audioStatus = 'error';
      };
      ws.onclose = () => {
        if (this.audioSocket === ws) {
          this.audioSocket = null;
          if (this.audioStatus === 'connected' || this.audioStatus === 'connecting') {
            this.audioStatus = 'pending';
          }
        }
      };
    },

    stopAudio() {
      if (this.audioSocket) {
        const socket = this.audioSocket;
        this.audioSocket = null;
        socket.close();
      }
      if (this.audioGainNode) this.audioGainNode.gain.value = 0;
      for (const source of this.audioSources) {
        try { source.stop(); } catch (_) {}
        try { source.disconnect(); } catch (_) {}
      }
      this.audioSources.clear();
      this.audioStatus = 'pending';
      this.audioNextTime = 0;
    },

    playPcmChunk(arrayBuffer) {
      if (!this.audioContext || this.audioContext.state === 'closed') return;
      if (!(arrayBuffer instanceof ArrayBuffer) || arrayBuffer.byteLength < 4) return;

      const pcm = new Int16Array(arrayBuffer);
      const frameCount = Math.floor(pcm.length / 2);
      if (frameCount === 0) return;

      const buffer = this.audioContext.createBuffer(2, frameCount, this.audioSampleRate);
      const left = buffer.getChannelData(0);
      const right = buffer.getChannelData(1);
      for (let i = 0, frame = 0; frame < frameCount; frame++, i += 2) {
        left[frame] = pcm[i] / 32768;
        right[frame] = pcm[i + 1] / 32768;
      }

      const source = this.audioContext.createBufferSource();
      source.buffer = buffer;
      source.connect(this.audioGainNode);
      this.audioSources.add(source);
      source.onended = () => {
        this.audioSources.delete(source);
        try { source.disconnect(); } catch (_) {}
      };

      const now = this.audioContext.currentTime;
      if (this.audioNextTime < now + 0.02 || this.audioNextTime > now + 1.0) {
        this.audioNextTime = now + 0.08;
      }
      source.start(this.audioNextTime);
      this.audioNextTime += buffer.duration;
    },

    openFilePicker() {
      const input = document.getElementById('file-input');
      if (input) input.click();
    },

    handleDrop(event) {
      this.dragOver = false;
      const files = event.dataTransfer.files;
      this.uploadFiles(files);
    },

    handleFileSelect(event) {
      const files = event.target.files;
      this.uploadFiles(files);
      event.target.value = '';
    },

    uploadFiles(files) {
      if (!files || files.length === 0) return;
      const selected = Array.from(files);
      this.uploadError = '';
      Promise.all(selected.map((file) => {
        const formData = new FormData();
        formData.append('file', file);

        fetch(`/api/sessions/${sessionId}/files/upload`, {
          method: 'POST',
          body: formData
        })
        .then(res => {
          if (!res.ok) {
            return res.text().then((message) => {
              throw new Error(message || `HTTP ${res.status}`);
            });
          }
          return file;
        })
      })).then((uploaded) => {
        const list = document.getElementById('file-list');
        if (list && typeof htmx !== 'undefined') htmx.trigger(list, 'refresh');
        this.uploadError = '';
      }).catch((err) => {
        console.error('Upload failed:', err);
        this.uploadError = `上传失败：${err.message || '请重试'}`;
      });
    }
  };
}
