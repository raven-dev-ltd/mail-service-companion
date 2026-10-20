FROM golang:1.27.1-alpine AS build
WORKDIR /source
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /mail-companion ./cmd/mail-companion \
    && mkdir -m 0700 /data

FROM scratch
COPY LICENSE /LICENSE
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /mail-companion /mail-companion
COPY --from=build --chown=10001:10001 /data /data
USER 10001:10001
WORKDIR /data
ENV COMPANION_LISTEN=0.0.0.0:8080 COMPANION_DATA_DIR=/data
EXPOSE 8080
ENTRYPOINT ["/mail-companion"]
