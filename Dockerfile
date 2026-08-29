FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cloudlet ./cmd/api

FROM alpine:3.22
RUN apk add --no-cache ca-certificates poppler-utils && adduser -D -u 10001 cloudlet
COPY --from=build /out/cloudlet /usr/local/bin/cloudlet
USER cloudlet
EXPOSE 18080
ENTRYPOINT ["/usr/local/bin/cloudlet"]
