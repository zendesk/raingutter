FROM golang:1.19.2-alpine3.16 as builder

RUN addgroup -g 1000 -S raingutter && adduser -u 1000 -S raingutter -G raingutter

ARG version
ENV GOPATH /go
WORKDIR /go/src/github.com/zendesk/raingutter
COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -a -mod=vendor -ldflags "-X main.version=${version}" -o /raingutter raingutter/raingutter.go raingutter/socket_stats.go raingutter/prometheus.go

FROM 713408432298.dkr.ecr.us-west-2.amazonaws.com/base/zendesk/docker-images-base/scratch:master

COPY --from=builder /raingutter /
COPY --from=builder /etc/passwd /etc/passwd
USER 1000

LABEL maintainer "GUIDEOPS <guideops@zendesk.com>"

CMD ["/raingutter"]
