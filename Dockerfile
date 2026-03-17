FROM golang:1.24-alpine AS builder

ENV GOTOOLCHAIN=auto
RUN apk add --no-cache git ca-certificates

WORKDIR /src

# Configure git for private repos
ARG GITLAB_TOKEN
RUN if [ -n "$GITLAB_TOKEN" ]; then \
      git config --global url."https://gitlab-ci-token:${GITLAB_TOKEN}@gitlab.flexinfer.ai/".insteadOf "https://gitlab.flexinfer.ai/"; \
    fi

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /fi-mcp-gateway ./cmd/fi-mcp-gateway

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
RUN adduser -D -u 1000 mcp
USER 1000
COPY --from=builder /fi-mcp-gateway /usr/local/bin/fi-mcp-gateway
ENTRYPOINT ["fi-mcp-gateway"]
