FILE=cmd/ngbot/main.go

run:
	go run $(FILE)

build:
	go build -o ngbot $(FILE)