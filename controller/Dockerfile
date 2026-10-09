FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /controller ./cmd/controller

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /controller /controller
USER 10001:10001
EXPOSE 8000
HEALTHCHECK --interval=30s --timeout=3s CMD ["/controller", "healthcheck"]
ENTRYPOINT ["/controller"]
