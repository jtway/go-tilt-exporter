# go-tilt-exporter image for the k3s cluster (MQTT mode only: no Bluetooth
# in a pod). Build for the cluster's arch: --platform linux/amd64.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/jtway/go-tilt-exporter/pkg/telemetry.Version=${VERSION}" \
      -o /out/tilt-exporter .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/tilt-exporter /usr/local/bin/tilt-exporter
# Config is mounted at /etc/tilt-exporter/config.yaml; secrets come as env.
WORKDIR /etc/tilt-exporter
EXPOSE 9100
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/tilt-exporter"]
