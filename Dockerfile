FROM golang:1.26-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -mod=readonly -trimpath -o cosmprund .

FROM alpine

COPY --from=builder /app/cosmprund /usr/bin/cosmprund

ENTRYPOINT [ "/usr/bin/cosmprund" ]
