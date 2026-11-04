FROM golang:1.27.3-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/edu-agent-server ./cmd/server \
 && CGO_ENABLED=0 go build -o /out/edu-agent-migrate ./cmd/migrate \
 && CGO_ENABLED=0 go build -o /out/edu-agent-admin ./cmd/admin

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && adduser -D edu
WORKDIR /home/edu
USER edu
COPY --from=build /out/ /usr/local/bin/
ENV EDU_ENV=production EDU_LISTEN=0.0.0.0:4020
EXPOSE 4020
CMD ["edu-agent-server"]
