FROM golang:1.24-alpine

WORKDIR /app

# Copy go.mod và go.sum để tải thư viện
COPY go.mod go.sum ./
RUN go mod download

# Copy toàn bộ mã nguồn vào container
COPY . .

# Build ứng dụng Go thành file thực thi
RUN CGO_ENABLED=0 GOOS=linux go build -o main .

EXPOSE 8080

CMD ["./main"]