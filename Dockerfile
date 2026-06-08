FROM golang:1.25-alpine AS builder

# GOTOOLCHAIN=local tells Go 1.22 to use itself even though go.mod was


# ca-certificates is needed at build time (go mod download over HTTPS)
# and copied into the final image for runtime HTTPS calls (OpenAI, Redis, etc.)
RUN apk add --no-cache ca-certificates

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags='-s -w' -o /helix ./cmd/helix

FROM scratch

# Copy TLS roots so the binary can reach OpenAI, Upstash Redis, and Supabase.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

COPY --from=builder /helix /helix

EXPOSE 8080

ENTRYPOINT ["/helix"]
