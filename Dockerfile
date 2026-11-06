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
# Unraid's default nobody:users, so files in appdata stay editable.
USER 99:100
ENV SILO_NEWSLETTER_CONFIG=/data/config.json
VOLUME /data
ENTRYPOINT ["/usr/local/bin/silo-newsletter"]
CMD ["run"]
