# syntax=docker/dockerfile:1.4

FROM scratch as scratch-base

FROM golang:1.23-alpine as builder

RUN addgroup -g 1000 -S raingutter && adduser -u 1000 -S raingutter -G raingutter

ARG version
ENV GOPATH /go
WORKDIR /go/src/github.com/zendesk/raingutter
COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -a -mod=vendor -ldflags "-X main.version=${version}" -o /raingutter raingutter/raingutter.go raingutter/socket_stats.go raingutter/prometheus.go

FROM scratch-base

COPY --from=builder /raingutter /
COPY --from=builder /etc/passwd /etc/passwd
USER 1000

LABEL maintainer "GUIDEOPS <guideops@zendesk.com>"

CMD ["/raingutter"]
