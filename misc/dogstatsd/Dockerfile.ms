FROM golang:1.16.3-alpine3.13 as builder

LABEL maintainer "GUIDEOPS <guideops@zendesk.com>"

ENV GOPATH /go
WORKDIR /go/src/github.com/zendesk/raingutter

COPY . .

RUN CGO_ENABLED=0 GOARCH=amd64 go build -a -mod=vendor -o /mock-dogstatsd mock-dogstatsd.go
FROM alpine

RUN addgroup -g 1000 -S datadog && \
    adduser -u 1000 -S datadog -G datadog
USER 1000

LABEL maintainer "GUIDEOPS <guideops@zendesk.com>"

COPY --from=builder /mock-dogstatsd /bin

CMD /bin/mock-dogstatsd
