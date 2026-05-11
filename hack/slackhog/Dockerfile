FROM golang:1.25-alpine AS builder
RUN go install github.com/harakeishi/slackhog@v0.2.3

FROM alpine:3.20
COPY --from=builder /go/bin/slackhog /usr/local/bin/slackhog
EXPOSE 4112
CMD ["slackhog"]
