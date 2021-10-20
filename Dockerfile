FROM golang as app-builder
WORKDIR /app
RUN apt-get update -y
RUN apt-get install git -y
COPY . .
RUN CGO_ENABLED=0 go build -o main /app/draft-plugin

FROM alpine:3.9
RUN apk add ca-certificates git
COPY --from=app-builder /app/main /app/main
CMD ["/app/main"]