FROM golang:1.18-stretch
RUN go install github.com/cespare/reflex@latest
COPY reflex.conf /
ENTRYPOINT ["reflex", "-c", "/reflex.conf"]