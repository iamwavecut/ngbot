FILE=cmd/ngbot/main.go
NGBOT_CONFIG=$(shell pwd)/etc/config.yml
NGBOT_RESOURCES_PATH=$(shell pwd)/resources

run:
	NGBOT_CONFIG=$(NGBOT_CONFIG) NGBOT_RESOURCES_PATH=$(NGBOT_RESOURCES_PATH) go run $(FILE)

build:
	NGBOT_CONFIG=$(NGBOT_CONFIG) NGBOT_RESOURCES_PATH=$(NGBOT_RESOURCES_PATH)  go build -o ./bin/ngbot $(FILE)