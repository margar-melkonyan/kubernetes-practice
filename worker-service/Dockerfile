# Этап сборки
FROM golang:1.23 AS builder

WORKDIR /app

# Копируем go-модули и зависимости
COPY go.mod go.sum ./
RUN go mod download

# Копируем исходный код и собираем
COPY . ./
RUN CGO_ENABLED=0 GOOS=linux go build -o ./worker-service ./main.go

# Этап запуска
FROM debian:bullseye-slim
# Устанавливаем корневые сертификаты (иначе не будут работать HTTPS-запросы)
RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=builder /app/worker-service .

EXPOSE 8080

ENTRYPOINT ["./worker-service"]
