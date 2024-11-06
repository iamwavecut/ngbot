# :shield: Telegram Chat Gatekeeper bot
> Get rid of the unwanted spam joins out of the box

![Demo](https://user-images.githubusercontent.com/239034/142725561-5fd80514-dae9-4d29-aa19-a7d2ad41e362.png)

## Join protection
0. Join-time challenge is disabled as for now, due to being buggy, but will be back as option in the future.
> 1. Triggered on the events, which introduces new chat members (invite, join, etc). Also works with **join requests**.
> 2. Restrict newcomer to be read-only.
> 3. Set up a challenge for the newcomer (join request), which is a simple task as shown on the image above, but yet, unsolvable for the vast majority of automated spam robots.
> 4. If the newcomer succeeds in choosing the right answer - restrictions gets fully lifted, challenge ends.
> 5. Otherwise - newcomer gets banned for 10 minutes (There is a "false-positive" chance, rememeber? Most robots aint coming back, anyway).
> 6. If the newcomer struggles to answer in a set period of time (defaults to 3 minutes) - challenge automatically fails the same way, as in p.5.
> 7. After the challenge bot cleans up all related messages, only leaving join notification for the newcomers, that made it. There are no traces of unsuccesful joins left, and that is awesome.

## Spam protection
1. Every chat member first message is being checked for spam using two approaches:
 - **Known spammers DB lookup** - checks if the message author is in the known spammers DB.
 - **GPT-powered content analysis** - asks GPT to analyze the message for harmful content.
2. If the message is considered as spam - newcomer gets kick-banned.
3. If the message is not considered as spam - user becomes a normal trusted chat member.

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
 docker compose build --build-arg NG_TOKEN=<REPLACE_THIS> --build-arg OPENAI_API_KEY=<REPLACE_THIS>
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
OPENAI_API_KEY=<REPLACE_THIS>
docker build . --build-arg NG_TOKEN=$NG_TOKEN --build-arg OPENAI_API_KEY=$OPENAI_API_KEY -t ngbot
// token gets baked into container, so you just simply
docker run ngbot
```
Override baked variables by providing them as runtime flags
```shell
docker run -e NG_TOKEN=<ANOTHER_TOKEN> -e OPENAI_API_KEY=<ANOTHER_OPENAI_API_KEY> ngbot
```


### "Ultra-Violence"
```shell
NG_DIR=${GOPATH-$HOME/go}/src/github.com/iamwavecut/ngbot
git clone git@github.com:iamwavecut/ngbot.git ${NG_DIR}
cd ${NG_DIR}

NG_TOKEN=<REPLACE_THIS>
NG_LANG=en
NG_HANDLERS=admin,gatekeeper,reactor
NG_LOG_LEVEL=6
OPENAI_API_KEY=<REPLACE_THIS>
CGO_ENABLE=1 go run .
```


## Configuration

> All configuration is meant to be passed as build time arguments, however, you are free to modify env vars at runtime at your own risk.

| Required | Variable name | Description | Default | Options |
| --- | --- | --- | --- | --- |
| :heavy_check_mark: | `NG_TOKEN` | Telegram BOT API token | | |
| :x: | `NG_LANG` | Default language to use in new chats | `en` | `be`, `bg`, `cs`, `da`, `de`, `el`, `en`, `es`, `et`, `fi`, `fr`, `hu`, `id`, `it`, `ja`, `ko`, `lt`, `lv`, `nb`, `nl`, `pl`, `pt`, `ro`, `ru`, `sk`, `sl`, `sv`, `tr`, `uk`, `zh` |
| :x: | `NG_HANDLERS` | Enabled bot handlers. Modify to add custom handlers or change invocation order | `admin,gatekeeper,reactor` | Comma-separated list of handlers |
| :x: | `NG_LOG_LEVEL` | Limits the logs spam | `2` | `0`=Panic, `1`=Fatal, `2`=Error, `3`=Warn, `4`=Info, `5`=Debug, `6`=Trace |
| :x: | `NG_DOT_PATH` | Path for bot data storage | `~/.ngbot` | Any valid filesystem path |
| :heavy_check_mark: | `NG_OPENAI_API_KEY` | OpenAI API key for content analysis | | |
| :x: | `NG_OPENAI_MODEL` | OpenAI model to use | `gpt-4o-mini` | Any valid OpenAI model |
| :x: | `NG_OPENAI_BASE_URL` | OpenAI API base URL | `https://api.openai.com/v1` | Any valid OpenAI API compliant base URL |
| :x: | `NG_FLAGGED_EMOJIS` | Emojis used to flag content | `👎🏻,💩` | Comma-separated list of emojis |

## TODO

- [ ] Individual chat's settings (behaviours, timeouts, custom welcome messages, etc).
- [ ] Chat-specific spam filters.
- [ ] Settings UI in private and/or web.

> Feel free to add your requests in issues.
