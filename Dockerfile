FROM golang:latest as builder
RUN mkdir /app 
ADD . /app/ 
WORKDIR /app 
RUN go get github.com/caffix/amass
RUN CGO_ENABLED=0 GOOS=linux go build -o main .

FROM scratch
COPY --from=builder /app/main /main
ENTRYPOINT ["/main"]
