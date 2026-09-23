FROM golang:1.26-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o manager ./cmd/manager

FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /app
COPY --from=builder /app/manager /usr/local/bin/manager
COPY web/ /app/web/
COPY migrations/ /app/migrations/

EXPOSE 8080
ENTRYPOINT ["manager"]
