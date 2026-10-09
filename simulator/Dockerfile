FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /simulator ./cmd/simulator \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /load-test ./cmd/load-test

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /simulator /simulator
COPY --from=build /load-test /load-test
USER 10001:10001
ENTRYPOINT ["/simulator"]
