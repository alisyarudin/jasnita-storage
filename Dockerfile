FROM node:20-alpine AS web
WORKDIR /src/web
COPY web/package*.json ./
RUN npm ci --no-audit --no-fund
COPY web/ .
RUN npm run build

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/console/dist ./internal/console/dist
RUN CGO_ENABLED=0 go build -ldflags '-s -w' -o /jasstore ./cmd/jasstore

FROM alpine:3.20
RUN adduser -D -u 1000 jasstore && mkdir -p /data && chown jasstore /data
COPY --from=build /jasstore /usr/local/bin/jasstore
USER jasstore
ENV JASSTORE_DATA_DIR=/data JASSTORE_S3_ADDR=:9000 JASSTORE_CONSOLE_ADDR=:9001
VOLUME /data
EXPOSE 9000 9001
ENTRYPOINT ["jasstore", "-env", "/data/.env"]
