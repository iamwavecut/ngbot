# Gatekeeper telegram bot (spam protection)

## Installation prerequisites
- Docker

## Installation
 ```shell
git clone git@github.com:iamwavecut/ngbot.git ngbot && cd ngbot
mkdir -p var/etc var/resources
cp dist/etc/config.example.yml var/etc/config.yml
# edit var/etc config to fill in required fields
cp -R dist/resources/* var/resources
docker-compose up
```
Then add your bot to the chat of your choice and give him administrative privileges to restrict users and delete messages.