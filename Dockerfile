FROM golang:1.14-alpine AS builder

WORKDIR /app

COPY go.mod .
COPY go.sum .
RUN go mod download

COPY . .
RUN go build -o ./app .

FROM alpine:latest

WORKDIR /app
COPY --from=builder /app .

EXPOSE 8080

ENTRYPOINT ["./app"]