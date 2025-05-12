FROM golang:1.23-alpine as builder

LABEL maintainer "GUIDEOPS <guideops@zendesk.com>"

ENV GOPATH /go
WORKDIR /go/src/github.com/zendesk/raingutter

COPY . .

RUN CGO_ENABLED=0 GOARCH=amd64 go build -a -mod=vendor -o /mock-raindrops mock-raindrops.go
FROM alpine

RUN addgroup -g 1000 -S raindrops && \
    adduser -u 1000 -S raindrops -G raindrops
USER 1000

LABEL maintainer "GUIDEOPS <guideops@zendesk.com>"

COPY --from=builder /mock-raindrops /bin

CMD /bin/mock-raindrops
