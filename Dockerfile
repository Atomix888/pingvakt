# PingVakt-server med agentfiler för alla plattformar.
#   docker build -t pingvakt-server .
#   docker run -d -p 8080:8080 -v pingvakt-data:/data pingvakt-server
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=dev
WORKDIR /src
COPY . .
RUN apk add --no-cache git && if [ ! -s go.sum ]; then go mod tidy; fi && go mod download
ENV CGO_ENABLED=0
RUN set -e; \
    LDF="-s -w -X main.Version=${VERSION}"; \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "$LDF" -o /out/pingvakt-server ./server; \
    mkdir -p /out/agents; \
    for p in linux/amd64 linux/arm64 linux/arm darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do \
      os=${p%/*}; arch=${p#*/}; name=$arch; ext=""; \
      [ "$arch" = arm ] && name=armv7; [ "$os" = windows ] && ext=.exe; \
      GOOS=$os GOARCH=$arch GOARM=7 go build -trimpath -ldflags "$LDF" -o /out/agents/pingvakt-agent-$os-$name$ext ./agent; \
    done

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -H -u 10001 pingvakt && mkdir /data && chown pingvakt /data
COPY --from=build /out/pingvakt-server /usr/local/bin/pingvakt-server
COPY --from=build /out/agents /app/agents
ENV PINGVAKT_DATA=/data PINGVAKT_AGENTS_DIR=/app/agents PINGVAKT_LISTEN=:8080 TZ=Europe/Stockholm
USER pingvakt
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["pingvakt-server"]
