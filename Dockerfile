# build stage
FROM golang:1.21-alpine
RUN mkdir -p /go/src/app

COPY service/go.* /go/src/app/
WORKDIR /go/src/app
RUN go mod download

WORKDIR /go/src/app
ADD service/cmd cmd
WORKDIR /go/src/app/cmd/server
RUN go build -o /app-server

RUN apk --no-cache update
RUN apk --no-cache upgrade
RUN apk add --no-cache tzdata
RUN cp /usr/share/zoneinfo/Europe/Berlin /etc/localtime
RUN echo "Europe/Berlin" > /etc/timezone

CMD [ "/app-server" ]