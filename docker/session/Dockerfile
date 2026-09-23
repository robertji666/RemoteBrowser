FROM golang:1.25-bookworm AS webrtc-service-builder

WORKDIR /src
COPY webrtc-service/go.mod webrtc-service/go.sum* ./
RUN go mod download
COPY webrtc-service/ ./
RUN CGO_ENABLED=0 go build -o /out/webrtc-service .

FROM golang:1.25-bookworm AS input-service-builder

WORKDIR /src
COPY input-service/go.mod input-service/go.sum* ./
RUN go mod download
COPY input-service/ ./
RUN CGO_ENABLED=0 go build -o /out/input-service .

FROM golang:1.25-bookworm AS file-service-builder

WORKDIR /src
COPY file-service/ ./
RUN GO111MODULE=off CGO_ENABLED=0 go build -o /out/file-service .

FROM debian:13-slim

ENV DEBIAN_FRONTEND=noninteractive

COPY --from=webrtc-service-builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

RUN sed -i 's|http://deb.debian.org|https://deb.debian.org|g' /etc/apt/sources.list.d/debian.sources && \
    apt-get update && apt-get install -y --no-install-recommends \
    chromium \
    xvfb \
    fluxbox \
    tigervnc-standalone-server \
    tigervnc-viewer \
    novnc \
    websockify \
    pulseaudio \
    pulseaudio-utils \
    ffmpeg \
    xdotool \
    xclip \
    python3 \
    curl \
    ca-certificates \
    fonts-noto-cjk \
    fonts-noto-color-emoji \
    supervisor \
    && rm -rf /var/lib/apt/lists/*

RUN groupadd --gid 1000 rbuser && useradd --uid 1000 --gid rbuser -m -s /bin/bash rbuser && \
    mkdir -p /home/rbuser/profile /home/rbuser/Downloads /home/rbuser/tmp && \
    chown -R rbuser:rbuser /home/rbuser

COPY entrypoint.sh /usr/local/bin/entrypoint.sh
COPY browser-lifecycle.sh /usr/local/bin/browser-lifecycle.sh
COPY profile-setup.py /usr/local/bin/profile-setup.py
COPY audio-startup.sh /usr/local/bin/audio-startup.sh
COPY healthcheck.sh /usr/local/bin/session-healthcheck
COPY supervisord.conf /etc/supervisor/conf.d/supervisord.conf
COPY audio-service.py /usr/local/bin/audio-service
COPY chromium-policy.json /etc/chromium/policies/managed/remotebrowser.json
COPY download-extension/ /opt/remotebrowser/download-extension/
COPY --from=file-service-builder /out/file-service /usr/local/bin/file-service
COPY --from=webrtc-service-builder /out/webrtc-service /usr/local/bin/webrtc-service
COPY --from=input-service-builder /out/input-service /usr/local/bin/input-service
RUN chmod +x /usr/local/bin/entrypoint.sh /usr/local/bin/session-healthcheck /usr/local/bin/file-service /usr/local/bin/audio-service /usr/local/bin/webrtc-service /usr/local/bin/input-service && \
    chmod -R a+rX /opt/remotebrowser/download-extension /etc/chromium/policies/managed

USER rbuser
WORKDIR /home/rbuser

ENV DISPLAY=:1
ENV VNC_PORT=5901
ENV NOVNC_PORT=6080
ENV AUDIO_SERVICE_PORT=6081
ENV RB_WEBRTC_PORT=6082
ENV RB_INPUT_PORT=6084
ENV RB_WEBRTC_UDP_PORT=6083
ENV RB_WEBRTC_ICE_IP=127.0.0.1
ENV RB_SCREEN_WIDTH=1280
ENV RB_SCREEN_HEIGHT=752
ENV RB_SCREEN_DEPTH=24
ENV RB_CHROME_WINDOW_TOP=32
ENV RB_CHROME_WINDOW_BOTTOM=0
ENV RB_WEBRTC_WIDTH=1280
ENV RB_WEBRTC_HEIGHT=720
ENV RB_WEBRTC_FRAMERATE=24
ENV RB_WEBRTC_VIDEO_CODEC=vp8
ENV RB_WEBRTC_VIDEO_BITRATE=1800k
ENV RB_WEBRTC_AUDIO_BITRATE=96k
ENV FILE_SERVICE_PORT=8081
ENV RB_PROFILE_DIR=/home/rbuser/profile
ENV RB_DOWNLOADS_DIR=/home/rbuser/Downloads

EXPOSE 6080 6081 6082 6084 6083/udp 8081

HEALTHCHECK --interval=10s --timeout=5s --start-period=30s --retries=3 \
    CMD /usr/local/bin/session-healthcheck

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
