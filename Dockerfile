FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/silo-newsletter ./cmd/silo-newsletter

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/silo-newsletter /usr/local/bin/silo-newsletter
# Starts as root only to give /data to PUID:PGID (default 99:100, Unraid's
# nobody:users), then switches to that user before doing anything else.
ENV SILO_NEWSLETTER_CONFIG=/data/config.json
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/silo-newsletter"]
CMD ["run"]
