# :shield: Telegram Chat Gatekeeper bot
> Get rid of the unwanted spam joins out of the box

![Demo](https://user-images.githubusercontent.com/239034/142725561-5fd80514-dae9-4d29-aa19-a7d2ad41e362.png)

Basically, that what happens, if the bot is set up in your chat:
1. Triggered on the events, which introduces new chat members (invite, join, etc). Also works with join requests.
2. Restrict newcomer to be read-only.
3. Set up a challenge for the newcomer, which is a simple task as shown on the image above, but yet, unsolvable for the vast majority of automated spam robots.
4. If the newcomer succeeds in choosing the right answer - restrictions gets fully lifted, challenge ends.
5. Otherwise - newcomer gets banned for 10 minutes (There is a "false-positive" chance, rememeber? Most robots aint coming back, anyway).
6. If the newcomer struggles to answer in a set period of time (defaults to 3 minutes) - challenge automatically fails the same way, as in p.5.
7. After the challenge bot cleans up all related messages, only leaving join notification for the newcomers, that made it. There are no traces of unsuccesful joins left, and that is awesome.

## Troubleshooting
Don't hesitate to contact me

[![telegram](https://user-images.githubusercontent.com/239034/142726254-d3378dee-5b73-41b0-858d-b2a6e85dc735.png)
](https://t.me/WaveCut) [![linkedin](https://user-images.githubusercontent.com/239034/142726236-86c526e0-8fc3-4570-bd2d-fc7723d5dc09.png)
](https://linkedin.com/in/wavecut)

## Installation


### "I'm a teapot"

Ok, I've got something for ya.
1. Invite the [Quarantino](https://tg.me/nedoibot) into your chat group. 
2. Promote him to Admin with at least **Ban**, **Delete**, and **Invite** permissions enabled.
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
8. Add your bot to chat, give him permissions to **Ban**, **Delete**, and **Invite**.
9. Change bot language for this chat, if needed.
    - `/lang ru` (see below for the complete list of available languages)
10. `docker compose stop` to stop bot `docker compose start` to get it up and running again.
11. `docker compose down` to remove bot's artifacs.
12. `rm ~/.ngbot/bot.db` to start clean.


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
| :x: | `NG_LANG` | Default language to use in new chats. | `en` | `bg`, `cs`, `da`, `de`, `el`, `en`, `es`, `et`, `fi`, `fr`, `hu`, `id`, `it`, `ja`, `ko`, `lt`, `lv`, `nb`, `nl`, `pl`, `pt`, `ro`, `ru`, `sk`, `sl`, `sv`, `tr`, `uk`, `zh` |
| :x: | `NG_HANDLERS` | If for some silly reason you want to get rid of admin or gateway function. Or if you are awesome and want to add yours. Or to change an invocation order. Go for it! | `admin,gatekeeper` | any combination of comma-separated default items. |
| :x: | `NG_LOG_LEVEL` | Limits the logs spam, maximum verbosity by default. | `6` | `0`=Panic, `1`=Fatal, `2`=Error, `3`=Warn, `4`=Info, `5`=Debug, `6`=Trace |

## TODO

- [ ] Improve thread safety.
- [ ] Individual chat's settings (behaviours, timeouts, custom welcome messages, etc).
- [ ] Interactive  handy chat settings UI in private.
- [ ] Dynamic plugin system.
- [ ] Handy web UI for chat owners.
> Feel free to add your requests in issues.
