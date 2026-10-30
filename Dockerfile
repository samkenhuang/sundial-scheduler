FROM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.23 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /internal/web/dist ./internal/web/dist
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/sched ./cmd/sched

FROM golang:1.23
WORKDIR /app
COPY --from=build /out/sched /usr/local/bin/sched
COPY migrations /app/migrations
ENTRYPOINT ["sched"]
