# Build on the host's native architecture and cross-compile with Go.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/sensors ./cmd/server/main
RUN mkdir /data

FROM gcr.io/distroless/static-debian12:nonroot
# token.json is written to the working directory; mount a volume at /data
# to keep it across restarts.
COPY --from=build --chown=nonroot:nonroot /data /data
WORKDIR /data
COPY --from=build /out/sensors /usr/local/bin/sensors
EXPOSE 8080
ENTRYPOINT ["sensors"]
