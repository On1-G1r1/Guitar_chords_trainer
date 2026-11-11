FROM golang:1.26-alpine AS builder
WORKDIR /app

# 1. Кэшируем зависимости (меняются редко)
COPY go.mod go.sum ./
RUN go mod download

# 2. Копируем ТОЛЬКО файлы Go (файлы .go). Никакого фронтенда тут нет!
# (Представим, что мы копируем только исходники кода)
COPY *.go ./ 
RUN CGO_ENABLED=0 GOOS=linux go build -o main .

FROM alpine:3.19
WORKDIR /app


COPY --from=builder /app/main .


COPY index.html app.js style.css ./

EXPOSE 8085
CMD ["./main"]
