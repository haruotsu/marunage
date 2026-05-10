FROM golang:1.25-alpine AS builder
# v0.2.3+ includes conversations.history (harakeishi/slackhog#8)
# Update the version tag once the release is cut.
RUN go install github.com/harakeishi/slackhog@latest

FROM alpine:3.20
COPY --from=builder /go/bin/slackhog /usr/local/bin/slackhog
EXPOSE 4112
CMD ["slackhog"]
