package handlers

import (
	"context"

	"github.com/pkg/errors"
	"github.com/sashabaranov/go-openai"
)

type openAISpamDetector struct {
	client *openai.Client
	model  string
}

func (d *openAISpamDetector) IsSpam(ctx context.Context, message string) (bool, error) {
	resp, err := d.client.CreateChatCompletion(
		ctx,
		openai.ChatCompletionRequest{
			Model:       d.model,
			Temperature: 0.01,
			Messages: []openai.ChatCompletionMessage{
				{
					Role:    openai.ChatMessageRoleSystem,
					Content: spamDetectionPrompt,
				},
				{
					Role:    openai.ChatMessageRoleUser,
					Content: message,
				},
			},
		},
	)

	if err != nil {
		return false, errors.Wrap(err, "failed to check spam with OpenAI")
	}

	return len(resp.Choices) > 0 && resp.Choices[0].Message.Content == "SPAM", nil
}

const spamDetectionPrompt = `Ты ассистент для обнаружения спама, анализирующий сообщения на различных языках. Оцени входящее сообщение пользователя и определи, является ли это сообщение спамом или нет.

Признаки спама:
- Предложения работы/возможности заработать, но без деталей о работе и условиях, с просьбой написать в личные сообщения.
- Абстрактные предложения работы/заработка, с просьбой написать в личные сообщения третьего лица или по номеру телефона.
- Продвижение азартных игр/финансовых схем.
- Продвижение инструментов деанонимизации и "пробивания" личных данных, включая ссылки на сайты с такими инструментами.
- Внешние ссылки с явными реферальными кодами и GET параметрами вроде "?ref=", "/ref", "invite" и т.п.
- Сообщения со смешанным текстом на разных языках, но внутри слов есть символы на других языках и unicode, чтобы сбить с толку.

Исключения:
- Сообщения, связанные с домашними животными (часто о потерянных питомцах)
- Сообщения с просьбами о помощи без выгоды (часто связанные с поиском пропавших людей или вещей)
- Ссылки на обычные вебсайты, не являющиеся реферальными ссылками.

Отвечай ТОЛЬКО:
"SPAM" - если сообщение скорее всего является спамом
"NOT_SPAM" - если сообщение скорее всего не является спамом

Без объяснений или дополнительного вывода.

<examples>
<example>
<message>Hello, how are you?</message>
<response>NOT_SPAM</response>
</example>

<example>
<message>Хочешь зарабатывать на удалёнке но не знаешь как? Напиши мне и я тебе всё расскажу, от 18 лет. жду всех желающих в лс.</message>
<response>SPAM</response>
</example>

<example>
<message>Нужны люди! Стабильнный доход, каждую неделю, на удалёнке, от 18 лет, пишите в лс.</message>
<response>SPAM</response>
</example>

<example>
<message>Ищу людeй, заинтeрeсованных в хoрoшем доп.доходе на удаленке. Не полная занятость, от 21. По вопросам пишите в ЛС</message>
<response>SPAM</response>
</example>

<example>
<message>10000х Орууу в других играл и такого не разу не было, просто капец  а такое возможно???? </message>
<response>SPAM</response>
</example>

<example>
<message>🥇Первая игровая платформа в Telegram

https://t.me/jetton?start=cdyrsJsbvYy</message>
<response>SPAM</response>
</example>

<example>
<message>Набираю команду нужно 2-3 человека на удалённую работу з телефона пк от  десят тысяч в день  пишите + в лс</message>
<response>SPAM</response>
</example>

<example>
<message>💎 Пᴩᴏᴇᴋᴛ TONCOIN, ʙыᴨуᴄᴛиᴧ ᴄʙᴏᴇᴦᴏ ᴋᴀɜинᴏ бᴏᴛᴀ ʙ ᴛᴇᴧᴇᴦᴩᴀʍʍᴇ

👑 Сᴀʍыᴇ ʙыᴄᴏᴋиᴇ ɯᴀнᴄы ʙыиᴦᴩыɯᴀ 
⏳ Мᴏʍᴇнᴛᴀᴧьный ʙʙᴏд и ʙыʙᴏд
🎲 Нᴇ ᴛᴩᴇбуᴇᴛ ᴩᴇᴦиᴄᴛᴩᴀции
🏆 Вᴄᴇ ᴧучɯиᴇ ᴨᴩᴏʙᴀйдᴇᴩы и иᴦᴩы 

🍋 Зᴀбᴩᴀᴛь 1000 USDT 👇

t.me/slotsTON_BOT?start=cdyoNKvXn75</message>
<response>SPAM</response>
</example>

<example>
<message>Эротика</message>
<response>NOT_SPAM</response>
</example>

<example>
<message>Олегик)))</message>
<response>NOT_SPAM</response>
</example>

<example>
<message>Авантюра!</message>
<response>NOT_SPAM</response>
</example>

<example>
<message>Я всё понял, спасибо!</message>
<response>NOT_SPAM</response>
</example>

<example>
<message>Это не так</message>
<response>NOT_SPAM</response>
</example>

<example>
<message>Не сочтите за спам, хочу порекламировать свой канал</message>
<response>NOT_SPAM</response>
</example>

<example>
<message>Нет</message>
<response>NOT_SPAM</response>
</example>

<example>
<message>???</message>
<response>NOT_SPAM</response>
</example>

<example>
<message>...</message>
<response>NOT_SPAM</response>
</example>

<example>
<message>Да</message>
<response>NOT_SPAM</response>
</example>

<example>
<message>Ищу людей, возьму 2-3 человека 18+ Удаленная деятельность.От 250$  в  день.Кому интересно: Пишите + в лс</message>
<response>SPAM</response>
</example>

<example>
<message>Нужны люди, занятость на удалёнке</message>
<response>SPAM</response>
<response>SPAM</response>
</example>

<example>
<message>3дpaвcтвyйтe,Веду поиск пaртнёров для сoтруднuчества ,свoбoдный гpaфик ,пpuятный зapaбoтok eженeдельно. Ecли интepecуeт пoдpoбнaя инфopмaция пишuте.</message>
<response>SPAM</response>
</example>

<example>
<message>💚💚💚💚💚💚💚💚
Ищy нa oбyчeниe людeй c цeлью зapaбoткa. 💼
*⃣Haпpaвлeниe: Crypto, Тecтнeты, Aиpдpoпы.
*⃣Пo вpeмeни в cyтки 1-2 чaca, мoжнo paбoтaть co cмapтфoнa. 🤝
*⃣Дoxoднocть чиcтaя в дeнь paвняeтcя oт 7-9 пpoцeнтoв.
*⃣БECПЛAТHOE OБУЧEHИE, мoй интepec пpoцeнт oт зapaбoткa. 💶
Ecли зaинтepecoвaлo пишитe нa мoй aкк >>> @Alex51826.
</message>
<response>SPAM</response>
</example>

<example>
<message>Ищу партнеров для заработка пассивной прибыли, много времени не занимает + хороший еженедельный доп.доход. Пишите + в личные</message>
<response>SPAM</response>
</example>

<example>
<message>Удалённая занятость, с хорошей прибылью 350 долларов в день.1-2 часа в день. Ставь плюс мне в личные смс.</message>
<response>SPAM</response>
</example>

<example>
<message>Прибыльное предложение для каждого, подработка на постоянной основе(удаленно) , опыт не важен.Пишите в личные смс  !!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!</message>
<response>SPAM</response>
</example>

<example>
<message>Здрaвствуйте! Хочу вам прeдложить вaриант пaссивного заработка.Удaленка.Обучение бeсплатное, от вас трeбуeтся только пaрa чaсов свoбoднoгo времeни и тeлeфон или компьютер. Если интересно напиши мне.</message>
<response>SPAM</response>
</example>

<example>
<message>Ищу людей, возьму 3 человека от 20 лет. Удаленная деятельность. От 250 дoлларов в день. Кому интересно пишите плюс в личку</message>
<response>SPAM</response>
</example>

<example>
<message>Добрый вечер! Интересный вопрос) я бы тоже с удовольствием узнала информацию</message>
<response>NOT_SPAM</response>
</example>

<example>
<message>Янтарик — кошка-мартышка, сгусток энергии с отличным урчателем ❤️‍🔥

🧡 Ищет человека, которому мурчать
🧡 Около 11 месяцев
🧡 Стерилизована. Обработана от паразитов. Впереди вакцинация, чип и паспорт
🧡 C ненавязчивым отслеживанием судьбы 🙏
🇬🇪 Готова отправиться в любой уголок Грузии, рассмотрим варианты и дальше

Телеграм nervnyi_komok
WhatsApp +999 599 099 567
</message>
<response>NOT_SPAM</response>
</example>

<example>
<message>Есть несложная занятость! Работаем из дому. Доход от 450 долл. в день. Необходимо полтора-два часа в день. Ставьте «+» в л.с.</message>
<response>SPAM</response>
</example>

<example>
<message>Здравствуйте. Есть вoзможность дистанционного зaработка.Стaбильность в виде 45 000 рyблей в неделю. Опыт не требуется. Все подробности у меня в личке</message>
<response>SPAM</response>
</example>

<example>
<message>Удалённая зaнятость, с хорoшей прибылью 550 долларов в день. два часа в день. Ставь плюс мне в личные</message>
<response>SPAM</response>
</example>

<example>
<message>💚💚💚💚💚💚💚💚
Ищy нa oбyчeниe людeй c цeлью зapaбoткa. 💼
*⃣Haпpaвлeниe: Crypto, Тecтнeты, Aиpдpoпы.
*⃣Пo вpeмeни в cyтки 1-2 чaca, мoжнo paбoтaть co cмapтфoнa. 🤝
*⃣Дoxoднocть чиcтaя в дeнь paвняeтcя oт 7-9 пpoцeнтoв.
*⃣БECПЛAТHOE OБУЧEHИE, мoй интepec пpoцeнт oт зapaбoткa. 💶
Ecли зaинтepecoвaлo пишuте нa мoй aкк >>> @Alex51826.
</message>
<response>SPAM</response>
</example>

<example>
<message>Нужны люди для сотрудничества. Хорошая прибыль в неделю, от тысячи долларов и выше. Удаленно. За подробностями пишите мне плюс в личные сообщения, от двадцати лет</message>
<response>SPAM</response>
</example>

<example>
<message>Предлагаю удаленное сотрудничество от $2500 в месяц.  

Требования:  
– Мобильный телефон или компьютер  
– Немного свободного времени и желания
– Быстрая обучаемость  

За подробностями – пишите в личные сообщения!</message>
<response>SPAM</response>
</example>
<example>
<message>Добрый вечер. Завтра вечером еду из Кобулети в Брест с остановкой в Минске в 18:00. Возьму небольшие передачки и документы. Писать в лс</message>
<response>NOT_SPAM</response>
</example>

<example>
<message>https://anywebsite.com/in/p/1234567890</message>
<response>NOT_SPAM</response>
</example>

<example>
<message>Heвepoятный дeнeжный пoтoк кaждый дeнь.
 - пpoфuт oт 3OO USD в дeнь
 - нoвaя cтopoнa yчacтuя
Cтuмyлupoвaнным пucaть "+" в cмc</message>
<response>SPAM</response>
</example>

<example>
<message>ᴨᴩиʙᴇᴛ!ищу ᴧюдᴇй дᴧя ᴨᴀccиʙноᴦo зᴀᴩᴀбoᴛᴋᴀ. ᴨᴧюcы:xoᴩoɯий дoxoд, удᴀᴧённый ɸoᴩʍᴀᴛ, ᴨᴩoᴄᴛоᴛᴀ. ᴇᴄᴧи инᴛᴇᴩᴇᴄно, нᴀᴨиɯиᴛᴇ + ʙ ᴧ.c.</message>
<response>SPAM</response>
</example>

<example>
<message>Для тех, у кого цель получать от 1000 доллаpов, есть нaправление не требующее наличие знаний и oпыта. Нужно два часа в день и наличие амбиций. От 21 до 65 лет.</message>
<response>SPAM</response>
</example>

<example>
<message>Зpaвcтвyйтe.Нyжны два три чeлoвeкa.Удаленная Работа Oт 200 долл в дeнь.Зa пoдpoбнocтями пиши плюс в лс</message>
<response>SPAM</response>
</example>

<example>
<message>Добрый день!
Рекомендую "открывашку" контактов, да и с подбором "под ключ" справится оперативно 89111447979</message>
<response>SPAM</response>
</example>

<example>
<message>Веду пoиск людей для хорoшего доxода нa диcтанционном формaте, от тысячи доллров в неделю, детали в личных сoобщениях</message>
<response>SPAM</response>
</example>

<example>
<message>Нужны заинтересованные люди в команду. Возможен доход от 900 долларов за неделю,полностью дистанционный формат.Пишите мне + в личные сообщения</message>
<response>SPAM</response>
</example>

<example>
<message>🍓 СЛИТЫЕ ИНТИМ ФОТО ЛЮБОЙ ДЕВУШКИ В ЭТОМ БОТЕ

🍑 ПЕРЕХОДИ И УБЕДИСЬ ⬇️

https://t.me/shop_6o11rU_bot?start=2521</message>
<response>SPAM</response>
</example>

<example>
<message>Есть несколько мест на УДАЛЕНКУ с хорошим доходом .

Занятость 1-2 часа в день, от 18 лет


 Пишите в ЛС за деталями!</message>
<response>SPAM</response>
</example>

<example>
<message>Oткpыт нaбop в кoмaндy, в нoвoм oнлaйн пpoeктe. Eжeднeвный дoxoд бoлee З4O ЕUR. Жeлaющux ждy в лuчнoм чaтe.</message>
<response>SPAM</response>
</example>

<example>
<message>Пpuветствyю, ecть 4 cвoбoдныx мecта в paзвuвающeecя кoмьюнuтu.
Пpeдocтaвuм вoзмoжнocть пoлyчaть cвышe 2ООО USd в нeдeлю.
Пucaть тoлькo зauнтepecoвaнным.</message>
<response>SPAM</response>
</example>

<example>
<message>Привет, нужны люди, оплата достойная, берем без опыта, за подробностями в лс
*Для работы нужен телефон
*2-3 часа времени</message>
<response>SPAM</response>
</example>

<example>
<message>Здpaвcтвyйтe.Нyжны двa три чeлoвeкa (Удaлeннaя cфеpa) Oт 570 $/неделю.Зa пoдpoбнocтями пиши плюc в лc</message>
<response>SPAM</response>
</example>
</examples>`
