FROM golang:1.25-alpine AS builder
# Temporarily building from haruotsu/slackhog PR #8 branch (feat/conversations-history).
# Revert to a pinned release tag once harakeishi/slackhog#8 is released.
RUN apk add --no-cache git && \
    git clone --depth=1 --branch feat/conversations-history \
      https://github.com/haruotsu/slackhog.git /src && \
    cd /src && go build -o /go/bin/slackhog .

FROM alpine:3.20
COPY --from=builder /go/bin/slackhog /usr/local/bin/slackhog
EXPOSE 4112
CMD ["slackhog"]
