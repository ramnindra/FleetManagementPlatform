FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /device-agent ./cmd/device-agent

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /device-agent /device-agent
USER 10001:10001
ENV AGENT_CONFIG_PATH=/etc/device-agent/config.yaml
VOLUME ["/etc/device-agent"]
ENTRYPOINT ["/device-agent"]
