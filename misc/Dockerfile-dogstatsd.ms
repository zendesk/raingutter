FROM golang:1.13 as builder

LABEL maintainer "GUIDEOPS <guideops@zendesk.com>"

ENV GOPATH /go
WORKDIR /go/src/github.com/zendesk/raingutter
RUN go get -u github.com/catkins/statsd-logger/metrics
RUN go get -u github.com/catkins/statsd-logger/trace

COPY . .

RUN CGO_ENABLED=0 GOARCH=amd64 go build -a -o /mock-dogstatsd mock-dogstatsd.go
FROM alpine

RUN addgroup -g 1000 -S datadog && \
    adduser -u 1000 -S datadog -G datadog
USER 1000

LABEL maintainer "GUIDEOPS <guideops@zendesk.com>"

COPY --from=builder /mock-dogstatsd /bin

CMD /bin/mock-dogstatsd
