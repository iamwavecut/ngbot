# :shield: Telegram Chat Gatekeeper bot
> Get rid of noisy spam with ease


## Installation


### "I'm a teapot"

Ok, I've got something for ya.
1. Invite the [Quarantino](https://tg.me/nedoibot) into your chat group. 
2. Promote him to Admin with at least **Ban** and **Delete** permissions enabled.
3. *(optional)* Message`/lang ru` to change chat language.
4. ...
5. PROFIT.

>I respect your privacy, so **I do NOT log messages nor collecting personal data**. This bot is hosted on my personal VDS instance, and it's completely private. Happy chatting!


### "I'm too young to die"
1. Create a bot via [BotFather](https://t.me/BotFather).
2. Finally, **enable group messages access** for your bot, it's essential for your bot to be able to see newcomers.
3. Memorize the unique **token** string of your bot.
4. Have recent version of [Docker](https://www.docker.com/get-started).
5. Obtain code either via `git clone` :arrow_upper_right: or by [downloading zip](https://github.com/iamwavecut/ngbot/archive/refs/heads/master.zip) and extracting it.
6. Open terminal app of your choice and navigate into the code folder.
7. Run this command, replacing the **T** with the actual token string
```
docker compose build --build-arg NG_TOKEN=<REPLACE_THIS>
docker compose up -d --no-recreate
```
9. Add your bot to chat, give him permissions to **Ban** and **Delete**.
10. Change bot language for this chat, if needed.
    - `/lang ru` (`en`, `ru`)
11. `docker compose stop` to stop bot `docker compose start` to get it up and running again.
12. `docker compose down` to remove bot's artifacs.
13. `rm ~/.ngbot/bot.db` to start clean.


### "Hurt me plenty"
```shell
NG_DIR=${GOPATH-$HOME/go}/src/github.com/iamwavecut/ngbot
git clone git@github.com:iamwavecut/ngbot.git ${NG_DIR}
cd ${NG_DIR}

NG_TOKEN=<REPLACE_THIS>
docker build . --build-arg NG_TOKEN=$NG_TOKEN -t ngbot
// token gets baked into container, so you just simply
docker run ngbot
```
Override baked variables by providing them as runtime flags
```shell
docker run -e NG_TOKEN=<ANOTHER_TOKEN> ngbot
```


### "Ultra-Violence"
```shell
NG_DIR=${GOPATH-$HOME/go}/src/github.com/iamwavecut/ngbot
git clone git@github.com:iamwavecut/ngbot.git ${NG_DIR}
cd ${NG_DIR}

NG_TOKEN=<REPLACE_THIS>
NG_LANG=en
NG_HANDLERS=admin,gatekeeper
NG_LOG_LEVEL=6
CGO_ENABLE=1 go run .
```


## Configuration

> All configuration is meant to be passed as build time arguments, however, you are free to modify env vars at runtime at your own risk.

| Required | Variable name | Description | Default | Options |
|---|---|---|---|---|
| :heavy_check_mark: | `NG_TOKEN` | Telegram BOT API token |  |  |
| :x: | `NG_LANG` | Default language to use in new chats. | `en` | `en`, `ru` |
| :x: | `NG_HANDLERS` | If for some silly reason you want to get rid of admin or gateway function. Or if you are awesome and want to add yours. Or to change an invocation order. Go for it! | `admin,gatekeeper` | any combination of comma-separated default items. |
| :x: | `NG_LOG_LEVEL` | Limits the logs spam, maximum verbosity by default. | `6` | 0=Panic, 1=Fatal, 2=Error, 3=Warn, 4=Info, 5=Debug, 6=Trace |

## TODO

- [ ] Individual chat's settings (behaviours, timeouts, custom welcome messages, etc).
- [ ] Interactive  handy chat settings UI in private.
- [ ] Web UI for chat owners.
> Feel free to add your requests in issues.
