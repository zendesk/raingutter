FROM golang:1.12.5-alpine3.9 as builder

RUN addgroup -g 1000 -S raingutter && \
    adduser -u 1000 -S raingutter -G raingutter

ARG version
ENV GOPATH /go
WORKDIR /go/src/github.com/zendesk/raingutter
COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -a -ldflags "-X main.version=${version}" -o /raingutter raingutter/raingutter.go raingutter/socket_stats.go

FROM scratch

COPY --from=builder /raingutter /
COPY --from=builder /etc/passwd /etc/passwd
USER 1000

LABEL maintainer "GUIDEOPS <guideops@zendesk.com>"

CMD ["/raingutter"]
